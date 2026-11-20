package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
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
	refs, err := git(dir, "--no-replace-objects", "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return fail(stderr, err)
	}

	// One cat-file process reads every tree, blob and tag object (EG0070).
	cf := &catFile{dir: dir}
	defer cf.kill()

	var blobHits, pathHits, msgHits, idHits, tagHits, refHits []string

	// Trees, paths and blobs. Each tree is walked depth-first in stored order, the order `ls-tree -r` lists. A
	// (prefix, tree) pair already walked is skipped: its paths and blobs were all seen then.
	trees := map[string][]treeEntry{}
	walked := map[string]bool{}
	seenPath := map[string]bool{}
	seenBlob := map[string]bool{}
	var blobs []blobRef
	var walk func(c histCommit, tree, prefix string) error
	walk = func(c histCommit, tree, prefix string) error {
		key := prefix + "\x00" + tree
		if walked[key] {
			return nil
		}
		walked[key] = true
		entries, ok := trees[tree]
		if !ok {
			data, err := cf.read(tree, "tree")
			if err != nil {
				return err
			}
			if entries, err = parseTree(tree, data); err != nil {
				return err
			}
			trees[tree] = entries
		}
		for _, e := range entries {
			path := prefix + e.name
			switch e.mode & 0o170000 {
			case 0o040000:
				if err := walk(c, e.oid, path+"/"); err != nil {
					return err
				}
				continue
			case 0o160000: // gitlink: the commit of another repository, not a file
				continue
			}
			if !seenPath[path] {
				seenPath[path] = true
				if found := matchTerms(path, terms); len(found) > 0 {
					pathHits = append(pathHits, fmt.Sprintf("leakcheck: path %s %s: %s", c.sha, path, join(found)))
				}
			}
			if !seenBlob[e.oid] {
				seenBlob[e.oid] = true
				blobs = append(blobs, blobRef{e.oid, c.sha, path})
			}
		}
		return nil
	}
	for _, c := range commits {
		if err := walk(c, c.tree, ""); err != nil {
			return fail(stderr, err)
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
	for _, b := range blobs {
		data, err := cf.read(b.oid, "blob")
		if err != nil {
			return fail(stderr, fmt.Errorf("%v (%s)", err, b.path))
		}
		lineScan(string(data), func(n int, found []string) {
			blobHits = append(blobHits, fmt.Sprintf("leakcheck: blob %s %s:%d: %s", b.commit, b.path, n, join(found)))
		})
	}

	// Tags and refs, whatever REV is. A ref under refs/tags is an annotated tag when its object is a tag object.
	for _, l := range strings.Split(string(refs), "\n") {
		l = strings.TrimSuffix(l, "\r")
		oid, ref, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		if found := matchTerms(ref, terms); len(found) > 0 {
			refHits = append(refHits, fmt.Sprintf("leakcheck: ref %s: %s", ref, join(found)))
		}
		if !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		typ, raw, err := cf.readAny(oid)
		if err != nil {
			return fail(stderr, fmt.Errorf("%v (%s)", err, ref))
		}
		if typ != "tag" {
			continue
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
	if err := cf.finish(); err != nil {
		return fail(stderr, err)
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
	args := []string{"--no-replace-objects", "-c", "core.quotePath=false", "log", "--no-color", "--no-show-signature", "-z",
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

// treeEntry is one entry of a raw tree object: its octal mode, name and object id (hex).
type treeEntry struct {
	mode      uint32
	name, oid string
}

// parseTree splits a raw tree object (`<octal mode> <name>\0<raw id>` records) in stored order. The raw id is as
// long as the tree's own id: 20 bytes for sha1, 32 for sha256.
func parseTree(id string, data []byte) ([]treeEntry, error) {
	idLen := len(id) / 2
	var entries []treeEntry
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		nul := bytes.IndexByte(data, 0)
		if sp <= 0 || nul < sp || nul+1+idLen > len(data) {
			return nil, fmt.Errorf("tree %s: malformed entry", id)
		}
		mode, err := strconv.ParseUint(string(data[:sp]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("tree %s: bad mode %q", id, data[:sp])
		}
		entries = append(entries, treeEntry{uint32(mode), string(data[sp+1 : nul]), hex.EncodeToString(data[nul+1 : nul+1+idLen])})
		data = data[nul+1+idLen:]
	}
	return entries, nil
}

// catFile is one `git cat-file --batch` process, started at the first read and driven one object at a time.
type catFile struct {
	dir    string
	cmd    *exec.Cmd
	in     io.WriteCloser
	w      *bufio.Writer
	r      *bufio.Reader
	stderr strings.Builder
}

// read returns the content of the object id, which must have type want.
func (c *catFile) read(id, want string) ([]byte, error) {
	typ, data, err := c.readAny(id)
	if err != nil {
		return nil, err
	}
	if typ != want {
		return nil, fmt.Errorf("git cat-file --batch: object %s is a %s, not a %s", id, typ, want)
	}
	return data, nil
}

// readAny returns the type and content of the object id; a missing object is an error.
func (c *catFile) readAny(id string) (string, []byte, error) {
	if c.cmd == nil {
		c.cmd = gitCommand(c.dir, "--no-replace-objects", "cat-file", "--batch")
		c.cmd.Stderr = &c.stderr
		in, err := c.cmd.StdinPipe()
		if err != nil {
			return "", nil, err
		}
		out, err := c.cmd.StdoutPipe()
		if err != nil {
			return "", nil, err
		}
		if err := c.cmd.Start(); err != nil {
			return "", nil, err
		}
		c.in, c.w, c.r = in, bufio.NewWriter(in), bufio.NewReaderSize(out, 1<<16)
	}
	if _, err := c.w.WriteString(id + "\n"); err != nil {
		return "", nil, fmt.Errorf("git cat-file --batch: writing %s: %v", id, err)
	}
	if err := c.w.Flush(); err != nil {
		return "", nil, fmt.Errorf("git cat-file --batch: writing %s: %v", id, err)
	}
	header, err := c.r.ReadString('\n')
	if err != nil {
		return "", nil, fmt.Errorf("git cat-file --batch: reading header of %s: %v", id, err)
	}
	f := strings.Fields(header)
	if len(f) == 2 && f[1] == "missing" {
		return "", nil, fmt.Errorf("git cat-file --batch: object %s is missing", id)
	}
	size := -1
	if len(f) == 3 {
		size, _ = strconv.Atoi(f[2])
	}
	if size < 0 {
		return "", nil, fmt.Errorf("git cat-file --batch: unexpected header %q", strings.TrimSpace(header))
	}
	buf := make([]byte, size+1) // the content and its trailing LF
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return "", nil, fmt.Errorf("git cat-file --batch: reading %s: %v", id, err)
	}
	return f[1], buf[:size], nil
}

// finish ends the process after the last read; a failed exit is an error.
func (c *catFile) finish() error {
	if c.cmd == nil {
		return nil
	}
	c.in.Close()
	err := c.cmd.Wait()
	c.cmd = nil
	if err != nil {
		return fmt.Errorf("git cat-file --batch: %v: %s", err, strings.TrimSpace(c.stderr.String()))
	}
	return nil
}

// kill stops a process still running (an early return).
func (c *catFile) kill() {
	if c.cmd == nil {
		return
	}
	c.in.Close()
	c.cmd.Process.Kill()
	c.cmd.Wait()
	c.cmd = nil
}
