package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/rs/zerolog"
)

// fakeStore records admin secret writes so the real handler can be driven without
// touching Windows Credential Manager.
type fakeStore struct {
	mu      sync.Mutex
	sets    []string
	deletes []string
}

func (f *fakeStore) Resolve(ws, name string) ([]byte, error) { return nil, nil }
func (f *fakeStore) Set(ws, name string, v []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, ws+"/"+name)
	return nil
}
func (f *fakeStore) Delete(ws, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, ws+"/"+name)
	return nil
}

const adminTok = "admin-secret"

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func adminServer(t *testing.T) (*Server, *fakeStore) {
	t.Helper()
	fs := &fakeStore{}
	secrets.SetStore(fs)
	cfg := &config.Config{AdminTokenSHA: sha(adminTok)}
	return NewAdminTestServer(cfg), fs
}

// NewAdminTestServer is kept test-local: a Server with only config and an empty registry.
func NewAdminTestServer(cfg *config.Config) *Server {
	s := &Server{logger: zerolog.Nop(), mux: http.NewServeMux()}
	s.state.Store(&runtimeState{cfg: cfg, reg: &policy.OperationRegistry{Operations: map[string]*policy.Operation{}}})
	s.setupRoutes()
	return s
}

func adminDo(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+adminTok)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAdminSecretsDeleteReadsQuery(t *testing.T) {
	s, fs := adminServer(t)
	w := adminDo(s, http.MethodDelete, "/admin/secrets?workspace=acme&name=jira", "")
	if w.Code != 200 || len(fs.deletes) != 1 || fs.deletes[0] != "acme/jira" {
		t.Fatalf("code %d deletes %v body %s", w.Code, fs.deletes, w.Body.String())
	}
	// The old egw-admin client sent a JSON body; the server rejects that shape.
	w = adminDo(s, http.MethodDelete, "/admin/secrets", `{"workspace":"acme","name":"jira"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("body-only delete: code %d, want 400", w.Code)
	}
}

func TestAdminSecretsRejectsBadNames(t *testing.T) {
	s, fs := adminServer(t)
	for _, c := range []struct{ ws, name string }{
		{"acme", "a b"}, {"acme", "../x"}, {"ac/me", "jira"}, {"acme", "jira\n"}, {"acme", "na;me"},
	} {
		body := `{"workspace":"` + c.ws + `","name":"` + strings.ReplaceAll(c.name, "\n", `\n`) + `","value":"v"}`
		if w := adminDo(s, http.MethodPost, "/admin/secrets", body); w.Code != http.StatusBadRequest {
			t.Errorf("set %q/%q: code %d, want 400", c.ws, c.name, w.Code)
		}
	}
	if w := adminDo(s, http.MethodDelete, "/admin/secrets?workspace=acme&name=a%20b", ""); w.Code != http.StatusBadRequest {
		t.Errorf("delete bad name: code %d, want 400", w.Code)
	}
	if len(fs.sets)+len(fs.deletes) != 0 {
		t.Fatalf("store touched: %v %v", fs.sets, fs.deletes)
	}
	// Real names from config.example/docs are accepted.
	for _, n := range []string{"jira", "pg-prod", "gdocs-client-secret", "slack-xoxc", "kibana_globex"} {
		body := `{"workspace":"globex","name":"` + n + `","value":"v"}`
		if w := adminDo(s, http.MethodPost, "/admin/secrets", body); w.Code != 200 {
			t.Errorf("set %q: code %d, want 200", n, w.Code)
		}
	}
}

func TestAdminBodyLimits(t *testing.T) {
	s, fs := adminServer(t)
	big := `{"workspace":"acme","name":"jira","value":"` + strings.Repeat("x", maxAdminSecretBody+10) + `"}`
	if w := adminDo(s, http.MethodPost, "/admin/secrets", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("secrets oversize: code %d, want 413", w.Code)
	}
	if len(fs.sets) != 0 {
		t.Errorf("oversized secret stored")
	}
	// exactly at the limit is not rejected for size (it fails later as bad JSON/400)
	pad := maxAdminSecretBody - len(`{"workspace":"acme","name":"jira","value":""}`)
	atLimit := `{"workspace":"acme","name":"jira","value":"` + strings.Repeat("x", pad) + `"}`
	if w := adminDo(s, http.MethodPost, "/admin/secrets", atLimit); w.Code != 200 {
		t.Errorf("secrets at limit: code %d, want 200", w.Code)
	}
	bigDiag := `{"workspace":"acme","site":"` + strings.Repeat("x", maxAdminDiagBody+10) + `"}`
	if w := adminDo(s, http.MethodPost, "/admin/diag", bigDiag); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("diag oversize: code %d, want 413", w.Code)
	}
}

// Hot-reload swaps config and registry while requests are in flight; run with -race.
func TestReloadConcurrentWithRequests(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	opsPath := filepath.Join(dir, "ops.yaml")
	token := "agent-token"
	cfgYAML := "admin_token_sha256: " + sha(adminTok) + "\n" +
		"workspaces:\n  ws: {}\nroles:\n  r:\n    operations: []\n" +
		"agents:\n  - id: bot\n    token_sha256: " + sha(token) + "\n    grants:\n      - {workspace: ws, role: r}\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opsPath, []byte("operations:\n  a.b:\n    connector: http-rest\n    site: s\n    method: GET\n    path: /x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := policy.NewRegistry(opsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Init(dir, 1); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, reg, opsPath, zerolog.Nop())

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws/op/a.b?dry_run=true", strings.NewReader("{}"))
				r.Header.Set("Authorization", "Bearer "+token)
				s.ServeHTTP(httptest.NewRecorder(), r)
			}
		}()
	}
	for i := 0; i < 50; i++ {
		if w := adminDo(s, http.MethodPost, "/admin/reload", ""); w.Code != 200 {
			t.Fatalf("reload %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	close(stop)
	wg.Wait()
}

// A reload of an ops file with an unknown constraint type is refused with 500 and
// leaves the previously loaded registry serving requests.
func TestReloadRefusesUnknownConstraintType(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	opsPath := filepath.Join(dir, "ops.yaml")
	token := "agent-token"
	cfgYAML := "admin_token_sha256: " + sha(adminTok) + "\n" +
		"workspaces:\n  ws:\n    sites:\n      a: {}\nroles:\n  r:\n    operations: [a.b]\n" +
		"agents:\n  - id: bot\n    token_sha256: " + sha(token) + "\n    grants:\n      - {workspace: ws, role: r}\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	const op = "operations:\n  a.b:\n    connector: http-rest\n    site: s\n    method: GET\n    path: /x\n"
	if err := os.WriteFile(opsPath, []byte(op), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := policy.NewRegistry(opsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Init(dir, 1); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, reg, opsPath, zerolog.Nop())
	dryRun := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws/op/a.b?dry_run=true", strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := dryRun(); w.Code != http.StatusOK {
		t.Fatalf("dry run before reload: %d %s", w.Code, w.Body.String())
	}

	// Had this file been installed, CheckConstraints would deny a.b with 403.
	bad := op + "    constraints:\n      - type: jql_scop\n"
	if err := os.WriteFile(opsPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	w := adminDo(s, http.MethodPost, "/admin/reload", "")
	if w.Code != http.StatusInternalServerError ||
		!strings.Contains(w.Body.String(), "ops reload failed") ||
		!strings.Contains(w.Body.String(), "unknown constraint type") {
		t.Fatalf("reload: %d %s, want 500 ops reload failed: unknown constraint type", w.Code, w.Body.String())
	}
	if w := dryRun(); w.Code != http.StatusOK {
		t.Fatalf("dry run after refused reload: %d %s, want the old registry's 200", w.Code, w.Body.String())
	}
}
