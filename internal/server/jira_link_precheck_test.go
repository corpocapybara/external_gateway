package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

// recUpstream records "METHOD path?query" for every request and answers with
// the per-case handler.
type recUpstream struct {
	mu      sync.Mutex
	reqs    []string
	handler http.HandlerFunc
	srv     *httptest.Server
}

func newRecUpstream(t *testing.T) *recUpstream {
	u := &recUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.reqs = append(u.reqs, r.Method+" "+r.URL.RequestURI())
		u.mu.Unlock()
		u.handler(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *recUpstream) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	r := u.reqs
	u.reqs = nil
	return r
}

// auditRecords returns the audit records written for one request id, read from
// every audit_*.log (audit.Init is process-global, so the directory is shared).
func auditRecords(t *testing.T, requestID string) []audit.Record {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(audit.GetLogger().Dir(), "audit_*.log"))
	var out []audit.Record
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var rec audit.Record
			if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.RequestID == requestID {
				out = append(out, rec)
			}
		}
		fh.Close()
	}
	return out
}

func TestJiraLinkDeletePrecheck(t *testing.T) {
	// Init first: Dir() is read below. A process-global logger from an earlier test is kept.
	if err := audit.Init(t.TempDir(), 1); err != nil {
		t.Fatal(err)
	}
	// An earlier test's t.TempDir may already be gone: recreate the shared audit dir.
	if err := os.MkdirAll(audit.GetLogger().Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	prod, stg, dead := newRecUpstream(t), newRecUpstream(t), newRecUpstream(t)
	dead.srv.Close()

	scope := []map[string]interface{}{{"type": "jira_key_scope", "projects": "$workspace.jira_projects", "issue_keys": []interface{}{"key"}}}
	ops := map[string]*policy.Operation{
		"jira.link_delete": {
			ID: "jira.link_delete", Connector: "http-rest", Site: "jira", Method: "DELETE", Path: "/rest/api/2/issueLink/{id}",
			Params:      map[string]policy.ParamDef{"id": {Type: "string", Required: true, Pattern: "^[0-9]+$"}, "extra": {Type: "string"}},
			Precheck:    "jira_issue_link",
			Constraints: scope,
		},
		"jira.edit": {
			ID: "jira.edit", Connector: "http-rest", Site: "jira", Method: "PUT", Path: "/rest/api/2/issue/{key}",
			Params: map[string]policy.ParamDef{"key": {Type: "string", Required: true}}, Constraints: scope,
		},
	}
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{"thp": {
			Policy: config.WorkspacePolicy{JiraProjects: []string{"THP"}},
			Sites: map[string]config.Site{"jira": {DefaultEnv: "prod", Environments: map[string]config.Environment{
				"prod": {BaseURL: prod.srv.URL}, "stg": {BaseURL: stg.srv.URL}, "dead": {BaseURL: dead.srv.URL},
			}}},
		}},
		Roles:  map[string]config.Role{"dev": {Operations: []string{"jira.link_delete", "jira.edit"}}},
		Agents: []config.Agent{{ID: "bot", TokenSHA: "bot-sha", Grants: []config.Grant{{Workspace: "thp", Role: "dev"}}}},
	}
	srv := &Server{logger: zerolog.Nop(), auditLog: audit.GetLogger(), httpClient: httprest.NewConnector()}
	srv.state.Store(&runtimeState{cfg: cfg, reg: &policy.OperationRegistry{Operations: ops}})

	link := func(in, out string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.Write([]byte(`{"id":"10","inwardIssue":` + in + `,"outwardIssue":` + out + `}`))
				return
			}
			w.WriteHeader(204)
		}
	}
	ok := `{"key":"THP-1"}`
	getThen := func(get http.HandlerFunc, del int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				get(w, r)
				return
			}
			w.WriteHeader(del)
		}
	}
	status := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code); w.Write([]byte(body)) }
	}
	const get, del = "GET /rest/api/2/issueLink/10", "DELETE /rest/api/2/issueLink/10"

	cases := []struct {
		name    string
		handler http.HandlerFunc
		body    string
		dryOp   string
		code    int      // HTTP status of the gateway response
		upstr   int      // upstream_status in a 200 response
		rule    string   // "" = not a deny
		want    []string // recorded on the target server
		other   []string // recorded on the prod server when the target is another
		audit   string   // "deny", "allow" or "" (not checked)
	}{
		{name: "both in scope", handler: link(ok, `{"key":"THP-2"}`), body: `{"id":"10","extra":"x"}`, code: 200, upstr: 204, want: []string{get, del + "?extra=x"}, audit: "allow"}, // GET has an empty query; the op request keeps the extra param
		{name: "env stg", handler: link(ok, ok), body: `{"id":"10","env":"stg"}`, code: 200, upstr: 204, want: []string{get, del}},
		{name: "cluster stg", handler: link(ok, ok), body: `{"id":"10","cluster":"stg"}`, code: 200, upstr: 204, want: []string{get, del}},
		{name: "env non-string, cluster stg", handler: link(ok, ok), body: `{"id":"10","env":5,"cluster":"stg"}`, code: 200, upstr: 204, want: []string{get, del}},
		{name: "delete 404", handler: getThen(link(ok, ok), 404), body: `{"id":"10"}`, code: 200, upstr: 404, want: []string{get, del}},
		{name: "inward out of scope", handler: link(`{"key":"ABC-1"}`, ok), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "outward out of scope", handler: link(ok, `{"key":"ABC-1"}`), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "key missing", handler: link(ok, `{"id":"3"}`), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "key a number", handler: link(`{"key":7}`, ok), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "empty object", handler: status(200, `{}`), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "inward null", handler: link(`null`, ok), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "non-JSON 200", handler: status(200, `<html>`), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "GET 302", handler: status(302, ``), body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "GET 302 with in-scope body", handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(302)
			w.Write([]byte(`{"inwardIssue":{"key":"THP-1"},"outwardIssue":{"key":"THP-2"}}`))
		}, body: `{"id":"10"}`, code: 403, rule: "constraint", want: []string{get}, audit: "deny"},
		{name: "GET 404", handler: status(404, `{"errorMessages":["gone"]}`), body: `{"id":"10"}`, code: 200, upstr: 404, want: []string{get}},
		{name: "transport error", body: `{"id":"10","env":"dead"}`, code: 200, rule: "error", audit: "allow"},
		{name: "id with newline", handler: link(ok, ok), body: `{"id":"12\n"}`, code: 403, rule: "validation"},
		{name: "id a number", handler: link(ok, ok), body: `{"id":12}`, code: 403, rule: "validation"},
		{name: "dry run out of scope", handler: link(ok, ok), body: `{"key":"ABC-1"}`, dryOp: "jira.edit", code: 403, rule: "constraint"},
	}
	for _, c := range cases {
		prod.handler, stg.handler = c.handler, c.handler
		target := prod
		if strings.Contains(c.body, `"stg"`) {
			target = stg
		}
		opID := "jira.link_delete"
		if c.dryOp != "" {
			opID = c.dryOp
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/thp/op/"+opID, strings.NewReader(c.body))
		w := httptest.NewRecorder()
		if c.dryOp != "" {
			srv.handleDryRun(w, r, "bot-sha", "thp", opID)
		} else {
			srv.executeOp(w, r, "bot-sha", "thp", opID)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Errorf("%s: response is not JSON: %v (%s)", c.name, err, w.Body.String())
			continue
		}
		if w.Code != c.code {
			t.Errorf("%s: HTTP %d, want %d (%v)", c.name, w.Code, c.code, out)
		}
		switch c.rule {
		case "error":
			if out["status"] != "error" || !strings.Contains(out["error"].(string), "upstream request failed") {
				t.Errorf("%s: body %v, want the upstream error response", c.name, out)
			}
		case "":
			if int(out["upstream_status"].(float64)) != c.upstr {
				t.Errorf("%s: upstream_status %v, want %d", c.name, out["upstream_status"], c.upstr)
			}
		default:
			if out["rule"] != c.rule {
				t.Errorf("%s: rule %v, want %s (%v)", c.name, out["rule"], c.rule, out)
			}
		}
		if c.dryOp != "" {
			if got := prod.take(); len(got) != 0 {
				t.Errorf("%s: dry run reached upstream: %v", c.name, got)
			}
		}
		if c.dryOp == "" && strings.Contains(w.Body.String(), "ABC-1") {
			t.Errorf("%s: response echoes the upstream issue key: %s", c.name, w.Body.String())
		}
		if c.dryOp == "" {
			if got := target.take(); strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("%s: recorded %v, want %v", c.name, got, c.want)
			}
			if target != prod {
				if got := prod.take(); len(got) != 0 {
					t.Errorf("%s: prod server saw %v", c.name, got)
				}
			}
		}
		if c.audit != "" {
			recs := auditRecords(t, w.Header().Get("X-Request-ID"))
			if len(recs) != 1 || recs[0].Decision != c.audit {
				t.Errorf("%s: audit records %+v, want exactly one %s", c.name, recs, c.audit)
			} else if c.audit == "deny" && recs[0].Rule != "constraint" {
				t.Errorf("%s: audit rule %q, want constraint", c.name, recs[0].Rule)
			} else if c.rule == "error" && recs[0].UpstreamStatus != 0 {
				t.Errorf("%s: audit upstream_status %d, want 0", c.name, recs[0].UpstreamStatus)
			}
		}
	}
	// The GET carries no query string even though the call had an extra param
	// (case "both in scope" sent extra=x): the recorded GET path has no "?".
}
