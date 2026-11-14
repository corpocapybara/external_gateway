package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An op key with no body (`a.b:` and nothing) unmarshals to a nil *Operation;
// loading must refuse it by name instead of panicking (EG0023).
func TestLoadNilOperation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ops.yaml")
	if err := os.WriteFile(p, []byte("operations:\n  a.b:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(p)
	if err == nil {
		t.Fatalf("NewRegistry: nil error, registry %v; want an empty-definition error", reg)
	}
	if !strings.Contains(err.Error(), "operation a.b: empty definition") {
		t.Fatalf("NewRegistry error %q, want it to contain %q", err, "operation a.b: empty definition")
	}
}
