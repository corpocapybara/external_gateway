package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

type exReq struct{ method, path, query, body string }

// TestExampleJiraOps drives the six Jira ops of config.example/operations.yaml
// through the HTTP handler against a recording upstream.
func TestExampleJiraOps(t *testing.T) {
	if err := audit.Init(t.TempDir(), 1); err != nil {
		t.Fatal(err)
	}
	reg, err := policy.NewRegistry("../../config.example/operations.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reqs []exReq
	var handler http.HandlerFunc
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, exReq{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(up.Close)
	take := func() []exReq {
		mu.Lock()
		defer mu.Unlock()
		r := reqs
		reqs = nil
		return r
	}

	ops := []string{"jira.create", "jira.edit", "jira.link", "jira.link_delete", "jira.versions", "jira.editmeta"}
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{"thp": {
			Policy: config.WorkspacePolicy{JiraProjects: []string{"THP"}},
			Sites:  map[string]config.Site{"jira": {Environments: map[string]config.Environment{"prod": {BaseURL: up.URL}}}},
		}},
		Roles:  map[string]config.Role{"dev": {Operations: ops}},
		Agents: []config.Agent{{ID: "bot", TokenSHA: tokenSHA("bot-tok"), Grants: []config.Grant{{Workspace: "thp", Role: "dev"}}}},
	}
	srv := New(cfg, reg, "", zerolog.Nop())

	call := func(op, body string) (int, map[string]interface{}) {
		r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/thp/op/"+op, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer bot-tok")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		var out map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: response not JSON: %v (%s)", op, err, w.Body.String())
		}
		return w.Code, out
	}
	ok200 := func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }

	happy := []struct {
		name, op, body string
		handler        http.HandlerFunc
		want           []exReq
		check          func(t *testing.T, out map[string]interface{})
	}{
		{name: "create", op: "jira.create", body: `{"fields":{"project":{"key":"THP"},"issuetype":{"name":"Bug"},"summary":"s"}}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"id":"1","key":"THP-9","self":"http://x/1"}`))
			},
			want: []exReq{{"POST", "/rest/api/2/issue", "", `{"fields":{"issuetype":{"name":"Bug"},"project":{"key":"THP"},"summary":"s"},"update":{}}`}}},
		{name: "edit update only", op: "jira.edit", body: `{"key":"THP-1","update":{"fixVersions":[{"add":{"name":"9.21.0"}}]}}`, handler: ok200,
			want: []exReq{{"PUT", "/rest/api/3/issue/THP-1", "", `{"fields":{},"update":{"fixVersions":[{"add":{"name":"9.21.0"}}]}}`}}},
		{name: "edit fields only", op: "jira.edit", body: `{"key":"THP-1","fields":{"summary":"n","parent":{"key":"THP-2"}}}`, handler: ok200,
			want: []exReq{{"PUT", "/rest/api/3/issue/THP-1", "", `{"fields":{"parent":{"key":"THP-2"},"summary":"n"},"update":{}}`}}},
		{name: "link", op: "jira.link", body: `{"type":"Blocks","inward":"THP-1","outward":"THP-2"}`, handler: ok200,
			want: []exReq{{"POST", "/rest/api/2/issueLink", "", `{"type":{"name":"Blocks"},"inwardIssue":{"key":"THP-1"},"outwardIssue":{"key":"THP-2"}}`}}},
		{name: "link_delete", op: "jira.link_delete", body: `{"id":"10"}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.Write([]byte(`{"id":"10","inwardIssue":{"key":"THP-1"},"outwardIssue":{"key":"THP-2"}}`))
					return
				}
				w.WriteHeader(204)
			},
			want: []exReq{{"GET", "/rest/api/2/issueLink/10", "", ""}, {"DELETE", "/rest/api/2/issueLink/10", "", ""}},
			check: func(t *testing.T, out map[string]interface{}) {
				// Pinned: what executeOp renders for an empty 204 today.
				if out["upstream_status"] != float64(204) || out["upstream_body"] != "" || out["operation"] != "jira.link_delete" || out["status"] != nil {
					t.Errorf("204 rendering changed: %v", out)
				}
			}},
		{name: "versions", op: "jira.versions", body: `{"project":"THP"}`,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`[{"id":"1","name":"9.21.0","released":false,"archived":false,"releaseDate":"2026-12-01","self":"http://x","description":"d"}]`))
			},
			want: []exReq{{"GET", "/rest/api/3/project/THP/versions", "", ""}},
			check: func(t *testing.T, out map[string]interface{}) {
				// A bare-array upstream body is rendered as the shaped JSON text in upstream_body.
				b, _ := out["upstream_body"].(string)
				if strings.Contains(b, "self") || strings.Contains(b, "description") || !strings.Contains(b, `"name":"9.21.0"`) || !strings.Contains(b, "releaseDate") {
					t.Errorf("allow_fields not applied: %s", b)
				}
			}},
		{name: "editmeta", op: "jira.editmeta", body: `{"key":"THP-1"}`, handler: ok200,
			want: []exReq{{"GET", "/rest/api/3/issue/THP-1/editmeta", "", ""}}},
	}
	for _, c := range happy {
		handler = c.handler
		take()
		code, out := call(c.op, c.body)
		if code != 200 {
			t.Errorf("%s: HTTP %d: %v", c.name, code, out)
		}
		got := take()
		if len(got) != len(c.want) {
			t.Errorf("%s: upstream saw %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: request %d = %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
		if c.check != nil {
			c.check(t, out)
		}
	}

	denied := []struct{ name, op, body, rule string }{
		{"edit key ABC-1", "jira.edit", `{"key":"ABC-1","fields":{"summary":"x"}}`, "constraint"},
		{"create project id", "jira.create", `{"fields":{"project":{"id":"10100"},"summary":"s"}}`, "constraint"},
		{"create other project", "jira.create", `{"fields":{"project":{"key":"ABC"},"summary":"s"}}`, "constraint"},
		{"create without project", "jira.create", `{"fields":{"summary":"s"}}`, "constraint"},
		{"create update.issuelinks", "jira.create", `{"fields":{"project":{"key":"THP"}},"update":{"issuelinks":[{"add":{}}]}}`, "constraint"},
		{"edit update.issuelinks", "jira.edit", `{"key":"THP-1","update":{"issuelinks":[{"add":{}}]}}`, "constraint"},
		{"edit fields.project", "jira.edit", `{"key":"THP-1","fields":{"project":{"key":"THP"}}}`, "constraint"},
		{"link inward OPS-1", "jira.link", `{"type":"Blocks","inward":"OPS-1","outward":"THP-2"}`, "constraint"},
		{"editmeta ABC-1", "jira.editmeta", `{"key":"ABC-1"}`, "constraint"},
		{"versions OPS", "jira.versions", `{"project":"OPS"}`, "validation"},
		{"update string", "jira.edit", `{"key":"THP-1","update":"x"}`, "validation"},
		{"update null", "jira.edit", `{"key":"THP-1","update":null}`, "validation"},
		{"update array", "jira.edit", `{"key":"THP-1","update":[]}`, "validation"},
	}
	handler = ok200
	for _, c := range denied {
		take()
		code, out := call(c.op, c.body)
		if code != 403 || out["rule"] != c.rule {
			t.Errorf("%s: HTTP %d rule %v, want 403 %s (%v)", c.name, code, out["rule"], c.rule, out)
		}
		if got := take(); len(got) != 0 {
			t.Errorf("%s: denied call reached upstream: %v", c.name, got)
		}
	}
}
