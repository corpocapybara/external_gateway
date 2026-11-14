package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOps(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ops.yaml")
	if err := os.WriteFile(p, []byte("operations:\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantLoadError(t *testing.T, path, want string) {
	t.Helper()
	reg, err := NewRegistry(path)
	if err == nil {
		t.Fatalf("NewRegistry: nil error, registry %v; want an error containing %q", reg, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("NewRegistry error %q, want it to contain %q", err, want)
	}
}

// `a.b: {}` unmarshals to a non-nil op with no connector; the EG0023 nil guard
// misses it, so the connector check must refuse it by name (EG0041).
func TestLoadEmptyOperation(t *testing.T) {
	wantLoadError(t, writeOps(t, "  a.b: {}\n"), `operation a.b: unknown connector ""`)
}

func TestLoadEmptyConnector(t *testing.T) {
	wantLoadError(t, writeOps(t, "  a.b:\n    connector: \"\"\n    site: s\n    method: GET\n    path: /x\n"),
		`operation a.b: unknown connector ""`)
}

func TestLoadUnknownConnector(t *testing.T) {
	wantLoadError(t, writeOps(t, "  a.b:\n    connector: http_rest\n    site: s\n    method: GET\n    path: /x\n"),
		`operation a.b: unknown connector "http_rest"`)
}

// Every connector executeOp dispatches (internal/server/server.go) loads. The
// names are pinned here, not read from knownConnectors, so a name dropped from
// or added to the list fails this test until it is changed on purpose.
func TestLoadKnownConnectors(t *testing.T) {
	want := []string{"http-rest", "form-session", "postgres", "local-service", "mcp", "email", "jira-worklog"}
	if strings.Join(knownConnectors, ",") != strings.Join(want, ",") {
		t.Fatalf("knownConnectors = %v, want %v (keep it in step with the executeOp dispatch)", knownConnectors, want)
	}
	for _, c := range want {
		p := writeOps(t, "  a.b:\n    connector: "+c+"\n    site: s\n    method: GET\n    path: /x\n")
		if _, err := NewRegistry(p); err != nil {
			t.Errorf("connector %s: NewRegistry error %v", c, err)
		}
	}
}

// With several bad ops the error always names the first id in sort order, not
// whichever one map iteration reaches first (EG0041).
func TestLoadErrorNamesFirstSortedOp(t *testing.T) {
	var b strings.Builder
	for _, id := range []string{"z.z", "m.m", "c.c", "q.q", "b.b", "x.x", "d.d", "a.a", "k.k"} {
		b.WriteString("  " + id + ":\n    connector: nope\n")
	}
	p := writeOps(t, b.String())
	for i := 0; i < 20; i++ {
		wantLoadError(t, p, `operation a.a: unknown connector "nope"`)
	}
}
