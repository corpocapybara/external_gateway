package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
)

func jiraFieldsOp() *Operation {
	return &Operation{
		ID:     "jira.search",
		Params: map[string]ParamDef{"jql": {Type: "string"}, "fields": {Type: "list"}},
		Constraints: []map[string]interface{}{
			{"type": "jira_fields", "allowed": []interface{}{"summary", "customfield_10333"}},
		},
	}
}

func TestJiraFieldsConstraint(t *testing.T) {
	const shape = "parameter fields must be a non-empty JSON array of field names (allowed: summary, customfield_10333)"
	item := func(f string) string { return `field "` + f + `" is not allowed (allowed: summary, customfield_10333)` }
	cases := []struct {
		name, fields string // fields is the JSON value, "" = absent
		want         string // "" = allowed
	}{
		{"absent", "", ""},
		{"allowed subset", `["summary","customfield_10333"]`, ""},
		{"duplicates", `["summary","summary"]`, ""},
		{"null", `null`, shape},
		{"string", `"summary"`, shape},
		{"object", `{"summary":true}`, shape},
		{"empty", `[]`, shape},
		{"number item", `[1]`, shape},
		{"mixed", `["summary",1]`, shape},
		{"nested", `[["summary"]]`, shape},
		{"star all", `["*all"]`, item("*all")},
		{"star navigable", `["*navigable"]`, item("*navigable")},
		{"minus field", `["-summary"]`, item("-summary")},
		{"unknown custom field", `["summary","customfield_99999"]`, item("customfield_99999")},
	}
	r := &OperationRegistry{Operations: map[string]*Operation{}}
	ws := &config.Workspace{}
	for _, c := range cases {
		params := map[string]interface{}{"jql": "project = THP"}
		if c.fields != "" {
			var v interface{}
			if err := json.Unmarshal([]byte(c.fields), &v); err != nil {
				t.Fatalf("%s: bad fixture: %v", c.name, err)
			}
			params["fields"] = v
		}
		err := r.CheckConstraints(jiraFieldsOp(), params, ws)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: error = %q, want %q", c.name, got, c.want)
		}
	}
}

// An op built in code with a malformed allowlist refuses rather than passing anything.
func TestJiraFieldsConstraintFailsClosed(t *testing.T) {
	op := jiraFieldsOp()
	op.Constraints[0]["allowed"] = []interface{}{"summary", 7}
	r := &OperationRegistry{Operations: map[string]*Operation{}}
	err := r.CheckConstraints(op, map[string]interface{}{"fields": []interface{}{"summary"}}, &config.Workspace{})
	if err == nil || err.Error() != "jira_fields on jira.search lists no allowed fields" {
		t.Fatalf("error = %v, want the fail-closed refusal", err)
	}
}

func TestJiraFieldsConstraintLoadErrors(t *testing.T) {
	const head = "operations:\n  jira.search:\n    connector: http-rest\n    site: jira\n    method: POST\n    path: /rest/api/3/search/jql\n"
	param := func(extra string) string {
		return "    params:\n      jql:\n        type: string\n      fields:\n        type: list\n" + extra
	}
	constraint := func(allowed string) string {
		return "    constraints:\n      - type: jira_fields\n" + allowed
	}
	okAllowed := "        allowed: [summary, status]\n"
	cases := []struct {
		name, yaml, want string // want "" = loads
	}{
		{"valid with default", head + param("        default: [summary]\n") + constraint(okAllowed), ""},
		{"valid without default", head + param("") + constraint(okAllowed), ""},
		{"null default is unset", head + param("        default: null\n") + constraint(okAllowed), ""},
		{"allowed missing", head + param("") + constraint(""), "jira_fields needs allowed"},
		{"allowed empty", head + param("") + constraint("        allowed: []\n"), "jira_fields needs allowed"},
		{"allowed not a list", head + param("") + constraint("        allowed: summary\n"), "jira_fields needs allowed"},
		{"allowed non-string item", head + param("") + constraint("        allowed: [summary, [x]]\n"), "jira_fields needs allowed"},
		{"fields undeclared", head + "    params:\n      jql:\n        type: string\n" + constraint(okAllowed), "needs a param fields of type list"},
		{"fields wrong type", head + "    params:\n      fields:\n        type: string\n" + constraint(okAllowed), "needs a param fields of type list"},
		{"default not a list", head + param("        default: summary\n") + constraint(okAllowed), "fields default to be a non-empty list"},
		{"default empty", head + param("        default: []\n") + constraint(okAllowed), "fields default to be a non-empty list"},
		{"default non-string item", head + param("        default: [summary, 3]\n") + constraint(okAllowed), "fields default to be a non-empty list"},
		{"default outside allowed", head + param("        default: [summary, labels]\n") + constraint(okAllowed), `default field "labels" is not in allowed`},
	}
	dir := t.TempDir()
	for i, c := range cases {
		path := filepath.Join(dir, "ops"+string(rune('a'+i))+".yaml")
		if err := os.WriteFile(path, []byte(c.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewRegistry(path)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: loaded, want an error containing %q", c.name, c.want)
			continue
		}
		if msg := err.Error(); !strings.Contains(msg, "operation jira.search: jira_fields") || !strings.Contains(msg, c.want) {
			t.Errorf("%s: error %q, want it to contain %q and %q", c.name, msg, "operation jira.search: jira_fields", c.want)
		}
	}
}

func TestExampleJiraSearchFields(t *testing.T) {
	for _, file := range []string{"operations.yaml", "operations.globex.yaml"} {
		r, err := NewRegistry(filepath.Join("..", "..", "config.example", file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		op := r.Operations["jira.search"]
		if op == nil {
			t.Fatalf("%s: no jira.search", file)
		}
		if op.Params["fields"].Type != "list" {
			t.Errorf("%s: fields param type = %q, want list", file, op.Params["fields"].Type)
		}
		for _, ph := range []string{"{{jql}}", "{{maxResults}}", "{{fields}}"} {
			if !strings.Contains(op.BodyTemplate, ph) {
				t.Errorf("%s: body_template lacks %s", file, ph)
			}
		}
		var allowed []string
		for _, c := range op.Constraints {
			if c["type"] == "jira_fields" {
				allowed, _ = stringList(c["allowed"])
			}
		}
		if len(allowed) == 0 {
			t.Fatalf("%s: no jira_fields constraint with allowed fields", file)
		}
		for _, f := range allowed {
			covered := false
			for _, af := range op.Response.AllowFields {
				if af == "issues[].fields."+f || strings.HasPrefix(af, "issues[].fields."+f+".") || strings.HasPrefix(af, "issues[].fields."+f+"[]") {
					covered = true
				}
			}
			if !covered {
				t.Errorf("%s: allowed field %s has no allow_fields entry", file, f)
			}
		}
	}
}
