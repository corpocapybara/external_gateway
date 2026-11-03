package server

import (
	"net/http"
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
		op, wantRule, wantSuggest string
		status                    int
	}{
		{"jira.get", "", "jira.issue", http.StatusNotFound},
		{"zzz.nothing_alike", "", "", http.StatusNotFound},
		{"jira.get_secre", "", "jira.search", http.StatusNotFound}, // nearest is ungranted jira.get_secret: must not leak
		{"jira." + strings.Repeat("x", 200), "", "", http.StatusNotFound},
		{"jira.get_secret", "grant", "", http.StatusForbidden},
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
			if c.wantSuggest == "" && has {
				t.Fatalf("%s: unexpected suggestion %v", c.op, got)
			}
			if c.wantSuggest != "" && got != c.wantSuggest {
				t.Fatalf("%s: did_you_mean = %v, want %s", c.op, got, c.wantSuggest)
			}
		}
	}
}
