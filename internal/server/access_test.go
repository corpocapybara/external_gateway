package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

// accessServer: agent "alice" holds role "r" (op a.one only) in workspace "acme";
// workspace "globex" exists but alice has no grant there. Both sites expose a.one
// and a.two, so a listing of "all workspace ops" would leak a.two.
func accessServer(t *testing.T) *Server {
	t.Helper()
	t.Chdir(t.TempDir())
	site := config.Site{Environments: map[string]config.Environment{"prod": {}}}
	cfg := &config.Config{
		Roles: map[string]config.Role{"r": {Operations: []string{"a.one"}}},
		Agents: []config.Agent{
			{ID: "alice", TokenSHA: tokenSHA("alice-tok"), Grants: []config.Grant{{Workspace: "acme", Role: "r"}}},
		},
		Workspaces: map[string]config.Workspace{
			"acme":   {Description: "acme desc", Sites: map[string]config.Site{"jira": site}},
			"globex": {Description: "globex desc", Sites: map[string]config.Site{"jira": {Environments: map[string]config.Environment{"secret-env": {}}}}},
		},
	}
	reg := &policy.OperationRegistry{Operations: map[string]*policy.Operation{
		"a.one": {ID: "a.one", Site: "jira"},
		"a.two": {ID: "a.two", Site: "jira"},
	}}
	return New(cfg, reg, "", zerolog.Nop())
}

func get(s *Server, target, tok string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func TestHelpWorkspaceRequiresToken(t *testing.T) {
	s := accessServer(t)
	for _, tok := range []string{"", "wrong"} {
		if w := get(s, "/help?workspace=acme", tok); w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: code=%d, want 401", tok, w.Code)
		}
	}
	if w := get(s, "/help", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("generic help with bad token: code=%d, want 401", w.Code)
	}
}

func TestHelpOtherWorkspaceDenied(t *testing.T) {
	s := accessServer(t)
	for _, ws := range []string{"globex", "nonexistent"} {
		w := get(s, "/help?workspace="+ws, "alice-tok")
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: code=%d, want 403", ws, w.Code)
		}
		if strings.Contains(w.Body.String(), "secret-env") {
			t.Fatalf("%s: leaked env: %q", ws, w.Body.String())
		}
	}
}

func TestHelpGrantedShowsOnlyGrantedOps(t *testing.T) {
	s := accessServer(t)
	for _, target := range []string{"/help?workspace=acme", "/help"} {
		w := get(s, target, "alice-tok")
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, "a.one") || !strings.Contains(body, "Workspace: acme") {
			t.Fatalf("%s: %d %q", target, w.Code, body)
		}
		if strings.Contains(body, "a.two") || strings.Contains(body, "globex") || strings.Contains(body, "secret-env") {
			t.Fatalf("%s: leaked ungranted detail: %q", target, body)
		}
	}
}

func TestHelpPublicBannerNamesNoWorkspace(t *testing.T) {
	s := accessServer(t)
	w := get(s, "/help", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Endpoints:") {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
	for _, leak := range []string{"acme", "globex", "desc", "a.one"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("public help leaks %q: %q", leak, w.Body.String())
		}
	}
}

func TestCapabilitiesAuthAndGrantScope(t *testing.T) {
	s := accessServer(t)
	if w := get(s, "/capabilities?workspace=acme", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code=%d", w.Code)
	}
	if w := get(s, "/capabilities?workspace=globex", "alice-tok"); w.Code != http.StatusForbidden {
		t.Fatalf("other workspace: code=%d", w.Code)
	}
	w := get(s, "/capabilities?workspace=acme", "alice-tok")
	if w.Code != 200 {
		t.Fatalf("granted: code=%d %q", w.Code, w.Body.String())
	}
	var resp struct {
		Operations []struct{ ID string } `json:"operations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Operations) != 1 || resp.Operations[0].ID != "a.one" {
		t.Fatalf("operations=%+v, want only a.one", resp.Operations)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), `"policy"`) {
		t.Fatalf("policy leaked: %q", w.Body.String())
	}
}
