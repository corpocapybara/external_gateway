package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// histCommit is one commit of `git log` with the fields history scans.
type histCommit struct {
	sha, tree                          string
	authorName, authorMail             string
	committerName, committerMail, body string
}

// blobRef is a distinct blob id with the first commit and path that listed it.
type blobRef struct{ oid, commit, path string }

// runHistory scans every commit reachable from the REVs (default --all) for leak terms: blobs, paths, messages,
// identities, plus all annotated tags and ref names (EG0058). Exit 0 clean, 1 hit or error, 2 usage.
func runHistory(args []string, dir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "directory whose "+termsRel+" holds the terms (default: the main checkout)")
	allowFile := fs.String("allow", "", "file of line texts to skip")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	revs := fs.Args()
	for _, r := range revs {
		if strings.HasPrefix(r, "-") {
			fmt.Fprintf(stderr, "leakcheck: history: bad revision %q\n", r)
			return 2
		}
	}
	rootDir := *root
	if rootDir == "" {
		m, err := mainCheckout(dir)
		if err != nil {
			return fail(stderr, err)
		}
		rootDir = m
	} else if !filepath.IsAbs(rootDir) {
		rootDir = filepath.Join(dir, rootDir)
	}
	terms, err := loadTerms(rootDir)
	if err != nil {
		return fail(stderr, err)
	}
	allow := map[string]bool{}
	if *allowFile != "" {
		p := *allowFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fail(stderr, err)
		}
		for _, l := range strings.Split(strings.TrimPrefix(string(data), "\xef\xbb\xbf"), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			allow[l] = true
		}
	}

	// lineScan returns the terms of each non-skipped line of text, numbered from 1.
	lineScan := func(text string, fn func(n int, found []string)) {
		for i, line := range strings.Split(text, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if allow[strings.TrimSpace(line)] {
				continue
			}
			if found := lineHits(line, terms); len(found) > 0 {
				fn(i+1, found)
			}
		}
	}
	join := func(f []string) string { return strings.Join(f, ", ") }

	commits, err := historyCommits(dir, revs)
	if err != nil {
		return fail(stderr, err)
	}

	var blobHits, pathHits, msgHits, idHits, tagHits, refHits []string

	// Trees, paths and blobs.
	treeEntries := map[string][]struct{ oid, path string }{}
	seenPath := map[string]bool{}
	seenBlob := map[string]bool{}
	var blobs []blobRef
	for _, c := range commits {
		entries, ok := treeEntries[c.tree]
		if !ok {
			out, err := git(dir, "-c", "core.quotePath=false", "ls-tree", "-r", "-z", "--full-tree", c.tree)
			if err != nil {
				return fail(stderr, err)
			}
			for _, rec := range strings.Split(string(out), "\x00") {
				if rec == "" {
					continue
				}
				meta, path, found := strings.Cut(rec, "\t")
				f := strings.Fields(meta)
				if !found || len(f) != 3 {
					return fail(stderr, fmt.Errorf("ls-tree %s: bad record %q", c.tree, rec))
				}
				if f[1] != "blob" { // gitlinks (160000) and anything else that is not a file
					continue
				}
				entries = append(entries, struct{ oid, path string }{f[2], path})
			}
			treeEntries[c.tree] = entries
		}
		for _, e := range entries {
			if !seenPath[e.path] {
				seenPath[e.path] = true
				if found := matchTerms(e.path, terms); len(found) > 0 {
					pathHits = append(pathHits, fmt.Sprintf("leakcheck: path %s %s: %s", c.sha, e.path, join(found)))
				}
			}
			if !seenBlob[e.oid] {
				seenBlob[e.oid] = true
				blobs = append(blobs, blobRef{e.oid, c.sha, e.path})
			}
		}
		lineScan(c.body, func(n int, found []string) {
			msgHits = append(msgHits, fmt.Sprintf("leakcheck: message %s line %d: %s", c.sha, n, join(found)))
		})
		if found := matchTerms(c.authorName+" "+c.authorMail, terms); len(found) > 0 {
			idHits = append(idHits, fmt.Sprintf("leakcheck: identity %s author: %s", c.sha, join(found)))
		}
		if found := matchTerms(c.committerName+" "+c.committerMail, terms); len(found) > 0 {
			idHits = append(idHits, fmt.Sprintf("leakcheck: identity %s committer: %s", c.sha, join(found)))
		}
	}
	if err := scanBlobs(dir, blobs, func(b blobRef, content string) {
		lineScan(content, func(n int, found []string) {
			blobHits = append(blobHits, fmt.Sprintf("leakcheck: blob %s %s:%d: %s", b.commit, b.path, n, join(found)))
		})
	}); err != nil {
		return fail(stderr, err)
	}

	// Tags and refs, whatever REV is.
	tags, err := git(dir, "for-each-ref", "--format=%(objecttype) %(refname)", "refs/tags")
	if err != nil {
		return fail(stderr, err)
	}
	for _, l := range strings.Split(string(tags), "\n") {
		l = strings.TrimSuffix(l, "\r")
		typ, ref, ok := strings.Cut(l, " ")
		if !ok || typ != "tag" {
			continue
		}
		raw, err := git(dir, "cat-file", "tag", ref)
		if err != nil {
			return fail(stderr, err)
		}
		head, msg, _ := strings.Cut(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n\n")
		for _, h := range strings.Split(head, "\n") {
			if rest, ok := strings.CutPrefix(h, "tagger "); ok {
				if i := strings.LastIndex(rest, ">"); i >= 0 {
					rest = rest[:i+1]
				}
				if found := matchTerms(rest, terms); len(found) > 0 {
					tagHits = append(tagHits, fmt.Sprintf("leakcheck: tag %s tagger: %s", ref, join(found)))
				}
			}
		}
		lineScan(msg, func(n int, found []string) {
			tagHits = append(tagHits, fmt.Sprintf("leakcheck: tag %s line %d: %s", ref, n, join(found)))
		})
	}
	refs, err := git(dir, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return fail(stderr, err)
	}
	for _, ref := range strings.Split(string(refs), "\n") {
		ref = strings.TrimSuffix(ref, "\r")
		if ref == "" {
			continue
		}
		if found := matchTerms(ref, terms); len(found) > 0 {
			refHits = append(refHits, fmt.Sprintf("leakcheck: ref %s: %s", ref, join(found)))
		}
	}

	n := 0
	for _, hs := range [][]string{blobHits, pathHits, msgHits, idHits, tagHits, refHits} {
		for _, h := range hs {
			fmt.Fprintln(stdout, h)
			n++
		}
	}
	fmt.Fprintf(stdout, "leakcheck: history: %d hits in %d commits\n", n, len(commits))
	if n > 0 {
		return 1
	}
	return 0
}

// historyCommits lists the commits and their scanned fields in one git call. Records are NUL-terminated
// (-z) with seven NUL-separated fields each; the message is last and holds no NUL.
func historyCommits(dir string, revs []string) ([]histCommit, error) {
	args := []string{"-c", "core.quotePath=false", "log", "--no-color", "-z",
		"--format=%H%x00%T%x00%an%x00%ae%x00%cn%x00%ce%x00%B"}
	if len(revs) == 0 {
		args = append(args, "--all")
	} else {
		args = append(args, revs...)
	}
	args = append(args, "--")
	out, err := git(dir, args...)
	if err != nil {
		return nil, err
	}
	f := strings.Split(string(out), "\x00")
	if len(f) > 0 && f[len(f)-1] == "" {
		f = f[:len(f)-1]
	}
	if len(f)%7 != 0 {
		return nil, fmt.Errorf("git log: unexpected output framing (%d fields)", len(f))
	}
	var commits []histCommit
	for i := 0; i < len(f); i += 7 {
		commits = append(commits, histCommit{
			sha: strings.TrimSpace(f[i]), tree: f[i+1], authorName: f[i+2], authorMail: f[i+3],
			committerName: f[i+4], committerMail: f[i+5], body: f[i+6],
		})
	}
	return commits, nil
}

// scanBlobs reads each blob once through one `git cat-file --batch` process and calls fn with its content.
func scanBlobs(dir string, blobs []blobRef, fn func(b blobRef, content string)) error {
	if len(blobs) == 0 {
		return nil
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		w := bufio.NewWriter(in)
		for _, b := range blobs {
			w.WriteString(b.oid + "\n")
		}
		w.Flush()
		in.Close()
	}()
	r := bufio.NewReaderSize(out, 1<<16)
	var rerr error
	for _, b := range blobs {
		header, err := r.ReadString('\n')
		if err != nil {
			rerr = fmt.Errorf("git cat-file --batch: reading header of %s: %v", b.oid, err)
			break
		}
		f := strings.Fields(header)
		if len(f) == 2 && f[1] == "missing" {
			rerr = fmt.Errorf("git cat-file --batch: blob %s (%s) is missing", b.oid, b.path)
			break
		}
		size := -1
		if len(f) == 3 {
			size, _ = strconv.Atoi(f[2])
		}
		if size < 0 || f[1] != "blob" {
			rerr = fmt.Errorf("git cat-file --batch: unexpected header %q", strings.TrimSpace(header))
			break
		}
		buf := make([]byte, size+1)
		if _, err := io.ReadFull(r, buf); err != nil {
			rerr = fmt.Errorf("git cat-file --batch: reading blob %s: %v", b.oid, err)
			break
		}
		fn(b, string(buf[:size]))
	}
	if rerr != nil {
		cmd.Process.Kill()
		io.Copy(io.Discard, out)
		cmd.Wait()
		return rerr
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git cat-file --batch: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
