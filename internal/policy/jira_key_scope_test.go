package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
)

func jiraScopeOp() *Operation {
	return &Operation{
		ID: "jira.create",
		Params: map[string]ParamDef{
			"key":    {Type: "string"},
			"inward": {Type: "string"},
			"fields": {Type: "object"},
			"update": {Type: "object"},
		},
		Constraints: []map[string]interface{}{{
			"type":         "jira_key_scope",
			"projects":     "$workspace.jira_projects",
			"issue_keys":   []interface{}{"key", "inward", "fields.parent.key"},
			"project_keys": []interface{}{"fields.project.key"},
			"required":     []interface{}{"fields.project.key"},
			"absent":       []interface{}{"fields.issuelinks", "update.issuelinks", "update.parent", "update.project"},
		}},
	}
}

func TestJiraScope(t *testing.T) {
	scoped := &config.Workspace{Policy: config.WorkspacePolicy{JiraProjects: []string{"THP"}}}
	open := &config.Workspace{}
	proj := `"fields":{"project":{"key":"THP"}}`
	cases := []struct {
		name, params string
		ws           *config.Workspace
		want         string // substring of the error; "" = allowed
	}{
		{"in-scope key", `{"key":"THP-1",` + proj + `}`, scoped, ""},
		{"other project", `{"key":"ABC-1",` + proj + `}`, scoped, "path key"},
		{"lowercase", `{"key":"abc-1",` + proj + `}`, scoped, "path key"},
		{"leading space", `{"key":" THP-1",` + proj + `}`, scoped, "path key"},
		{"trailing space", `{"key":"THP-1 ",` + proj + `}`, scoped, "path key"},
		{"trailing newline", `{"key":"THP-1\n",` + proj + `}`, scoped, "path key"},
		{"traversal", `{"key":"THP-1/../x",` + proj + `}`, scoped, "path key"},
		{"full-width", `{"key":"ＴＨＰ-１",` + proj + `}`, scoped, "path key"},
		{"number", `{"key":5,` + proj + `}`, scoped, "path key"},
		{"null", `{"key":null,` + proj + `}`, scoped, "path key"},
		{"object", `{"key":{"a":1},` + proj + `}`, scoped, "path key"},
		{"array", `{"key":["THP-1"],` + proj + `}`, scoped, "path key"},
		{"bool", `{"key":true,` + proj + `}`, scoped, "path key"},
		{"empty string", `{"key":"",` + proj + `}`, scoped, "path key"},
		{"absent issue key passes", `{` + proj + `}`, scoped, ""},
		{"second issue path", `{"inward":"ABC-2",` + proj + `}`, scoped, "path inward"},
		{"parent in scope", `{"fields":{"project":{"key":"THP"},"parent":{"key":"THP-9"}}}`, scoped, ""},
		{"parent out of scope", `{"fields":{"project":{"key":"THP"},"parent":{"key":"ABC-9"}}}`, scoped, "path fields.parent.key"},
		{"parent by id", `{"fields":{"project":{"key":"THP"},"parent":{"id":"1"}}}`, scoped, "path fields.parent"},
		{"project pinned", `{"fields":{"project":{"key":"THP"}}}`, scoped, ""},
		{"project other", `{"fields":{"project":{"key":"ABC"}}}`, scoped, "path fields.project.key"},
		{"project by id", `{"fields":{"project":{"id":10100}}}`, scoped, "path fields.project"},
		{"project key and id", `{"fields":{"project":{"key":"THP","id":1}}}`, scoped, "path fields.project"},
		{"project holder empty", `{"fields":{"project":{}}}`, scoped, "path fields.project"},
		{"project by name", `{"fields":{"project":{"name":"THP"}}}`, scoped, "path fields.project"},
		{"project string", `{"fields":{"project":"THP"}}`, scoped, "path fields.project"},
		{"project null", `{"fields":{"project":null}}`, scoped, "path fields.project"},
		{"project array", `{"fields":{"project":[{"key":"THP"}]}}`, scoped, "path fields.project"},
		{"project key array", `{"fields":{"project":{"key":["THP"]}}}`, scoped, "path fields.project.key"},
		{"project key bool", `{"fields":{"project":{"key":true}}}`, scoped, "path fields.project.key"},
		{"project key empty", `{"fields":{"project":{"key":""}}}`, scoped, "path fields.project.key"},
		{"missing required project", `{"fields":{"summary":"x"}}`, scoped, "missing required path fields.project.key"},
		{"update issuelinks denied", `{` + proj + `,"update":{"issuelinks":[{"add":{}}]}}`, scoped, "path update.issuelinks is not allowed"},
		{"update fixVersions passes", `{` + proj + `,"update":{"fixVersions":[{"add":{"name":"1"}}]}}`, scoped, ""},
		{"update omitted passes", `{` + proj + `}`, scoped, ""},
		{"fields issuelinks denied", `{"fields":{"project":{"key":"THP"},"issuelinks":[]}}`, scoped, "path fields.issuelinks is not allowed"},
		{"update parent null denied", `{` + proj + `,"update":{"parent":null}}`, scoped, "path update.parent is not allowed"},
		{"absent through non-object", `{` + proj + `,"update":"x"}`, scoped, "path update.issuelinks: update is not an object"},
		{"required through non-object", `{"fields":"x"}`, scoped, "fields is not an object"},
		{"no jira_projects: any key", `{"key":"ABC-1",` + proj + `}`, open, ""},
		{"no jira_projects: any project", `{"fields":{"project":{"key":"ABC"}}}`, open, ""},
		{"no jira_projects: id still denied", `{"fields":{"project":{"id":1}}}`, open, "path fields.project"},
		{"no jira_projects: shape still denied", `{"key":"abc-1",` + proj + `}`, open, "path key"},
		{"no jira_projects: absent still denied", `{` + proj + `,"update":{"issuelinks":[]}}`, open, "path update.issuelinks"},
	}
	r := &OperationRegistry{Operations: map[string]*Operation{}}
	for _, c := range cases {
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(c.params), &params); err != nil {
			t.Fatalf("%s: bad fixture: %v", c.name, err)
		}
		err := r.CheckConstraints(jiraScopeOp(), params, c.ws)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: error = %q, want containing %q", c.name, got, c.want)
		}
	}
}

// A literal projects list pins the scope, whatever the workspace says.
func TestJiraScopeLiteralProjects(t *testing.T) {
	op := jiraScopeOp()
	op.Constraints[0]["projects"] = []interface{}{"SUP"}
	r := &OperationRegistry{Operations: map[string]*Operation{}}
	ws := &config.Workspace{Policy: config.WorkspacePolicy{JiraProjects: []string{"THP"}}}
	if err := r.CheckConstraints(op, map[string]interface{}{"key": "SUP-1", "fields": map[string]interface{}{"project": map[string]interface{}{"key": "SUP"}}}, ws); err != nil {
		t.Errorf("SUP-1 with literal [SUP]: %v", err)
	}
	if err := r.CheckConstraints(op, map[string]interface{}{"key": "THP-1"}, ws); err == nil {
		t.Error("THP-1 with literal [SUP] was allowed")
	}
	// Malformed config fails closed at check time.
	op.Constraints[0]["issue_keys"] = "key"
	if err := r.CheckConstraints(op, map[string]interface{}{}, ws); err == nil {
		t.Error("malformed issue_keys was allowed")
	}
}

func TestJiraScopeLoad(t *testing.T) {
	const scope = "constraints: [{type: jira_key_scope, projects: [THP], issue_keys: [key]}]"
	const pscope = "constraints: [{type: jira_key_scope, projects: [THP]}]"
	const idParam = "id: {type: string, required: true, pattern: '^[0-9]+$'}"
	cases := []struct{ name, op, want string }{
		{"valid", "params: {key: {type: string}}\n    " + scope, ""},
		{"valid precheck", "params: {" + idParam + "}\n    precheck: jira_issue_link\n    constraints: [{type: jira_key_scope, projects: $workspace.jira_projects}]", ""},
		{"projects missing", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, issue_keys: [key]}]", "projects"},
		{"empty literal list", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, projects: [], issue_keys: [key]}]", "projects"},
		{"unknown workspace field", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, projects: $workspace.nope, issue_keys: [key]}]", "projects"},
		{"unknown first segment", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, projects: [THP], issue_keys: [other.key]}]", "not a declared param"},
		{"required not listed", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, projects: [THP], issue_keys: [key], required: [key.x]}]", "required"},
		{"empty segment", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, projects: [THP], issue_keys: ['key..x']}]", "empty segment"},
		{"array index", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, projects: [THP], issue_keys: ['key[0]']}]", "array index"},
		{"absent also listed", "params: {key: {type: string}}\n    constraints: [{type: jira_key_scope, projects: [THP], issue_keys: [key], absent: [key]}]", "absent"},
		{"non-empty default", "params: {key: {type: object, default: {a: 1}}}\n    " + scope, "default"},
		{"unknown precheck", "params: {key: {type: string}}\n    precheck: nope", "unknown precheck"},
		{"precheck no id", "params: {key: {type: string}}\n    precheck: jira_issue_link\n    " + scope, "id param"},
		{"precheck id no pattern", "params: {id: {type: string}}\n    precheck: jira_issue_link\n    " + pscope, "id param"},
		{"precheck id not required", "params: {id: {type: string, pattern: '^[0-9]+$'}}\n    precheck: jira_issue_link\n    " + pscope, "id param"},
		{"precheck id not string", "params: {id: {type: list, required: true, pattern: '^[0-9]+$'}}\n    precheck: jira_issue_link\n    " + pscope, "id param"},
		{"precheck no scope", "params: {" + idParam + "}\n    precheck: jira_issue_link", "jira_key_scope"},
		{"precheck wrong connector", "params: {" + idParam + "}\n    precheck: jira_issue_link\n    " + pscope, "http-rest"},
		{"precheck multipart", "params: {" + idParam + ", f: {type: string}, c: {type: string}}\n    multipart: {field: file, filename_param: f, content_param: c}\n    precheck: jira_issue_link\n    " + pscope, "multipart"},
	}
	dir := t.TempDir()
	for _, c := range cases {
		path := filepath.Join(dir, "ops.yaml")
		connector := "http-rest"
		if c.name == "precheck wrong connector" {
			connector = "postgres"
		}
		yml := "operations:\n  jira.x:\n    connector: " + connector + "\n    site: jira\n    method: GET\n    path: /x\n    " + c.op + "\n"
		if err := os.WriteFile(path, []byte(yml), 0600); err != nil {
			t.Fatal(err)
		}
		r := &OperationRegistry{Operations: map[string]*Operation{}}
		err := r.loadFromFile(path)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected load error: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), "jira.x") || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: error = %v, want one naming jira.x and containing %q", c.name, err, c.want)
		}
	}
}
