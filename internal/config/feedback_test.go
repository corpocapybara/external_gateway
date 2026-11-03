package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFeedbackTodoPathLoadAndAbsoluteCheck(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "TODO.md")
	load := func(p string) (*Config, error) {
		// yaml single-quoted so backslashes are literal
		y := "agents:\n  - id: bot\n    token_sha256: sha\nfeedback:\n  todo_path: '" + p + "'\n"
		f := filepath.Join(t.TempDir(), "config.x.yaml")
		if err := os.WriteFile(f, []byte(y), 0644); err != nil {
			t.Fatal(err)
		}
		return Load(f)
	}
	cfg, err := load(abs)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}
	if cfg.Feedback.TodoPath != abs {
		t.Fatalf("todo_path = %q, want %q", cfg.Feedback.TodoPath, abs)
	}
	if _, err := load("TODO.md"); err == nil {
		t.Fatal("relative todo_path accepted")
	}
}
