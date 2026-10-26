package policy

import (
	"path/filepath"
	"testing"

	"github.com/external_gateway/internal/config"
)

func TestSlackFileOpFromExampleCatalog(t *testing.T) {
	r, err := NewRegistry(filepath.Join("..", "..", "config.example", "operations.yaml"))
	if err != nil {
		t.Fatalf("loading example catalog: %v", err)
	}
	op, ok := r.Get("slack.file")
	if !ok {
		t.Fatal("slack.file is not declared")
	}
	if op.Method != "GET" || op.Path != "/files.info" || op.Site != "slack" {
		t.Fatalf("unexpected op shape: %s %s on %s", op.Method, op.Path, op.Site)
	}
	w := ws(config.WorkspacePolicy{})
	if err := r.ValidateParams(op, map[string]interface{}{"file": "F0FILE123"}, w); err != nil {
		t.Fatalf("valid file id rejected: %v", err)
	}
	for _, bad := range []string{"", "f0file123", "F0FILE123&token=x", "../F0FILE123", "https://files.slack.com/files-pri/T1-F1/x"} {
		if err := r.ValidateParams(op, map[string]interface{}{"file": bad}, w); err == nil {
			t.Fatalf("file %q should be rejected", bad)
		}
	}
	if err := r.ValidateParams(op, map[string]interface{}{}, w); err == nil {
		t.Fatal("missing file should be rejected")
	}
}

func TestSlackFileContentOpFromExampleCatalog(t *testing.T) {
	r, err := NewRegistry(filepath.Join("..", "..", "config.example", "operations.yaml"))
	if err != nil {
		t.Fatalf("loading example catalog: %v", err)
	}
	op, ok := r.Get("slack.file_content")
	if !ok {
		t.Fatal("slack.file_content is not declared")
	}
	if !op.SlackFileContent || op.Method != "GET" || op.Site != "slack" {
		t.Fatalf("unexpected op shape: %+v", op)
	}
	w := ws(config.WorkspacePolicy{})
	if err := r.ValidateParams(op, map[string]interface{}{"file": "F0FILE123"}, w); err != nil {
		t.Fatalf("valid file id rejected: %v", err)
	}
	if err := r.ValidateParams(op, map[string]interface{}{"file": "https://files.slack.com/files-pri/T1-F1/x"}, w); err == nil {
		t.Fatal("a url must be rejected")
	}
	if err := r.ValidateParams(op, map[string]interface{}{"file": "F1", "url": "https://evil.example"}, w); err == nil {
		t.Fatal("an extra url param must be rejected")
	}
}
