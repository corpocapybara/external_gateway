package main

import (
	"bytes"
	"os"
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

func (r *repo) head() string {
	r.t.Helper()
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

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

// TestHistoryLeakyRepo runs every surface, REV isolation, allow rules, -root and dispatch on one repo.
func TestHistoryLeakyRepo(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("clean.txt", "same content\n")
	r.commitAll("start")
	c0 := r.head()
	r.write("a.txt", "one\nuse zork here\n")
	r.commitAll("add")
	c1 := r.head()
	r.git("rm", "-q", "a.txt")
	r.commitAll("remove")
	c2 := r.head()
	r.write("zork/dup.txt", "same content\n") // the clean blob under a term path
	r.commitAll("copy")
	c3 := r.head()
	r.git("rm", "-q", "-r", "zork")
	r.commitAll("zork and acme-real\nfine line")
	c4 := r.head()
	r.git("commit", "-q", "--no-verify", "--allow-empty", "-m", "second", "--author=Fine <zork@example.invalid>")
	c5 := r.head()
	r.git("-c", "user.name=zork", "-c", "user.email=ok@example.invalid",
		"commit", "-q", "--no-verify", "--allow-empty", "-m", "third", "--author=Fine <fine@example.invalid>")
	c6 := r.head()
	r.write("allow.txt", "zork # leakcheck:allow\r\n  zork secret  \r\nzork bad\n")
	r.commitAll("zork msg\nzork x leakcheck:allow")
	c7 := r.head()

	// One commit alone is clean.
	code, out, _ := r.hist(c0)
	wantHist(t, code, out, 0, "leakcheck: history: 0 hits in 1 commits")
	// A removed blob.
	code, out, _ = r.hist(c0 + ".." + c2)
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" a.txt:2: zork",
		"leakcheck: history: 1 hits in 2 commits")
	// A clean blob copied to a term path.
	code, out, _ = r.hist(c2 + ".." + c3)
	wantHist(t, code, out, 1,
		"leakcheck: path "+c3+" zork/dup.txt: zork",
		"leakcheck: history: 1 hits in 1 commits")
	// Message, author, committer.
	code, out, _ = r.hist(c3 + ".." + c4)
	wantHist(t, code, out, 1,
		"leakcheck: message "+c4+" line 1: acme-real, zork",
		"leakcheck: history: 1 hits in 1 commits")
	code, out, _ = r.hist(c4 + ".." + c5)
	wantHist(t, code, out, 1,
		"leakcheck: identity "+c5+" author: zork",
		"leakcheck: history: 1 hits in 1 commits")
	code, out, _ = r.hist(c5 + ".." + c6)
	wantHist(t, code, out, 1,
		"leakcheck: identity "+c6+" committer: zork",
		"leakcheck: history: 1 hits in 1 commits")

	// Allow marker, then -allow FILE (BOM before a real allow line, CRLF, comment, blank).
	rng := c6 + ".." + c7
	code, out, _ = r.hist(rng)
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c7+" allow.txt:2: zork",
		"leakcheck: blob "+c7+" allow.txt:3: zork",
		"leakcheck: message "+c7+" line 1: zork",
		"leakcheck: history: 3 hits in 1 commits")
	allow := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(allow, []byte("\xef\xbb\xbfzork secret \r\n# comment\n\nzork msg\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = r.hist("-allow", allow, rng)
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c7+" allow.txt:3: zork",
		"leakcheck: history: 1 hits in 1 commits")

	// -root: absolute and relative to the working directory.
	r.write("rootx/.ai/leakcheck-terms.txt", "same content\n")
	for _, root := range []string{filepath.Join(r.dir, "rootx"), "rootx"} {
		code, out, _ = r.hist("-root", root, c0)
		wantHist(t, code, out, 1,
			"leakcheck: blob "+c0+" clean.txt:1: same content",
			"leakcheck: history: 1 hits in 1 commits")
	}

	// Dispatch through run.
	var so, se bytes.Buffer
	if code := run([]string{"history", c0}, r.dir, &so, &se); code != 0 || so.String() != "leakcheck: history: 0 hits in 1 commits\n" {
		t.Fatalf("run history: exit %d, stdout %q, stderr %q", code, so.String(), se.String())
	}

	// Tags and a term branch, added last.
	r.git("-c", "user.name=zork", "-c", "user.email=ok@example.invalid",
		"tag", "-a", "-m", "line one\nacme-real here", "v1")
	r.git("branch", "zork-branch")
	// Tag and ref hits appear even though REV limits the commits to one clean commit.
	code, out, _ = r.hist(c0)
	wantHist(t, code, out, 1,
		"leakcheck: tag refs/tags/v1 line 2: acme-real",
		"leakcheck: tag refs/tags/v1 tagger: zork",
		"leakcheck: ref refs/heads/zork-branch: zork",
		"leakcheck: history: 3 hits in 1 commits")
	// Whole history.
	code, out, _ = r.hist()
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" a.txt:2: zork",
		"leakcheck: path "+c3+" zork/dup.txt: zork",
		"leakcheck: message "+c4+" line 1: acme-real, zork",
		"leakcheck: identity "+c5+" author: zork",
		"leakcheck: identity "+c6+" committer: zork",
		"leakcheck: blob "+c7+" allow.txt:2: zork",
		"leakcheck: blob "+c7+" allow.txt:3: zork",
		"leakcheck: message "+c7+" line 1: zork",
		"leakcheck: tag refs/tags/v1 line 2: acme-real",
		"leakcheck: tag refs/tags/v1 tagger: zork",
		"leakcheck: ref refs/heads/zork-branch: zork",
		"leakcheck: history: 11 hits in 8 commits")
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
