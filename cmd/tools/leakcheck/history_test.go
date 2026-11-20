package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// hist runs `leakcheck history args...` in the repo and returns exit code, stdout and stderr.
func (r *repo) hist(args ...string) (int, string, string) {
	r.t.Helper()
	var stdout, stderr bytes.Buffer
	code := runHistory(args, r.dir, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// gitIn runs git in the repo with stdin and returns its trimmed stdout.
func (r *repo) gitIn(stdin string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// fastImport feeds one git fast-import stream to the repo: the whole fixture history in one git process (EG0059).
func (r *repo) fastImport(stream string) {
	r.t.Helper()
	r.gitIn(stream, "fast-import", "--quiet")
}

// fiData is a fast-import data block.
func fiData(s string) string { return fmt.Sprintf("data %d\n%s\n", len(s), s) }

// wantHist asserts the exit code and the exact stdout lines (order-insensitive except the last, the count line).
func wantHist(t *testing.T, code int, stdout string, wantCode int, want ...string) {
	t.Helper()
	got := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	last := got[len(got)-1]
	body := append([]string{}, got[:len(got)-1]...)
	sort.Strings(body)
	wantBody := append([]string{}, want[:len(want)-1]...)
	sort.Strings(wantBody)
	if code != wantCode || last != want[len(want)-1] || strings.Join(body, "\n") != strings.Join(wantBody, "\n") {
		t.Fatalf("exit %d, stdout %q; want exit %d, lines %q", code, stdout, wantCode, want)
	}
}

// TestHistoryLeakyRepo runs every surface, REV isolation, allow rules, -root and dispatch on one repo, in five scans.
func TestHistoryLeakyRepo(t *testing.T) {
	r := newRepo(t, testTerms)
	const me = "t <t@example.invalid>"
	var s strings.Builder
	for i, c := range []struct {
		author, committer, msg string
		files                  []string // path, content pairs; empty content deletes the path
	}{
		{me, me, "start", []string{"clean.txt", "same content\n"}},                                                                     // c0
		{me, me, "add", []string{"a.txt", "one\nuse zork here\n"}},                                                                     // c1
		{me, me, "remove", []string{"a.txt", ""}},                                                                                      // c2: a removed blob
		{me, me, "copy", []string{"zork/dup.txt", "same content\n"}},                                                                   // c3: the clean blob under a term path
		{me, me, "zork and acme-real\nfine line", []string{"zork/dup.txt", ""}},                                                        // c4
		{"Fine <zork@example.invalid>", me, "second", nil},                                                                             // c5: author
		{"Fine <fine@example.invalid>", "zork <ok@example.invalid>", "third", nil},                                                     // c6: committer
		{me, me, "zork msg\nzork x leakcheck:allow", []string{"allow.txt", "zork # leakcheck:allow\r\n  zork secret  \r\nzork bad\n"}}, // c7
	} {
		msg := c.msg + "\n"
		fmt.Fprintf(&s, "commit refs/heads/main\nauthor %s %d +0000\ncommitter %s %d +0000\ndata %d\n%s\n",
			c.author, 1700000000+i, c.committer, 1700000000+i, len(msg), msg)
		for j := 0; j < len(c.files); j += 2 {
			if c.files[j+1] == "" {
				fmt.Fprintf(&s, "D %s\n", c.files[j])
			} else {
				fmt.Fprintf(&s, "M 100644 inline %s\ndata %d\n%s\n", c.files[j], len(c.files[j+1]), c.files[j+1])
			}
		}
	}
	r.fastImport(s.String())
	ids := strings.Fields(r.git("rev-list", "--reverse", "main"))
	if len(ids) != 8 {
		t.Fatalf("fixture commits %q, want 8", ids)
	}
	c0, c1, c3, c4, c5, c6, c7 := ids[0], ids[1], ids[3], ids[4], ids[5], ids[6], ids[7]

	// One commit alone is clean (no blob, path, message or identity hit of the later commits), through run.
	var so, se bytes.Buffer
	if code := run([]string{"history", c0}, r.dir, &so, &se); code != 0 || so.String() != "leakcheck: history: 0 hits in 1 commits\n" {
		t.Fatalf("run history: exit %d, stdout %q, stderr %q", code, so.String(), se.String())
	}

	// -root: absolute and relative to the working directory.
	r.write("rootx/.ai/leakcheck-terms.txt", "same content\n")
	for _, root := range []string{filepath.Join(r.dir, "rootx"), "rootx"} {
		code, out, _ := r.hist("-root", root, c0)
		wantHist(t, code, out, 1,
			"leakcheck: blob "+c0+" clean.txt:1: same content",
			"leakcheck: history: 1 hits in 1 commits")
	}

	// Tags and a term branch, added last.
	r.git("-c", "user.name=zork", "-c", "user.email=ok@example.invalid",
		"tag", "-a", "-m", "line one\nacme-real here", "v1")
	r.git("branch", "zork-branch")

	// REV c3..c7 limits blob, path, message and identity hits to c4..c7 (c1's blob and c3's path stay out); tag and
	// ref hits appear whatever REV is. The marker hides allow.txt:1 and message line 2 of c7.
	code, out, _ := r.hist(c3 + ".." + c7)
	wantHist(t, code, out, 1,
		"leakcheck: message "+c4+" line 1: acme-real, zork",
		"leakcheck: identity "+c5+" author: zork",
		"leakcheck: identity "+c6+" committer: zork",
		"leakcheck: blob "+c7+" allow.txt:2: zork",
		"leakcheck: blob "+c7+" allow.txt:3: zork",
		"leakcheck: message "+c7+" line 1: zork",
		"leakcheck: tag refs/tags/v1 line 2: acme-real",
		"leakcheck: tag refs/tags/v1 tagger: zork",
		"leakcheck: ref refs/heads/zork-branch: zork",
		"leakcheck: history: 9 hits in 4 commits")

	// Whole history with -allow FILE (BOM before a real allow line, CRLF, comment, blank): it drops the CRLF blob
	// line allow.txt:2 and message line 1 of c7 that the run above reports.
	allow := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(allow, []byte("\xef\xbb\xbfzork secret \r\n# comment\n\nzork msg\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = r.hist("-allow", allow)
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" a.txt:2: zork",
		"leakcheck: path "+c3+" zork/dup.txt: zork",
		"leakcheck: message "+c4+" line 1: acme-real, zork",
		"leakcheck: identity "+c5+" author: zork",
		"leakcheck: identity "+c6+" committer: zork",
		"leakcheck: blob "+c7+" allow.txt:3: zork",
		"leakcheck: tag refs/tags/v1 line 2: acme-real",
		"leakcheck: tag refs/tags/v1 tagger: zork",
		"leakcheck: ref refs/heads/zork-branch: zork",
		"leakcheck: history: 9 hits in 8 commits")
}

// TestHistoryTreeWalk covers the one-process tree walk (EG0070): a fixed git process count, subtree reuse under a
// new prefix, tree order, gitlinks, a dangling ref, tags, sha256 and fail-closed reads of broken trees.
func TestHistoryTreeWalk(t *testing.T) {
	r := newRepo(t, testTerms)
	const me = "t <t@example.invalid>"
	commit := func(ts int, changes string) string {
		return fmt.Sprintf("commit refs/heads/main\nauthor %s %d +0000\ncommitter %s %d +0000\n%s", me, ts, me, ts, fiData("m\n")) + changes
	}
	file := func(path, content string) string { return "M 100644 inline " + path + "\n" + fiData(content) }
	r.fastImport(
		// A: the subtree docs/sub under a clean directory.
		commit(1700000001, file("docs/sub/f.txt", "fine\n")) +
			// B: the same subtree object under a term directory: zork/ has the same tree id as docs/.
			commit(1700000002, file("zork/sub/f.txt", "fine\n")) +
			// C: docs/ again at the same prefix; one term blob at a.txt and a/x.txt; a gitlink to an absent commit.
			commit(1700000003, "D zork\n"+file("a.txt", "acme-real here\n")+file("a/x.txt", "acme-real here\n")+
				"M 160000 1111111111111111111111111111111111111111 zork-mod\n") +
			"tag v1\nfrom refs/heads/main\ntagger zork <ok@example.invalid> 1700000004 +0000\n" + fiData("clean\n") +
			"tag v2\nfrom refs/heads/main\ntagger " + me + " 1700000005 +0000\n" + fiData("line one\nzork here\n"))
	ids := strings.Fields(r.git("rev-list", "--reverse", "main"))
	if len(ids) != 3 {
		t.Fatalf("fixture commits %q, want 3", ids)
	}
	b, c := ids[1], ids[2]
	// A dangling ref is only name-scanned (git log --all would refuse it, so the scans name main).
	r.write(".git/refs/heads/zork-dangling", "2222222222222222222222222222222222222222\n")

	want := []string{
		"leakcheck: blob " + c + " a.txt:1: acme-real",
		"leakcheck: path " + b + " zork/sub/f.txt: zork",
		"leakcheck: tag refs/tags/v1 tagger: zork",
		"leakcheck: tag refs/tags/v2 line 2: zork",
		"leakcheck: ref refs/heads/zork-dangling: zork",
		"leakcheck: history: 5 hits in 3 commits",
	}
	for _, tc := range []struct {
		args  []string
		procs int
	}{{[]string{"-root", r.dir, "main"}, 3}, {[]string{"main"}, 4}} {
		before := gitStarts
		code, out, _ := r.hist(tc.args...)
		if got := gitStarts - before; got != tc.procs {
			t.Errorf("history %q started %d git processes, want %d", tc.args, got, tc.procs)
		}
		wantHist(t, code, out, 1, want...)
	}

	// Fail closed on a missing subtree, a blob under a tree entry and a truncated tree.
	blob, err := hex.DecodeString(strings.TrimSpace(r.git("rev-parse", "main:docs/sub/f.txt")))
	if err != nil {
		t.Fatal(err)
	}
	for name, tree := range map[string]string{
		"missing subtree": r.gitIn("040000 tree 3333333333333333333333333333333333333333\tsub\n", "mktree", "--missing"),
		"blob as tree":    r.gitIn("40000 sub\x00"+string(blob), "hash-object", "-t", "tree", "--literally", "-w", "--stdin"),
		"truncated tree":  r.gitIn("100644 f\x00abc", "hash-object", "-t", "tree", "--literally", "-w", "--stdin"),
	} {
		id := strings.TrimSpace(r.git("commit-tree", tree, "-m", name))
		if code, _, stderr := r.hist("-root", r.dir, id); code != 1 || !strings.Contains(stderr, "leakcheck: error:") {
			t.Errorf("%s: exit %d, stderr %q; want exit 1 and an error line", name, code, stderr)
		}
	}

	// sha256: 32-byte ids in tree entries.
	r2 := &repo{t: t, dir: t.TempDir()}
	r2.git("init", "-q", "-b", "main", "--object-format=sha256")
	r2.write(termsRel, testTerms)
	r2.fastImport(commit(1700000001, file("a/b/c.txt", "zork\n")))
	id := strings.TrimSpace(r2.git("rev-parse", "main"))
	code, out, _ := r2.hist("-root", r2.dir)
	wantHist(t, code, out, 1,
		"leakcheck: blob "+id+" a/b/c.txt:1: zork",
		"leakcheck: history: 1 hits in 1 commits")
}

// TestHistoryNoRepo covers usage and error exits without a repository.
func TestHistoryNoRepo(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, termsRel), []byte(testTerms), 0o644); err != nil {
		t.Fatal(err)
	}
	var so, se bytes.Buffer
	if code := run(nil, dir, &so, &se); code != 2 || !strings.Contains(se.String(), "history") {
		t.Fatalf("usage: exit %d, stderr %q", code, se.String())
	}
	h := func(args ...string) (int, string) {
		var o, e bytes.Buffer
		return runHistory(args, dir, &o, &e), e.String()
	}
	if code, stderr := h("-root", t.TempDir()); code != 1 || !strings.Contains(stderr, "leakcheck: error:") {
		t.Fatalf("missing terms file: exit %d, stderr %q", code, stderr)
	}
	if code, stderr := h("-root", root, "-allow", filepath.Join(dir, "nope.txt")); code != 1 || !strings.Contains(stderr, "leakcheck: error:") {
		t.Fatalf("missing -allow file: exit %d, stderr %q", code, stderr)
	}
	if code, _ := h("-bogus"); code != 2 {
		t.Fatalf("bad flag: exit %d, want 2", code)
	}
	if code, _ := h("-root", root, "HEAD", "-x"); code != 2 {
		t.Fatalf("flag after a REV: exit %d, want 2", code)
	}
	if code, _ := h("-root", root, "--all"); code != 2 {
		t.Fatalf("explicit --all: exit %d, want 2", code)
	}
}
