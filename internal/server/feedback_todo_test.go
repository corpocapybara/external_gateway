package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/external_gateway/internal/config"
)

// ---- helpers ----

type runCall struct {
	argv, env []string
	dir       string
}

type runRecorder struct {
	mu    sync.Mutex
	calls []runCall
}

func (r *runRecorder) argvs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, strings.Join(c.argv, " "))
	}
	return out
}

// record replaces todoRun with fn for the test, recording every call.
func record(t *testing.T, fn func(ctx context.Context, argv, env []string, dir string) ([]byte, error)) *runRecorder {
	t.Helper()
	rec := &runRecorder{}
	old := todoRun
	todoRun = func(ctx context.Context, argv, env []string, dir string) ([]byte, error) {
		rec.mu.Lock()
		rec.calls = append(rec.calls, runCall{slices.Clone(argv), slices.Clone(env), dir})
		rec.mu.Unlock()
		return fn(ctx, argv, env, dir)
	}
	t.Cleanup(func() { todoRun = old })
	return rec
}

func useRunner(t *testing.T, fn func(argv []string) ([]byte, error)) *runRecorder {
	return record(t, func(_ context.Context, argv, _ []string, _ string) ([]byte, error) { return fn(argv) })
}

// adoptedRunner answers the adoption check with "adopted" and passes other calls to fn.
func adoptedRunner(fn func(argv []string) ([]byte, error)) func(argv []string) ([]byte, error) {
	return func(argv []string) ([]byte, error) {
		switch strings.Join(argv, " ") {
		case "git rev-parse --verify -q HEAD":
			return []byte("abc\n"), nil
		case "git ls-tree HEAD -- todo/_config.yaml":
			return []byte("100644 blob abc\ttodo/_config.yaml\n"), nil
		}
		return fn(argv)
	}
}

const priorityTemplate = "# Priority list\n\nEvery open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file\n" +
	"in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the\n" +
	"same commit as the todo change (todo rules, section \"Without the tool\").\n\n" +
	"## P0\n\nnone\n\n## P1\n\nnone\n\n## P2\n\nnone\n\n## P3\n\nnone\n"

const todoConfigYAML = "id_prefix: T\npriorities: [P0, P1, P2, P3]\nbuckets:\n  - {name: feedback, owns: \"feedback\"}\n" +
	"review_lanes: [correctness, tests, scope, design]\n"

// fakeAdoptedRepo is a directory with the todo config and PRIORITY.md, without git.
func fakeAdoptedRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "todo", "_config.yaml"), todoConfigYAML)
	writeFile(t, filepath.Join(repo, "todo", "PRIORITY.md"), priorityTemplate)
	return repo
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0644); err != nil {
		t.Fatal(err)
	}
}

// toolConfig files into repo with an existing (fake) tool file.
func toolConfig(t *testing.T, repo string) config.FeedbackConfig {
	t.Helper()
	dir := t.TempDir()
	tool := filepath.Join(dir, "todo.py")
	writeFile(t, tool, "")
	return config.FeedbackConfig{TodoRepo: repo, TodoTool: tool, TodoPython: filepath.Join(dir, "py", "python.exe"),
		TodoBucket: "feedback"}
}

// handConfig files into repo with no tool, so the built-in writer runs.
func handConfig(t *testing.T, repo string) config.FeedbackConfig {
	fbc := toolConfig(t, repo)
	fbc.TodoTool = ""
	return fbc
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_COMMITTER_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %v\n%s", args, err, out)
	}
	return string(out), nil
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Template repos are built once per test binary (git process starts are slow on Windows) and
// copied per test: "unborn" (git init), "plain" (one commit, no todo config) and "adopted"
// (plus the todo config and PRIORITY.md committed, then the commits `todo: done T0007` and
// `feat: T0099 ...`).
var (
	templatesOnce sync.Once
	templatesDir  string
	templatesErr  error
)

func buildTemplates() (string, error) {
	root, err := os.MkdirTemp("", "egw-feedback-templates-")
	if err != nil {
		return "", err
	}
	work := filepath.Join(root, "work")
	steps := []struct {
		files    map[string]string
		gitCalls [][]string
		snapshot string
	}{
		{nil, [][]string{{"init", "-q"}, {"config", "core.autocrlf", "false"}}, "unborn"},
		{map[string]string{"README": "x"}, [][]string{{"add", "README"}, {"commit", "-q", "-m", "init"}}, "plain"},
		{map[string]string{"todo/_config.yaml": todoConfigYAML, "todo/PRIORITY.md": priorityTemplate}, [][]string{
			{"add", "todo"}, {"commit", "-q", "-m", "todo: adopt"},
			{"commit", "-q", "--allow-empty", "-m", "todo: done T0007"},
			{"commit", "-q", "--allow-empty", "-m", "feat: T0099 is not a todo subject"},
		}, "adopted"},
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		return "", err
	}
	for _, st := range steps {
		for rel, s := range st.files {
			p := filepath.Join(work, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(s), 0644); err != nil {
				return "", err
			}
		}
		for _, args := range st.gitCalls {
			if _, err := git(work, args...); err != nil {
				return "", err
			}
		}
		if err := os.CopyFS(filepath.Join(root, st.snapshot), os.DirFS(work)); err != nil {
			return "", err
		}
	}
	return root, nil
}

func TestMain(m *testing.M) {
	code := m.Run()
	if templatesDir != "" {
		os.RemoveAll(templatesDir)
	}
	os.Exit(code)
}

// gitRepo is a fresh copy of the named template repo ("unborn", "plain" or "adopted").
func gitRepo(t *testing.T, kind string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	templatesOnce.Do(func() { templatesDir, templatesErr = buildTemplates() })
	if templatesErr != nil {
		t.Fatal(templatesErr)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(repo, os.DirFS(filepath.Join(templatesDir, kind))); err != nil {
		t.Fatal(err)
	}
	return repo
}

func realGit(t *testing.T) *runRecorder { return record(t, runTodoCmd) }

func noTodoFiles(t *testing.T, repo string) {
	t.Helper()
	if m, _ := filepath.Glob(filepath.Join(repo, "todo", "T*.md")); len(m) != 0 {
		t.Fatalf("todo files written: %v", m)
	}
}

func noLegacy(t *testing.T, repo string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, legacyName)); err == nil {
		t.Fatal("legacy list written")
	}
}

func logKept(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(readFile(t, "feedback.log"), "| todo | agent=tester | "+text) {
		t.Fatal("feedback.log not written")
	}
}

// wantTodoEnv asserts the seven git variables and the PATH prefix on every call.
func wantTodoEnv(t *testing.T, rec *runRecorder, fbc config.FeedbackConfig) {
	t.Helper()
	want := []string{
		"GIT_AUTHOR_NAME=egw-feedback", "GIT_COMMITTER_NAME=egw-feedback",
		"GIT_AUTHOR_EMAIL=egw-feedback@localhost", "GIT_COMMITTER_EMAIL=egw-feedback@localhost",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=" + filepath.ToSlash(fbc.TodoRepo),
	}
	path := "PATH=" + filepath.Dir(fbc.TodoPython) + ";" + os.Getenv("PATH")
	if len(rec.calls) == 0 {
		t.Fatal("no calls")
	}
	for _, c := range rec.calls {
		for _, w := range want {
			if !slices.Contains(c.env, w) {
				t.Errorf("%q: env lacks %s", c.argv, w)
			}
		}
		if last := c.env[len(c.env)-1]; last != path {
			t.Errorf("%q: last env %q, want %q", c.argv, last, path)
		}
		if c.dir != fbc.TodoRepo {
			t.Errorf("%q: dir %q", c.argv, c.dir)
		}
	}
}

// ---- tool path ----

func TestFeedbackTodoViaToolArgvEnv(t *testing.T) {
	repo := fakeAdoptedRepo(t)
	fbc := toolConfig(t, repo)
	rec := useRunner(t, adoptedRunner(func(argv []string) ([]byte, error) {
		if argv[5] == "new" {
			return []byte("T0042 created: fix -x (pending)\nwarning\n"), nil
		}
		return []byte("1 commit (abc)\n"), nil
	}))
	s := todoServer(t, fbc)
	wantResp(t, postFeedback(s, `{"type":"todo","text":"fix  -x"}`), 200, `{"status":"added","todo":"T0042"}`)
	tool := []string{fbc.TodoPython, "-I", "-X", "utf8", fbc.TodoTool}
	want := []string{
		"git rev-parse --verify -q HEAD",
		"git ls-tree HEAD -- todo/_config.yaml",
		strings.Join(append(tool, "new", "--repo", repo, "--by", "agent", "--title=fix -x",
			"--body=fix -x\n\nFiled via /feedback by tester on "+todayUTC()+".",
			"--bucket=feedback", "--priority=P2", "--size=S"), " "),
		strings.Join(append(tool, "commit", "--repo", repo, "--by", "agent"), " "),
	}
	if got := rec.argvs(); !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	wantTodoEnv(t, rec, fbc)
	noLegacy(t, repo)
	logKept(t, "fix  -x")
}

func TestFeedbackTodoToolFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		newOut    string
		newErr    error
		commitErr error
		calls     int
	}{
		"new fails":                {newErr: &cmdExitError{code: 2, stderr: "boom"}, calls: 3},
		"commit fails after new":   {newOut: "T0001 created: x (pending)\n", commitErr: &cmdExitError{code: 1}, calls: 4},
		"new output has no id":     {newOut: "something else\n", calls: 4},
		"new id on a later line":   {newOut: "note\nT0001 created: x (pending)\n", calls: 4},
		"new id with other prefix": {newOut: "X0001 created: x (pending)\n", calls: 4},
	} {
		t.Run(name, func(t *testing.T) {
			repo := fakeAdoptedRepo(t)
			rec := useRunner(t, adoptedRunner(func(argv []string) ([]byte, error) {
				if argv[5] == "new" {
					return []byte(tc.newOut), tc.newErr
				}
				return nil, tc.commitErr
			}))
			s := todoServer(t, toolConfig(t, repo))
			if w := postFeedback(s, `{"type":"todo","text":"x"}`); w.Code != 500 {
				t.Fatalf("code %d %q", w.Code, w.Body.String())
			}
			if got := rec.argvs(); len(got) != tc.calls || (tc.calls == 4 && !strings.Contains(got[3], " commit --repo ")) {
				t.Fatalf("calls %q", got)
			}
			logKept(t, "x")
			noTodoFiles(t, repo)
			noLegacy(t, repo)
		})
	}
}

func TestFeedbackTodoTimeouts(t *testing.T) {
	old := todoToolTimeout
	todoToolTimeout = 20 * time.Millisecond
	t.Cleanup(func() { todoToolTimeout = old })
	block := func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	for _, blocked := range []string{"rev-parse", "ls-tree", "new"} {
		t.Run(blocked, func(t *testing.T) {
			repo := fakeAdoptedRepo(t)
			rec := record(t, func(ctx context.Context, argv, _ []string, _ string) ([]byte, error) {
				if slices.Contains(argv, blocked) {
					return block(ctx)
				}
				return adoptedRunner(func([]string) ([]byte, error) { return nil, nil })(argv)
			})
			s := todoServer(t, toolConfig(t, repo))
			if w := postFeedback(s, `{"type":"todo","text":"x"}`); w.Code != 500 {
				t.Fatalf("code %d", w.Code)
			}
			if got := rec.argvs(); !strings.Contains(got[len(got)-1], blocked) {
				t.Fatalf("ran after the timeout: %q", got)
			}
			logKept(t, "x")
			noTodoFiles(t, repo)
			noLegacy(t, repo)
		})
	}
}

// ---- adoption check ----

func TestFeedbackTodoAdoptionCheckFailures500(t *testing.T) {
	for name, fail := range map[string]func(argv []string) ([]byte, error){
		"rev-parse 128": func(argv []string) ([]byte, error) {
			return nil, &cmdExitError{code: 128, stderr: "detected dubious ownership"}
		},
		"ls-tree 128": func(argv []string) ([]byte, error) {
			if argv[1] == "ls-tree" {
				return nil, &cmdExitError{code: 128}
			}
			return nil, nil
		},
		"git does not start": func(argv []string) ([]byte, error) { return nil, exec.ErrNotFound },
	} {
		t.Run(name, func(t *testing.T) {
			repo := fakeAdoptedRepo(t)
			useRunner(t, fail)
			s := todoServer(t, toolConfig(t, repo))
			if w := postFeedback(s, `{"type":"todo","text":"x"}`); w.Code != 500 {
				t.Fatalf("code %d", w.Code)
			}
			logKept(t, "x")
			noLegacy(t, repo)
			noTodoFiles(t, repo)
		})
	}
}

// Not adopted (unborn HEAD; config only in the working tree and index): legacy append and
// only the check calls. After the adopting commit the next request files a todo.
func TestFeedbackTodoNotAdoptedThenAdopted(t *testing.T) {
	unborn := gitRepo(t, "unborn")
	rec := realGit(t)
	s := todoServer(t, handConfig(t, unborn))
	wantResp(t, postFeedback(s, `{"type":"todo","text":"a"}`), 200, `{"status":"added"}`)
	if got := rec.argvs(); !slices.Equal(got, []string{"git rev-parse --verify -q HEAD"}) {
		t.Fatalf("unborn calls %q", got)
	}
	if got := readFile(t, filepath.Join(unborn, legacyName)); !strings.Contains(got, item("a")) {
		t.Fatalf("legacy %q", got)
	}

	repo := gitRepo(t, "plain")
	writeFile(t, filepath.Join(repo, "todo", "_config.yaml"), todoConfigYAML)
	writeFile(t, filepath.Join(repo, "todo", "PRIORITY.md"), priorityTemplate)
	fbc := handConfig(t, repo)
	s = todoServer(t, fbc)
	for _, stage := range []string{"working tree", "index"} {
		if stage == "index" {
			gitT(t, repo, "add", "todo")
		}
		rec.calls = nil
		wantResp(t, postFeedback(s, `{"type":"todo","text":"`+stage+`"}`), 200, `{"status":"added"}`)
		if got := rec.argvs(); !slices.Equal(got, []string{"git rev-parse --verify -q HEAD", "git ls-tree HEAD -- todo/_config.yaml"}) {
			t.Fatalf("%s calls %q", stage, got)
		}
		noTodoFiles(t, repo)
	}
	wantResp(t, postFeedback(s, `{"type":"todo","text":"index"}`), 200, `{"status":"duplicate"}`)
	legacy := readFile(t, filepath.Join(repo, legacyName))

	gitT(t, repo, "commit", "-q", "-m", "todo: adopt")
	wantResp(t, postFeedback(s, `{"type":"todo","text":"after"}`), 200, `{"status":"added","todo":"T0001"}`)
	if readFile(t, filepath.Join(repo, legacyName)) != legacy {
		t.Fatal("legacy list written after adoption")
	}
	wantTodoEnv(t, rec, fbc)
}

// ---- built-in writer ----

// Real git: the file, its PRIORITY.md line, a pathspec-limited [hand] commit by egw-feedback,
// an id one past a `todo:` commit subject, other staged/dirty paths untouched, `todo check` passes.
func TestFeedbackTodoBuiltInWriter(t *testing.T) {
	repo := gitRepo(t, "adopted") // its history has `todo: done T0007` and `feat: T0099 ...`
	writeFile(t, filepath.Join(repo, "staged.txt"), "s")
	gitT(t, repo, "add", "staged.txt")
	writeFile(t, filepath.Join(repo, "dirty.txt"), "d")
	fbc := handConfig(t, repo)
	rec := realGit(t)
	s := todoServer(t, fbc)
	text := `fix:  the "thing" \ now`
	norm := normalizeTodoText(text)
	body, _ := json.Marshal(map[string]string{"type": "todo", "text": text})
	wantResp(t, postFeedback(s, string(body)), 200, `{"status":"added","todo":"T0008"}`)

	doc := readFile(t, filepath.Join(repo, "todo", "T0008.md"))
	for _, w := range []string{"---\nid: T0008\ntitle: \"fix: the \\\"thing\\\" \\\\ now\"\nstate: draft\npriority: P2\nbucket: feedback\nsize: S\nupdated: {by: agent, at: ",
		"\nhistory:\n  - {at: ", ", by: agent, to: draft}\n---\n" + norm + "\n\nFiled via /feedback by tester on " + todayUTC() + ".\n"} {
		if !strings.Contains(doc, w) {
			t.Fatalf("todo file lacks %q:\n%s", w, doc)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(repo, "todo", "PRIORITY.md")), "## P2\n\n- T0008 · draft · feedback · S — "+norm+"\n\n## P3") {
		t.Fatal("PRIORITY.md line missing")
	}
	if got, want := gitT(t, repo, "log", "-1", "--name-only", "--format=%an <%ae>%n%s%n%b"),
		"egw-feedback <egw-feedback@localhost>\ntodo: T0008 created: "+norm+" [hand]\nChanged-By: agent\n\n\ntodo/PRIORITY.md\ntodo/T0008.md\n"; got != want {
		t.Fatalf("commit\n%q\nwant\n%q", got, want)
	}
	if got := gitT(t, repo, "status", "--porcelain"); got != "A  staged.txt\n?? dirty.txt\n" {
		t.Fatalf("status %q", got)
	}
	noNoVerify(t, rec)
	wantTodoEnv(t, rec, fbc)
	noLegacy(t, repo)
	todoCheck(t, repo)
}

func noNoVerify(t *testing.T, rec *runRecorder) {
	t.Helper()
	for _, a := range rec.argvs() {
		if strings.Contains(a, "--no-verify") {
			t.Fatalf("--no-verify in %q", a)
		}
	}
}

// The tool is not available (no file, or python does not start): the built-in writer files it.
func TestFeedbackTodoToolUnavailableFallsBack(t *testing.T) {
	for _, mode := range []string{"tool file missing", "python does not start"} {
		t.Run(mode, func(t *testing.T) {
			repo := fakeAdoptedRepo(t)
			fbc := toolConfig(t, repo)
			if mode == "tool file missing" {
				fbc.TodoTool = filepath.Join(t.TempDir(), "todo.py")
			}
			rec := useRunner(t, adoptedRunner(func(argv []string) ([]byte, error) {
				if argv[0] != "git" {
					return nil, exec.ErrNotFound
				}
				return nil, nil
			}))
			s := todoServer(t, fbc)
			wantResp(t, postFeedback(s, `{"type":"todo","text":"x"}`), 200, `{"status":"added","todo":"T0001"}`)
			var got []string
			for _, a := range rec.argvs()[2:] {
				got = append(got, strings.SplitN(a, " ", 3)[1])
			}
			want := []string{"log", "add", "commit"}
			if mode == "python does not start" {
				want = []string{"-I", "log", "add", "commit"}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("calls %q", rec.argvs())
			}
			readFile(t, filepath.Join(repo, "todo", "T0001.md"))
		})
	}
}

// todoCheck runs the ToDo tool's validator on repo, skipped when the tool or python is absent.
func todoCheck(t *testing.T, repo string) {
	t.Helper()
	tool := os.Getenv("TODO_TOOL")
	if tool == "" {
		tool = "C:/dev/_tools/todo/todo.py"
	}
	py, err := exec.LookPath("python")
	if _, serr := os.Stat(tool); err != nil || serr != nil {
		t.Logf("todo check skipped: tool %s or python not found", tool)
		return
	}
	out, err := exec.Command(py, "-I", "-X", "utf8", tool, "check", "--repo", repo).CombinedOutput()
	if err != nil {
		t.Fatalf("todo check: %v\n%s", err, out)
	}
}

// A failing commit (index.lock held, then a refusing hook) leaves no new file and PRIORITY.md as it was.
func TestFeedbackTodoBuiltInWriterCommitFailureRestores(t *testing.T) {
	repo := gitRepo(t, "adopted")
	prio := readFile(t, filepath.Join(repo, "todo", "PRIORITY.md"))
	rec := realGit(t)
	s := todoServer(t, handConfig(t, repo))
	lock := filepath.Join(repo, ".git", "index.lock")
	for _, mode := range []string{"index.lock", "pre-commit hook"} {
		if mode == "index.lock" {
			writeFile(t, lock, "")
		} else {
			os.Remove(lock)
			writeFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"), "#!/bin/sh\necho no python interpreter found >&2\nexit 1\n")
		}
		rec.calls = nil
		if w := postFeedback(s, `{"type":"todo","text":"x"}`); w.Code != 500 {
			t.Fatalf("%s: code %d", mode, w.Code)
		}
		noTodoFiles(t, repo)
		if readFile(t, filepath.Join(repo, "todo", "PRIORITY.md")) != prio {
			t.Fatalf("%s: PRIORITY.md not restored", mode)
		}
		noNoVerify(t, rec)
	}
	if !slices.ContainsFunc(rec.argvs(), func(a string) bool { return strings.HasPrefix(a, "git commit ") }) {
		t.Fatal("commit not attempted")
	}
	if got := gitT(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status %q", got)
	}
	logKept(t, "x")
}

func TestFeedbackTodoBuiltInWriterUnknownBucketAndExistingID(t *testing.T) {
	t.Run("unknown bucket", func(t *testing.T) {
		repo := fakeAdoptedRepo(t)
		rec := useRunner(t, adoptedRunner(func([]string) ([]byte, error) { return nil, nil }))
		fbc := handConfig(t, repo)
		fbc.TodoBucket = "nope"
		s := todoServer(t, fbc)
		if w := postFeedback(s, `{"type":"todo","text":"x"}`); w.Code != 500 {
			t.Fatalf("code %d", w.Code)
		}
		if len(rec.calls) != 2 {
			t.Fatalf("calls %q", rec.argvs())
		}
		noTodoFiles(t, repo)
		if readFile(t, filepath.Join(repo, "todo", "PRIORITY.md")) != priorityTemplate {
			t.Fatal("PRIORITY.md changed")
		}
	})
	t.Run("PRIORITY.md changed during the id scan keeps that change", func(t *testing.T) {
		repo := fakeAdoptedRepo(t)
		prioPath := filepath.Join(repo, "todo", "PRIORITY.md")
		theirs := strings.Replace(priorityTemplate, "## P2\n\nnone", "## P2\n\n- T0005 · draft · feedback · S — theirs", 1)
		useRunner(t, adoptedRunner(func(argv []string) ([]byte, error) {
			if argv[1] == "log" { // another process commits T0005 while we scan
				writeFile(t, prioPath, theirs)
				return []byte("todo: T0005 created: theirs [cli]\n"), nil
			}
			return nil, nil
		}))
		s := todoServer(t, handConfig(t, repo))
		wantResp(t, postFeedback(s, `{"type":"todo","text":"x"}`), 200, `{"status":"added","todo":"T0006"}`)
		if got := readFile(t, prioPath); !strings.Contains(got, "- T0005 · draft · feedback · S — theirs\n- T0006 · draft · feedback · S — x\n") {
			t.Fatalf("PRIORITY.md %q", got)
		}
	})
	t.Run("id file appears before the write", func(t *testing.T) {
		repo := fakeAdoptedRepo(t)
		useRunner(t, adoptedRunner(func([]string) ([]byte, error) { return nil, nil }))
		old := beforeTodoCreate
		beforeTodoCreate = func(p string) { writeFile(t, p, "theirs") }
		t.Cleanup(func() { beforeTodoCreate = old })
		s := todoServer(t, handConfig(t, repo))
		if w := postFeedback(s, `{"type":"todo","text":"x"}`); w.Code != 500 {
			t.Fatalf("code %d", w.Code)
		}
		if readFile(t, filepath.Join(repo, "todo", "T0001.md")) != "theirs" {
			t.Fatal("existing file changed")
		}
		if readFile(t, filepath.Join(repo, "todo", "PRIORITY.md")) != priorityTemplate {
			t.Fatal("PRIORITY.md changed")
		}
	})
}

// ---- content ----

func TestFeedbackTodoDuplicateScansOnlyTodoFiles(t *testing.T) {
	repo := fakeAdoptedRepo(t)
	writeFile(t, filepath.Join(repo, "todo", "T0003.md"), "---\nid: T0003\nstate: designed\n---\nfix the\r\nthing\n\nmore\n")
	// PRIORITY.md shaped like a todo with the text as its first paragraph: must not count.
	writeFile(t, filepath.Join(repo, "todo", "PRIORITY.md"), "---\n---\nonly in priority\n")
	rec := useRunner(t, adoptedRunner(func(argv []string) ([]byte, error) {
		return []byte("T0004 created: x (pending)\n"), nil
	}))
	s := todoServer(t, toolConfig(t, repo))
	wantResp(t, postFeedback(s, `{"type":"todo","text":"fix  the thing"}`), 200, `{"status":"duplicate"}`)
	if len(rec.calls) != 2 {
		t.Fatalf("duplicate ran %q", rec.argvs())
	}
	wantResp(t, postFeedback(s, `{"type":"todo","text":"only in priority"}`), 200, `{"status":"added","todo":"T0004"}`)
}

func TestTodoTitle(t *testing.T) {
	long := strings.Repeat("a", 119) + " " + strings.Repeat("b", 10)
	for in, want := range map[string]string{
		"short":                          "short",
		strings.Repeat("x", 120):         strings.Repeat("x", 120),
		strings.Repeat("x", 130):         strings.Repeat("x", 120),
		long:                             strings.Repeat("a", 119),
		strings.Repeat("a", 120) + " b":  strings.Repeat("a", 120),
		"ab " + strings.Repeat("ł", 130): "ab",
	} {
		if got := todoTitle(in); got != want {
			t.Errorf("todoTitle(%.20q...) = %.20q... (%d runes), want %d runes", in, got, len([]rune(got)), len([]rune(want)))
		}
	}
}

func TestIsStartError(t *testing.T) {
	if !isStartError(exec.ErrNotFound) || isStartError(&cmdExitError{code: 1}) || isStartError(context.DeadlineExceeded) ||
		isStartError(nil) || !isStartError(errors.New("fork failed")) {
		t.Fatal("isStartError")
	}
}
