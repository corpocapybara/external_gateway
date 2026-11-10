package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedbackTodoConfigLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	repo, tool, py := filepath.Join(dir, "repo"), filepath.Join(dir, "todo.py"), filepath.Join(dir, "python.exe")
	legacy := filepath.Join(dir, LegacyTodoName)
	load := func(fb string) (*Config, error) {
		y := "agents:\n  - id: bot\n    token_sha256: sha\nfeedback:\n" + fb
		f := filepath.Join(t.TempDir(), "config.x.yaml")
		if err := os.WriteFile(f, []byte(y), 0644); err != nil {
			t.Fatal(err)
		}
		return Load(f)
	}
	// yaml single-quoted so backslashes are literal
	kv := func(k, v string) string { return "  " + k + ": '" + v + "'\n" }
	base := kv("todo_repo", repo) + kv("todo_python", py) + kv("todo_bucket", "feedback")

	cfg, err := load(base + kv("todo_tool", tool))
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if fb := cfg.Feedback; fb.TodoRepo != repo || fb.TodoTool != tool || fb.TodoPython != py || fb.TodoBucket != "feedback" {
		t.Fatalf("loaded %+v", fb)
	}
	if got, want := cfg.Feedback.LegacyTodoPath(), filepath.Join(repo, LegacyTodoName); got != want {
		t.Fatalf("default legacy path %q, want %q", got, want)
	}
	cfg, err = load(base + kv("todo_path", legacy))
	if err != nil || cfg.Feedback.LegacyTodoPath() != legacy {
		t.Fatalf("todo_path: %v %+v", err, cfg)
	}
	if _, err := load(base); err != nil {
		t.Fatalf("no todo_tool: %v", err)
	}
	if _, err := load("  todo_bucket: x\n"); err != nil {
		t.Fatalf("no todo_repo: %v", err)
	}

	for name, tc := range map[string]struct{ fb, wantErr string }{
		"todo_path without todo_repo": {kv("todo_path", legacy), "todo_repo"},
		"relative todo_repo":          {kv("todo_repo", "repo") + kv("todo_python", py) + kv("todo_bucket", "b"), "todo_repo"},
		"relative todo_tool":          {base + kv("todo_tool", "todo.py"), "todo_tool"},
		"relative todo_path":          {base + kv("todo_path", LegacyTodoName), "todo_path"},
		"empty todo_python":           {kv("todo_repo", repo) + kv("todo_bucket", "b"), "todo_python"},
		"relative todo_python":        {kv("todo_repo", repo) + kv("todo_python", "python") + kv("todo_bucket", "b"), "todo_python"},
		"no todo_bucket":              {kv("todo_repo", repo) + kv("todo_python", py), "todo_bucket"},
	} {
		if _, err := load(tc.fb); err == nil || !strings.Contains(err.Error(), "feedback."+tc.wantErr) {
			t.Errorf("%s: err %v, want one naming %s", name, err, tc.wantErr)
		}
	}
}
