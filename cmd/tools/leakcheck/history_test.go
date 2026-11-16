package main

import (
	"bytes"
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

func (r *repo) commitEmpty(msg string, extra ...string) {
	r.t.Helper()
	args := append(append([]string{}, extra...), "commit", "-q", "--no-verify", "--allow-empty", "-m", msg)
	r.git(args...)
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

func TestHistoryRemovedBlob(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("a.txt", "one\nuse zork here\n")
	r.commitAll("add")
	c1 := r.head()
	r.git("rm", "-q", "a.txt")
	r.commitAll("remove")
	code, out, _ := r.hist()
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" a.txt:2: zork",
		"leakcheck: history: 1 hits in 2 commits")
}

func TestHistoryLaterLeakingPath(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("x/clean.txt", "same content\n")
	r.commitAll("add")
	r.write("zork/dup.txt", "same content\n")
	r.commitAll("copy")
	c2 := r.head()
	code, out, _ := r.hist()
	wantHist(t, code, out, 1,
		"leakcheck: path "+c2+" zork/dup.txt: zork",
		"leakcheck: history: 1 hits in 2 commits")
}

func TestHistorySurfaces(t *testing.T) {
	r := newRepo(t, testTerms)
	r.commitEmpty("clean start")
	r.commitEmpty("zork and acme-real\nfine line")
	c1 := r.head()
	r.git("commit", "-q", "--no-verify", "--allow-empty", "-m", "second", "--author=Fine <zork@example.invalid>")
	c2 := r.head()
	r.git("-c", "user.name=zork", "-c", "user.email=ok@example.invalid",
		"commit", "-q", "--no-verify", "--allow-empty", "-m", "third", "--author=Fine <fine@example.invalid>")
	c3 := r.head()
	r.git("-c", "user.name=zork", "-c", "user.email=ok@example.invalid",
		"tag", "-a", "-m", "line one\nacme-real here", "v1")
	r.git("branch", "zork-branch")
	code, out, _ := r.hist()
	wantHist(t, code, out, 1,
		"leakcheck: message "+c1+" line 1: acme-real, zork",
		"leakcheck: identity "+c2+" author: zork",
		"leakcheck: identity "+c3+" committer: zork",
		"leakcheck: tag refs/tags/v1 line 2: acme-real",
		"leakcheck: tag refs/tags/v1 tagger: zork",
		"leakcheck: ref refs/heads/zork-branch: zork",
		"leakcheck: history: 6 hits in 4 commits")
}

func TestHistoryAllow(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("a.txt", "zork # leakcheck:allow\r\n  zork secret  \r\nzork bad\n")
	r.commitAll("zork msg\nzork x leakcheck:allow")
	c1 := r.head()
	r.write("allow.txt", "\xef\xbb\xbf# comment\n\n zork secret \r\nzork msg\r\n")
	code, out, _ := r.hist()
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" a.txt:2: zork",
		"leakcheck: blob "+c1+" a.txt:3: zork",
		"leakcheck: message "+c1+" line 1: zork",
		"leakcheck: history: 3 hits in 1 commits")
	code, out, _ = r.hist("-allow", filepath.Join(r.dir, "allow.txt"))
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" a.txt:3: zork",
		"leakcheck: history: 1 hits in 1 commits")
}

func TestHistoryRevLimits(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("clean.txt", "fine\n")
	r.commitAll("start")
	c0 := r.head()
	r.write("zork.txt", "zork\n")
	r.git("add", "-A")
	r.git("commit", "-q", "--no-verify", "-m", "zork msg", "--author=Z <zork@example.invalid>")
	c1 := r.head()
	code, out, _ := r.hist(c0)
	wantHist(t, code, out, 0, "leakcheck: history: 0 hits in 1 commits")
	code, out, _ = r.hist(c1)
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" zork.txt:1: zork",
		"leakcheck: path "+c1+" zork.txt: zork",
		"leakcheck: message "+c1+" line 1: zork",
		"leakcheck: identity "+c1+" author: zork",
		"leakcheck: history: 4 hits in 2 commits")
}

func TestHistoryRoot(t *testing.T) {
	termsRepo := newRepo(t, testTerms)
	r := newRepo(t, "-")
	r.write("a.txt", "zork\n")
	r.commitAll("add")
	c1 := r.head()
	if code, _, stderr := r.hist(); code != 1 || !strings.Contains(stderr, "leakcheck: error:") {
		t.Fatalf("missing terms file: exit %d, stderr %q", code, stderr)
	}
	code, out, _ := r.hist("-root", termsRepo.dir)
	wantHist(t, code, out, 1,
		"leakcheck: blob "+c1+" a.txt:1: zork",
		"leakcheck: history: 1 hits in 1 commits")
}

func TestHistoryCleanAndUsage(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("a.txt", "fine\n")
	r.commitAll("add")
	code, out, _ := r.hist()
	if code != 0 || out != "leakcheck: history: 0 hits in 1 commits\n" {
		t.Fatalf("clean repo: exit %d, stdout %q", code, out)
	}
	if code, _, stderr := r.hist("-allow", filepath.Join(r.dir, "nope.txt")); code != 1 || !strings.Contains(stderr, "leakcheck: error:") {
		t.Fatalf("missing -allow file: exit %d, stderr %q", code, stderr)
	}
	if code, _, _ := r.hist("-bogus"); code != 2 {
		t.Fatalf("bad flag: exit %d, want 2", code)
	}
	if code, _, _ := r.hist("HEAD", "-x"); code != 2 {
		t.Fatalf("REV starting with -: exit %d, want 2", code)
	}
	if code, _, stderr := r.hist("no-such-rev"); code != 1 || !strings.Contains(stderr, "leakcheck: error:") {
		t.Fatalf("bad REV: exit %d, stderr %q", code, stderr)
	}
}
