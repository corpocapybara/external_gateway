package server

import (
	"net/http"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// A guessed parameter name used to come back as a bare "unknown parameter: fields",
// so the agent guessed again (2026-W44: 20+ such retries in thp sessions). The deny
// now names the operation's parameters, so one retry fixes the call.
func TestValidationDenyNamesOperationParams(t *testing.T) {
	f := newEnvFixture(t)
	f.sites["gitlab"] = config.Site{Environments: map[string]config.Environment{"prod": {BaseURL: f.prodURL}}}
	f.ops["jira.search"] = &policy.Operation{ID: "jira.search", Connector: "http-rest", Site: "jira", Method: "POST", Path: "/rest/api/3/search/jql",
		Params: map[string]policy.ParamDef{
			"jql":           {Type: "string", Required: true},
			"maxResults":    {Type: "int", Max: 100},
			"nextPageToken": {Type: "string"},
			"env":           {Type: "string"},
		}}
	f.ops["gitlab.mr"] = &policy.Operation{ID: "gitlab.mr", Connector: "http-rest", Site: "gitlab", Method: "GET", Path: "/projects/{project_id}/merge_requests/{mr_iid}",
		Params: map[string]policy.ParamDef{
			"project_id": {Type: "string", Required: true},
			"mr_iid":     {Type: "int", Required: true},
		}}
	f.ops["gitlab.projects"] = &policy.Operation{ID: "gitlab.projects", Connector: "http-rest", Site: "gitlab", Method: "GET", Path: "/projects",
		Params: map[string]policy.ParamDef{"search": {Type: "string"}}}
	f.ops["jira.myself"] = &policy.Operation{ID: "jira.myself", Connector: "http-rest", Site: "jira", Method: "GET", Path: "/rest/api/3/myself"}

	cases := []struct {
		op, body, want string
	}{
		{"jira.search", `{"jql":"project = THP","fields":["summary"]}`,
			"unknown parameter: fields (valid: env, jql, maxResults, nextPageToken; required: jql)"},
		{"gitlab.mr", `{"mr_iid":7}`,
			"missing required parameter: project_id (valid: env, mr_iid, project_id; required: mr_iid, project_id)"},
		// Several missing: always the same one named, not whichever the map yields.
		{"gitlab.mr", `{}`,
			"missing required parameter: mr_iid (valid: env, mr_iid, project_id; required: mr_iid, project_id)"},
		{"gitlab.projects", `{"per_page":100}`,
			"unknown parameter: per_page (valid: env, search)"},
		{"jira.myself", `{"max_results":5}`,
			"unknown parameter: max_results (valid: env)"},
	}
	for _, c := range cases {
		for i := 0; i < 20; i++ {
			dryRun := i%2 == 1
			code, out := f.send(t, c.op, c.body, dryRun)
			if code != http.StatusForbidden || out["error"] != "denied" || out["rule"] != "validation" {
				t.Fatalf("%s %s (dry run %v): status %d, body %v; want a validation deny", c.op, c.body, dryRun, code, out)
			}
			if out["detail"] != c.want {
				t.Fatalf("%s %s (dry run %v): detail = %q, want %q", c.op, c.body, dryRun, out["detail"], c.want)
			}
		}
	}
	if n := f.prod.Load() + f.vpn.Load(); n != 0 {
		t.Errorf("upstream called %d times for refused requests", n)
	}
}
