package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestMain runs the tool when the binary is started as a hook (install copies the test binary, design D11);
// otherwise it clears the caller's GIT_* environment and git's global config so test repos are hermetic.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "pre-commit", "commit-msg", "install":
			os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr))
		}
	}
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			os.Unsetenv(k)
		}
	}
	empty, err := os.CreateTemp("", "leakcheck-gitconfig-*")
	if err != nil {
		panic(err)
	}
	empty.Close()
	os.Setenv("GIT_CONFIG_GLOBAL", empty.Name())
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	os.Remove(empty.Name())
	os.Exit(code)
}

type repo struct {
	t   *testing.T
	dir string
}

const testTerms = "# test terms\nzork\nacme-real\n"

// newRepo makes a git repo in a new temp dir with the given terms file (none when terms is "-").
func newRepo(t *testing.T, terms string) *repo {
	t.Helper()
	return newRepoAt(t, t.TempDir(), terms)
}

// skipSlow skips a git-spawning test in the fast tier (go test -short); see .ai/DEVELOPMENT.md "Running tests".
func skipSlow(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("slow tier: spawns git; runs without -short")
	}
}

func newRepoAt(t *testing.T, dir, terms string) *repo {
	t.Helper()
	skipSlow(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &repo{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	r.appendFile(".git/config", "[user]\n\tname = t\n\temail = t@example.invalid\n[commit]\n\tgpgsign = false\n[core]\n\tautocrlf = false\n")
	r.write(".git/info/exclude", ".ai/\n.worktrees/\n")
	if terms != "-" {
		r.write(termsRel, terms)
	}
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	out, err := git(r.dir, args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(out)
}

func (r *repo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) appendFile(rel, content string) {
	r.t.Helper()
	f, err := os.OpenFile(filepath.Join(r.dir, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commitAll(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--no-verify", "-m", msg)
}

// preCommit runs the pre-commit check in dir (the repo top when dir is "").
func (r *repo) preCommit(dir string) (int, string) {
	if dir == "" {
		dir = r.dir
	}
	var stderr bytes.Buffer
	code := runPreCommit(dir, &stderr)
	return code, stderr.String()
}

// hitLines returns the "leakcheck: ..." hit lines of stderr, without the hint, sorted.
func hitLines(stderr string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
		if l == "" || l == hint {
			continue
		}
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func wantHits(t *testing.T, code int, stderr string, want ...string) {
	t.Helper()
	sort.Strings(want)
	wantCode := 0
	if len(want) > 0 {
		wantCode = 1
		if !strings.Contains(stderr, hint) {
			t.Errorf("stderr lacks the hint line:\n%s", stderr)
		}
	}
	if got := hitLines(stderr); code != wantCode || !reflect.DeepEqual(got, want) {
		t.Fatalf("exit %d, hits %q; want exit %d, hits %q\nstderr:\n%s", code, got, wantCode, want, stderr)
	}
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line number " + string(rune('a'+i)) + " of the file\n")
	}
	return b.String()
}

func TestPreCommitBlocksAddedLine(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("a.txt", "one\n")
	r.write("b.txt", "keep\n-- y\nend\n")
	r.write("e.txt", numbered(10))
	r.commitAll("init")

	r.write("a.txt", "one\nZORK here\n")                                              // plain added line, upper case
	r.write("b.txt", "keep\n++ b/zork\nend\n")                                        // diff lines "--- y" / "+++ b/zork" inside a hunk
	r.write("c.txt", "x\nzork no newline")                                            // no final newline
	r.write("d.txt", "first\r\nzork crlf\r\n")                                        // CRLF content
	edited := strings.Replace(numbered(10), "line number h of the file", "zork h", 1) // line 7
	if err := os.Remove(filepath.Join(r.dir, "e.txt")); err != nil {
		t.Fatal(err)
	}
	r.write("f.txt", edited) // e.txt renamed to f.txt with line 7 edited
	r.git("add", "-A")
	if st := r.git("diff", "--cached", "--name-status", "-M"); !strings.Contains(st, "\te.txt\tf.txt") {
		t.Fatalf("expected the rename e.txt -> f.txt in the staged diff:\n%s", st)
	}
	code, stderr := r.preCommit("")
	wantHits(t, code, stderr,
		"leakcheck: a.txt:2: zork",
		"leakcheck: b.txt:2: zork",
		"leakcheck: c.txt:2: zork",
		"leakcheck: d.txt:2: zork",
		"leakcheck: f.txt:7: zork",
	)
}

func TestPreCommitIgnoresUnchangedAndRemovedLines(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("g.txt", "zork stays\nline\n")
	r.write("h.txt", "first\nzork goes\n")
	r.write("gone.txt", "zork in a deleted file\n")
	r.commitAll("init")
	r.write("g.txt", "zork stays\nline changed\n")
	r.write("h.txt", "first\n")
	os.Remove(filepath.Join(r.dir, "gone.txt"))
	r.git("add", "-A")
	code, stderr := r.preCommit("")
	wantHits(t, code, stderr)
}

func TestPreCommitFirstCommit(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("a.txt", "fine\nacme-real host\n")
	r.git("add", "a.txt")
	code, stderr := r.preCommit("")
	wantHits(t, code, stderr, "leakcheck: a.txt:2: acme-real")
}

func TestMatch(t *testing.T) {
	cases := []struct {
		line, term string
		want       bool
	}{
		{"AB-1", "ab", true},
		{"config.ab.yaml", "ab", true},
		{"abc", "ab", false},
		{"cab", "ab", false},
		{"cab ab", "ab", true},
		{"x aXb.example y", "a.b.example", false},
		{"x a.b.example y", "a.b.example", true},
		{"https://kibana.a.b.example:5601/", "a.b.example", true},
		{"-ÄB-", "äb", true},
		{"-İB-", strings.ToLower("İB"), true},
		{"x-ab.", "-ab", true},
		{"x-abq", "-ab", false},
		{"under_ab", "ab", false},
		{strings.Repeat("x ", 100000) + "ab", "ab", true},
	}
	for _, c := range cases {
		if got := len(matchTerms(c.line, []string{c.term})) == 1; got != c.want {
			l := c.line
			if len(l) > 40 {
				l = l[:20] + "..." + l[len(l)-20:]
			}
			t.Errorf("match(%q, %q) = %v, want %v", l, c.term, got, c.want)
		}
	}
	if got := matchTerms("abc ab", []string{"ab", "abc"}); !reflect.DeepEqual(got, []string{"ab", "abc"}) {
		t.Errorf("overlapping terms: got %q", got)
	}
}

func TestLoadTerms(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(termsRel, "\xef\xbb\xbf# comment\r\nZork\r\n   \r\nzork\n  Alpha Beta  \n")
	write("config.one.yaml", "sites:\n  base_url: https://user:pw@Kibana.Internal.example:5601/app\n"+
		"  other: https://api.github.com/x\n  local: http://localhost:8080 http://127.0.0.1/x http://[::1]:80/\n"+
		"  v6: https://[fe80::1]/\n  bad: ': : [ https:// %zz\n")
	write("config.one.yaml.20260101-000000.bak", "base_url: https://bak.example\n")
	write("operations.one.yaml", "url: https://ops.example\n")
	got, err := loadTerms(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha beta", "fe80::1", "kibana.internal.example", "zork"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("loadTerms = %q, want %q", got, want)
	}
}

func TestPreCommitPaths(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("src.txt", numbered(10))
	r.write("old.txt", "plain\n")
	r.write("zork-old.txt", "hello\n")
	r.write("zork-gone.txt", "bye\n")
	r.write("leaky.txt", "zork leaks here\n"+numbered(5)) // an existing leak in the tree
	r.commitAll("init")

	r.write("new zork.txt", "zork inside\n")                    // added, path with a space
	r.git("mv", "old.txt", "zork-renamed.txt")                  // pure rename
	r.write("zork-old.txt", "hello\nmore\n")                    // modified, path already had the term
	os.Remove(filepath.Join(r.dir, "zork-gone.txt"))            // deleted
	r.write("zork-copy.txt", numbered(10))                      // copy of src.txt into a term path
	r.write("src.txt", numbered(10)+"tail\n")                   // source modified, as git -C would need
	r.write("copy.txt", "zork leaks here\n"+numbered(5))        // verbatim copy of the existing leak ...
	r.write("leaky.txt", "zork leaks here\n"+numbered(5)+"x\n") // ... whose source is modified too
	r.git("add", "-A")
	code, stderr := r.preCommit("")
	// No copy detection (design D6, review round 1): a copied file is an added file, so both its path and
	// every line it brings in are checked.
	wantHits(t, code, stderr,
		"leakcheck: new zork.txt: path contains zork",
		"leakcheck: new zork.txt:1: zork",
		"leakcheck: zork-renamed.txt: path contains zork",
		"leakcheck: zork-copy.txt: path contains zork",
		"leakcheck: copy.txt:1: zork",
	)
}

func TestPreCommitBinary(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("bin.dat", "zork\x00\x01\x02zork\n")
	r.git("add", "bin.dat")
	code, stderr := r.preCommit("")
	wantHits(t, code, stderr)
}

func TestCommitMsg(t *testing.T) {
	r := newRepo(t, testTerms)
	check := func(msg string, want ...string) {
		t.Helper()
		r.write("msg.txt", msg)
		var stderr bytes.Buffer
		code := runCommitMsg(r.dir, "msg.txt", &stderr)
		wantHits(t, code, stderr.String(), want...)
	}
	check("Fix zork thing\r\n\r\n# zork in a comment\nbody ok\n"+
		"# ------------------------ >8 ------------------------\nzork below the scissors\n",
		"leakcheck: commit message:1: zork")
	check("")
	check("# only a comment naming zork\n")
	check("Merge branch 'agent/zork-x'\n", "leakcheck: commit message:1: zork")
	check("Revert \"x\"\n\nbody acme-real\n", "leakcheck: commit message:3: acme-real")
}

func TestPreCommitSkipsMerge(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("sub/a.txt", "a\n")
	r.commitAll("init")
	head := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	r.write("sub/a.txt", "a\nzork\n")
	r.git("add", "-A")
	if code, _ := r.preCommit(filepath.Join(r.dir, "sub")); code != 1 {
		t.Fatalf("without MERGE_HEAD: exit %d, want 1", code)
	}
	r.write(".git/MERGE_HEAD", head+"\n")
	code, stderr := r.preCommit(filepath.Join(r.dir, "sub"))
	wantHits(t, code, stderr)
}

func TestAllowMarker(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("a.txt", "zork and acme-real // leakcheck:allow\n")
	r.git("add", "a.txt")
	code, stderr := r.preCommit("")
	wantHits(t, code, stderr)

	r.write("msg.txt", "subject zork acme-real leakcheck:allow\n")
	var buf bytes.Buffer
	code = runCommitMsg(r.dir, "msg.txt", &buf)
	wantHits(t, code, buf.String())
}

func TestWorktreeUsesMainCheckout(t *testing.T) {
	r := newRepo(t, testTerms)
	r.write("a.txt", "a\n")
	r.commitAll("init")
	r.git("worktree", "add", "-q", ".worktrees/w", "-b", "w")
	wt := filepath.Join(r.dir, ".worktrees", "w")
	if _, err := os.Stat(filepath.Join(wt, filepath.FromSlash(termsRel))); err == nil {
		t.Fatal("the worktree must not have its own terms file")
	}
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "sub", "wt.txt"), []byte("zork in the worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := git(wt, "add", "sub/wt.txt"); err != nil {
		t.Fatal(err)
	}
	code, stderr := r.preCommit(filepath.Join(wt, "sub"))
	wantHits(t, code, stderr, "leakcheck: sub/wt.txt:1: zork")
	// The main checkout's own index is clean.
	code, stderr = r.preCommit("")
	wantHits(t, code, stderr)
}

func TestFailClosed(t *testing.T) {
	r := newRepo(t, "-")
	termsPath := filepath.Join(r.dir, filepath.FromSlash(termsRel))
	var buf bytes.Buffer
	if code := runPreCommit(r.dir, &buf); code != 1 || !strings.Contains(buf.String(), termsPath) {
		t.Errorf("missing terms, pre-commit: exit %d, stderr %q; want 1 naming %s", code, buf.String(), termsPath)
	}
	buf.Reset()
	r.write("msg.txt", "fine\n")
	if code := runCommitMsg(r.dir, "msg.txt", &buf); code != 1 || !strings.Contains(buf.String(), termsPath) {
		t.Errorf("missing terms, commit-msg: exit %d, stderr %q", code, buf.String())
	}

	r.write(termsRel, "")
	if code, stderr := r.preCommit(""); code != 0 {
		t.Errorf("empty terms file: exit %d, stderr %q; want 0", code, stderr)
	}

	os.Remove(termsPath)
	if err := os.Mkdir(termsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, stderr := r.preCommit(""); code != 1 || !strings.Contains(stderr, "terms file") {
		t.Errorf("terms file is a directory: exit %d, stderr %q; want 1", code, stderr)
	}
	os.Remove(termsPath)
	r.write(termsRel, "zork\n")

	if err := os.Mkdir(filepath.Join(r.dir, "config.x.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, stderr := r.preCommit(""); code != 1 || !strings.Contains(stderr, "config") {
		t.Errorf("unreadable config: exit %d, stderr %q; want 1", code, stderr)
	}

	buf.Reset()
	if code := runPreCommit(t.TempDir(), &buf); code != 1 || !strings.Contains(buf.String(), "leakcheck: error: git") {
		t.Errorf("not a repo: exit %d, stderr %q; want 1 with the git error", code, buf.String())
	}

	for _, args := range [][]string{nil, {"bogus"}, {"commit-msg"}} {
		buf.Reset()
		if code := run(args, r.dir, &buf, &buf); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
}

func TestInstall(t *testing.T) {
	r := newRepoAt(t, filepath.Join(t.TempDir(), "with space"), testTerms)
	hooks := filepath.Join(r.dir, ".git", "hooks")
	var out, errb bytes.Buffer
	if code := runInstall(r.dir, &out, &errb); code != 0 {
		t.Fatalf("install: exit %d, stderr %s", code, errb.String())
	}
	exe := filepath.ToSlash(filepath.Join(hooks, "leakcheck.exe"))
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
	for name, cmd := range map[string]string{"pre-commit.local": `pre-commit "$@"`, "commit-msg.local": `commit-msg "$1"`} {
		data, err := os.ReadFile(filepath.Join(hooks, name))
		if err != nil {
			t.Fatal(err)
		}
		want := "#!/bin/sh\n" + hookMarker + "\nexec \"" + exe + "\" " + cmd + "\n"
		if string(data) != want {
			t.Errorf("%s =\n%q\nwant\n%q", name, data, want)
		}
	}
	if code := runInstall(r.dir, &out, &errb); code != 0 {
		t.Fatalf("second install: exit %d, stderr %s", code, errb.String())
	}

	foreign := filepath.Join(hooks, "commit-msg.local")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := runInstall(r.dir, &out, &errb); code != 1 || !strings.Contains(errb.String(), "foreign hook") {
		t.Errorf("foreign .local: exit %d, stderr %q; want 1", code, errb.String())
	}
	if data, _ := os.ReadFile(foreign); string(data) != "#!/bin/sh\necho mine\n" {
		t.Errorf("foreign hook was overwritten: %q", data)
	}

	r.git("config", "core.hooksPath", "elsewhere")
	errb.Reset()
	if code := runInstall(r.dir, &out, &errb); code != 1 || !strings.Contains(errb.String(), "core.hooksPath") {
		t.Errorf("core.hooksPath set: exit %d, stderr %q; want 1", code, errb.String())
	}
}

func TestHookChainEndToEnd(t *testing.T) {
	common, err := gitCommonDir(".")
	if err != nil {
		t.Skipf("not inside this repo's checkout: %v", err)
	}
	managed := map[string][]byte{}
	for _, name := range []string{"pre-commit", "commit-msg"} {
		data, err := os.ReadFile(filepath.Join(common, "hooks", name))
		if err != nil || !bytes.Contains(data, []byte("todo-hook")) {
			t.Skipf("todo-managed %s hook not installed in this repo", name)
		}
		managed[name] = data
	}
	r := newRepo(t, testTerms)
	for name, data := range managed {
		if err := os.WriteFile(filepath.Join(r.dir, ".git", "hooks", name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	if code := runInstall(r.dir, &out, &errb); code != 0 {
		t.Fatalf("install: exit %d, stderr %s", code, errb.String())
	}
	commit := func(msg string) (string, error) {
		cmd := exec.Command("git", "commit", "-q", "-m", msg)
		cmd.Dir = r.dir
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	r.write("a.txt", "a zork line\n")
	r.git("add", "a.txt")
	if outp, err := commit("add a"); err == nil || !strings.Contains(outp, "a.txt:1: zork") {
		t.Fatalf("commit with a term: err %v, output %s; want refused naming a.txt:1", err, outp)
	}
	r.write("a.txt", "a clean line\n")
	r.git("add", "a.txt")
	if outp, err := commit("add a"); err != nil {
		t.Fatalf("clean commit refused: %v\n%s", err, outp)
	}
	r.write("b.txt", "b\n")
	r.git("add", "b.txt")
	if outp, err := commit("mention zork"); err == nil || !strings.Contains(outp, "commit message:1: zork") {
		t.Fatalf("message with a term: err %v, output %s; want refused", err, outp)
	}
}

func TestRepoWiring(t *testing.T) {
	skipSlow(t)
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := git(strings.TrimSpace(string(top)), "check-ignore", "-q", termsRel); err != nil {
		t.Errorf("%s is not gitignored: %v", termsRel, err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{termsRel, allowMarker, "go run ./cmd/tools/leakcheck install"} {
		if !bytes.Contains(readme, []byte(s)) {
			t.Errorf("README.md does not mention %q", s)
		}
	}
}
