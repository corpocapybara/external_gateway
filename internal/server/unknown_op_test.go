package server

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// `jira.get` (no such op; the real one is `jira.issue`) used to answer
// 403 "denied", which reads as a permission problem. A missing op is a 404
// that names the closest op the agent may call, and nothing it may not call.
func TestUnknownOpIsNotFoundWithSuggestion(t *testing.T) {
	f := newEnvFixture(t)
	f.ops["jira.issue"] = &policy.Operation{ID: "jira.issue", Connector: "http-rest", Site: "jira", Method: "GET", Path: "/rest/api/3/issue/{key}"}
	f.ops["jira.search"] = &policy.Operation{ID: "jira.search", Connector: "http-rest", Site: "jira", Method: "GET", Path: "/rest/api/3/search"}
	f.ops["jira.get_secret"] = &policy.Operation{ID: "jira.get_secret", Connector: "http-rest", Site: "jira", Method: "GET", Path: "/x"}
	f.send(t, "jira.issue", `{}`, true) // builds f.srv with the grants
	// jira.get_secret exists but is not granted: it is closest to "jira.get", yet must not leak.
	cfg := f.srv.cfg()
	var granted []string
	for _, id := range cfg.Roles["dev"].Operations {
		if id != "jira.get_secret" {
			granted = append(granted, id)
		}
	}
	cfg.Roles["dev"] = config.Role{Operations: granted}

	cases := []struct {
		op, wantRule string
		wantSuggest  []string
		status       int
	}{
		{"jira.get", "", []string{"jira.issue", "jira.search"}, http.StatusNotFound},
		{"zzz.nothing_alike", "", nil, http.StatusNotFound},
		{"jira.get_secre", "", []string{"jira.search", "jira.issue"}, http.StatusNotFound}, // nearest is ungranted jira.get_secret: must not leak
		{"jira." + strings.Repeat("x", 200), "", nil, http.StatusNotFound},
		{"jira.get_secret", "grant", nil, http.StatusForbidden},
	}
	for _, c := range cases {
		for _, dryRun := range []bool{false, true} {
			code, out := f.send(t, c.op, `{}`, dryRun)
			if code != c.status {
				t.Fatalf("%s (dry run %v): status %d, body %v; want %d", c.op, dryRun, code, out, c.status)
			}
			if c.status == http.StatusForbidden {
				if out["error"] != "denied" || out["rule"] != c.wantRule {
					t.Fatalf("%s: body %v; want a %s deny", c.op, out, c.wantRule)
				}
				continue
			}
			if out["error"] != "not_found" || out["detail"] != "operation not found" || out["op"] != c.op {
				t.Fatalf("%s (dry run %v): body %v", c.op, dryRun, out)
			}
			got, has := out["did_you_mean"]
			if c.wantSuggest == nil && has {
				t.Fatalf("%s: unexpected suggestion %v", c.op, got)
			}
			if c.wantSuggest != nil {
				var ids []string
				arr, _ := got.([]interface{})
				for _, v := range arr {
					ids = append(ids, v.(string))
				}
				if !reflect.DeepEqual(ids, c.wantSuggest) {
					t.Fatalf("%s: did_you_mean = %v, want %v", c.op, got, c.wantSuggest)
				}
			}
		}
	}
}

// More than three granted ops qualify: only the best three come back, ordered
// by edit distance, then closer length, then name.
func TestUnknownOpSuggestionsCappedAtThree(t *testing.T) {
	f := newEnvFixture(t)
	for _, id := range []string{"jira.gets", "jira.getx", "jira.geta", "jira.getzz", "jira.getzzz"} {
		f.ops[id] = &policy.Operation{ID: id, Connector: "http-rest", Site: "jira", Method: "GET", Path: "/x"}
	}
	f.send(t, "jira.gets", `{}`, true) // builds f.srv with the grants
	code, out := f.send(t, "jira.get", `{}`, false)
	if code != http.StatusNotFound {
		t.Fatalf("status %d, body %v", code, out)
	}
	arr, _ := out["did_you_mean"].([]interface{})
	var got []string
	for _, v := range arr {
		got = append(got, v.(string))
	}
	want := []string{"jira.geta", "jira.gets", "jira.getx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("did_you_mean = %v, want %v", got, want)
	}
}

// An op granted through two roles is suggested once.
func TestUnknownOpSuggestionsDedupeAcrossRoles(t *testing.T) {
	f := newEnvFixture(t)
	for _, id := range []string{"jira.gets", "jira.getx"} {
		f.ops[id] = &policy.Operation{ID: id, Connector: "http-rest", Site: "jira", Method: "GET", Path: "/x"}
	}
	f.send(t, "jira.gets", `{}`, true) // builds f.srv with the grants
	cfg := f.srv.cfg()
	cfg.Roles["dev2"] = config.Role{Operations: []string{"jira.gets"}}
	cfg.Agents[0].Grants = append(cfg.Agents[0].Grants, config.Grant{Workspace: "thp", Role: "dev2"})
	_, out := f.send(t, "jira.get", `{}`, false)
	arr, _ := out["did_you_mean"].([]interface{})
	var got []string
	for _, v := range arr {
		got = append(got, v.(string))
	}
	want := []string{"jira.gets", "jira.getx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("did_you_mean = %v, want %v", got, want)
	}
}

// At equal edit distance the candidate whose length is closer to the request
// comes first, even though the other sorts first by name.
func TestUnknownOpSuggestionsTieBreakOnLength(t *testing.T) {
	f := newEnvFixture(t)
	for _, id := range []string{"jira.getzz", "jira.gxx"} { // both distance 2 from jira.get
		f.ops[id] = &policy.Operation{ID: id, Connector: "http-rest", Site: "jira", Method: "GET", Path: "/x"}
	}
	f.send(t, "jira.gxx", `{}`, true) // builds f.srv with the grants
	_, out := f.send(t, "jira.get", `{}`, false)
	arr, _ := out["did_you_mean"].([]interface{})
	var got []string
	for _, v := range arr {
		got = append(got, v.(string))
	}
	want := []string{"jira.gxx", "jira.getzz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("did_you_mean = %v, want %v", got, want)
	}
}
