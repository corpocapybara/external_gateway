package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// jira.search used to fetch a fixed field set and refuse a `fields` param
// (T0003). With a jira_fields constraint the caller picks from an allowlist;
// allow_fields still shapes the reply.
func TestJiraSearchFields(t *testing.T) {
	f := newEnvFixture(t)
	var hits atomic.Int32
	var mu sync.Mutex
	var bodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.Write([]byte(`{"issues":[` +
			`{"key":"THP-1","fields":{"summary":"one","customfield_10333":"notes 1","secret":"s1"}},` +
			`{"key":"THP-2","fields":{"summary":"two","customfield_10333":"notes 2","secret":"s2"}}]}`))
	}))
	t.Cleanup(upstream.Close)
	// One environment, so no env param is needed.
	f.sites["jira"] = config.Site{Environments: map[string]config.Environment{"prod": {BaseURL: upstream.URL}}}

	newOp := func(id string, constrained bool) *policy.Operation {
		op := &policy.Operation{ID: id, Connector: "http-rest", Site: "jira", Method: "POST", Path: "/rest/api/3/search/jql",
			Params: map[string]policy.ParamDef{
				"jql":    {Type: "string", Required: true},
				"fields": {Type: "list", Default: []interface{}{"summary"}},
			},
			BodyTemplate: `{"jql":"{{jql}}","fields":{{fields}}}`,
			Response: policy.ResponsePolicy{AllowFields: []string{
				"issues[].key", "issues[].fields.summary", "issues[].fields.customfield_10333"}},
		}
		if constrained {
			op.Constraints = []map[string]interface{}{
				{"type": "jira_fields", "allowed": []interface{}{"summary", "customfield_10333"}},
			}
		}
		return op
	}
	f.ops["jira.search"] = newOp("jira.search", true)
	f.ops["jira.search_open"] = newOp("jira.search_open", false)

	lastFields := func() interface{} {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		var body map[string]interface{}
		if err := json.Unmarshal(bodies[len(bodies)-1], &body); err != nil {
			t.Fatalf("upstream body is not JSON: %v (%s)", err, bodies[len(bodies)-1])
		}
		return body["fields"]
	}

	// Asked-for fields go upstream as that array; the reply keeps both issues, shaped.
	code, out := f.send(t, "jira.search", `{"jql":"project = THP","fields":["summary","customfield_10333"]}`, false)
	if code != http.StatusOK || out["status"] == "error" {
		t.Fatalf("allowed fields: status %d, body %v", code, out)
	}
	if got, want := lastFields(), []interface{}{"summary", "customfield_10333"}; !reflect.DeepEqual(got, want) {
		t.Errorf("upstream fields = %v, want %v", got, want)
	}
	issues, _ := out["issues"].([]interface{})
	if len(issues) != 2 {
		t.Fatalf("reply issues = %v, want 2", out["issues"])
	}
	for _, it := range issues {
		fields := it.(map[string]interface{})["fields"].(map[string]interface{})
		if fields["summary"] == nil || fields["customfield_10333"] == nil {
			t.Errorf("issue %v lost an allowed field", it)
		}
		if _, leaked := fields["secret"]; leaked {
			t.Errorf("issue %v carries the unlisted field secret", it)
		}
	}

	// No fields: the op default goes upstream.
	if code, out := f.send(t, "jira.search", `{"jql":"project = THP"}`, false); code != http.StatusOK || out["status"] == "error" {
		t.Fatalf("default fields: status %d, body %v", code, out)
	}
	if got, want := lastFields(), []interface{}{"summary"}; !reflect.DeepEqual(got, want) {
		t.Errorf("upstream fields without a fields param = %v, want %v", got, want)
	}

	// A field outside the allowlist is refused on both paths, before any upstream call.
	before := hits.Load()
	const detail = `field "*all" is not allowed (allowed: summary, customfield_10333)`
	for _, dryRun := range []bool{true, false} {
		code, out := f.send(t, "jira.search", `{"jql":"project = THP","fields":["*all"]}`, dryRun)
		if code != http.StatusForbidden || out["rule"] != "constraint" || out["detail"] != detail {
			t.Errorf("*all (dry run %v): status %d, body %v; want 403 constraint %q", dryRun, code, out, detail)
		}
	}
	if n := hits.Load() - before; n != 0 {
		t.Errorf("refused requests reached upstream %d times", n)
	}

	// Control: the same op without the constraint passes *all through.
	if code, out := f.send(t, "jira.search_open", `{"jql":"project = THP","fields":["*all"]}`, false); code != http.StatusOK || out["status"] == "error" {
		t.Fatalf("control op: status %d, body %v", code, out)
	}
	if got, want := lastFields(), []interface{}{"*all"}; !reflect.DeepEqual(got, want) {
		t.Errorf("control op upstream fields = %v, want %v", got, want)
	}
}
