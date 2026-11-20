package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

// TestAuditAgentIDIsConfigID: audit records name the configured agent id, never
// the token's SHA-256 (EG0064, review R14).
func TestAuditAgentIDIsConfigID(t *testing.T) {
	if err := audit.Init(t.TempDir(), 1, 0); err != nil {
		t.Fatal(err)
	}
	// An earlier test's t.TempDir may already be gone: recreate the shared audit dir.
	if err := os.MkdirAll(audit.GetLogger().Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	up := newRecUpstream(t)
	up.handler = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }

	const token = "audit-agent-id-token"
	sum := sha256.Sum256([]byte(token))
	tokenSHA := hex.EncodeToString(sum[:])

	ops := map[string]*policy.Operation{
		"jira.get": {ID: "jira.get", Connector: "http-rest", Site: "jira", Method: "GET", Path: "/rest/api/2/issue/{key}",
			Params: map[string]policy.ParamDef{"key": {Type: "string", Required: true, Pattern: "^[A-Z]+-[0-9]+$"}}},
		"jira.delete": {ID: "jira.delete", Connector: "http-rest", Site: "jira", Method: "DELETE", Path: "/rest/api/2/issue/{key}",
			Params: map[string]policy.ParamDef{"key": {Type: "string", Required: true}}},
	}
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{"acme": {
			Sites: map[string]config.Site{"jira": {DefaultEnv: "prod", Environments: map[string]config.Environment{
				"prod": {BaseURL: up.srv.URL},
			}}},
		}},
		Roles:  map[string]config.Role{"reader": {Operations: []string{"jira.get"}}},
		Agents: []config.Agent{{ID: "audit-bot", TokenSHA: tokenSHA, Grants: []config.Grant{{Workspace: "acme", Role: "reader"}}}},
	}
	srv := &Server{logger: zerolog.Nop(), auditLog: audit.GetLogger(), httpClient: httprest.NewConnector()}
	srv.state.Store(&runtimeState{cfg: cfg, reg: &policy.OperationRegistry{Operations: ops}})

	cases := []struct {
		name, path, body, decision, rule string
		code                             int
	}{
		{name: "allow", path: "/v1/workspaces/acme/op/jira.get", body: `{"key":"ACME-1"}`, code: 200, decision: "allow"},
		{name: "workspace not found", path: "/v1/workspaces/nope/op/jira.get", body: `{"key":"ACME-1"}`, code: 403, decision: "deny", rule: "workspace"},
		{name: "grant deny", path: "/v1/workspaces/acme/op/jira.delete", body: `{"key":"ACME-1"}`, code: 403, decision: "deny", rule: "grant"},
		{name: "validation deny", path: "/v1/workspaces/acme/op/jira.get", body: `{"key":"bad key"}`, code: 403, decision: "deny", rule: "validation"},
		{name: "unknown op", path: "/v1/workspaces/acme/op/jira.nope", body: `{}`, code: 404, decision: "deny", rule: "operation"},
		{name: "dry-run grant deny", path: "/v1/workspaces/acme/op/jira.delete?dry_run=true", body: `{"key":"ACME-1"}`, code: 403, decision: "deny", rule: "grant"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.handleWorkspaceOp(w, r)
		if w.Code != c.code {
			t.Errorf("%s: HTTP %d, want %d (%s)", c.name, w.Code, c.code, w.Body.String())
		}
		recs := auditRecords(t, w.Header().Get("X-Request-ID"))
		if len(recs) != 1 {
			t.Fatalf("%s: %d audit records, want 1", c.name, len(recs))
		}
		if rec := recs[0]; rec.AgentID != "audit-bot" || rec.Decision != c.decision || rec.Rule != c.rule {
			t.Errorf("%s: audit agent_id=%q decision=%q rule=%q, want audit-bot/%s/%s", c.name, rec.AgentID, rec.Decision, rec.Rule, c.decision, c.rule)
		}
	}

	// Agent gone from config between authenticate and executeOp (a reload): the
	// deny is audited with no agent id rather than the token hash.
	w := httptest.NewRecorder()
	srv.executeOp(w, httptest.NewRequest(http.MethodPost, "/v1/workspaces/acme/op/jira.get", strings.NewReader(`{}`)), "f00d"+tokenSHA[4:], "acme", "jira.get")
	recs := auditRecords(t, w.Header().Get("X-Request-ID"))
	if len(recs) != 1 || recs[0].AgentID != "" || recs[0].Rule != "agent" {
		t.Errorf("agent gone: audit records %+v, want one deny with rule agent and empty agent_id", recs)
	}

	// The token hash appears in no audit line.
	files, _ := filepath.Glob(filepath.Join(audit.GetLogger().Dir(), "audit_*.log"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), tokenSHA[:16]) {
			t.Errorf("%s holds the token SHA-256", f)
		}
	}
}
