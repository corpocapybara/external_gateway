package policy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
)

// constraintOpsFile writes an ops file with one http-rest op whose YAML lines
// after `path:` are extra, and returns its path.
func constraintOpsFile(t *testing.T, extra string) string {
	t.Helper()
	body := "operations:\n  a.b:\n    connector: http-rest\n    site: s\n    method: GET\n    path: /x\n" + extra
	p := filepath.Join(t.TempDir(), "ops.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConstraintTypeLoadErrors(t *testing.T) {
	const noType = "operation a.b: constraint has no type"
	unknown := func(ty string) string { return `operation a.b: unknown constraint type "` + ty + `"` }

	// Every known type on one op, with the params each needs at load.
	names := make([]string, 0, len(constraintTypes))
	for name := range constraintTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	all := "    params:\n      fields: {type: list, default: [summary]}\n" +
		`    body_template: '{"fields":{{fields}}}'` + "\n    constraints:\n"
	for _, name := range names {
		all += "      - type: " + name + "\n"
		switch name {
		case "jira_fields":
			all += "        allowed: [summary]\n"
		case "jira_key_scope":
			all += "        projects: $workspace.jira_projects\n"
		}
	}

	cases := []struct {
		name, extra string
		want        string // "" = loads
	}{
		{"typo", "    constraints:\n      - type: jql_scop\n", unknown("jql_scop")},
		{"missing type", "    constraints:\n      - projects: [ACME]\n", noType},
		{"null type", "    constraints:\n      - type: null\n", noType},
		{"empty type", "    constraints:\n      - type: \"\"\n", noType},
		{"number type", "    constraints:\n      - type: 7\n", noType},
		{"null entry", "    constraints:\n      - ~\n", noType},
		{"upper case", "    constraints:\n      - type: JQL_SCOPE\n", unknown("JQL_SCOPE")},
		{"trailing space", "    constraints:\n      - type: \"jql_scope \"\n", unknown("jql_scope ")},
		{"scalar entry", "    constraints:\n      - jql_scope\n", "parsing operations file"},
		// The type check runs before checkJiraFields, so a mixed op reports the type.
		{"typo and bad jira_fields", "    constraints:\n      - type: jira_fields\n      - type: jql_scop\n", unknown("jql_scop")},
		{"constraints null", "    constraints: null\n", ""},
		{"constraints empty", "    constraints: []\n", ""},
		{"no constraints key", "", ""},
		{"all known types", all, ""},
	}
	for _, c := range cases {
		_, err := NewRegistry(constraintOpsFile(t, c.extra))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.want)
		}
	}
}

func TestCheckConstraintsUnknownTypeFailsClosed(t *testing.T) {
	r := &OperationRegistry{Operations: map[string]*Operation{}}
	ws := &config.Workspace{}
	check := func(constraint map[string]interface{}) error {
		op := &Operation{ID: "a.b", Constraints: []map[string]interface{}{constraint}}
		return r.CheckConstraints(op, map[string]interface{}{}, ws)
	}

	refusals := []struct {
		name       string
		constraint map[string]interface{}
		want       string
	}{
		{"missing", map[string]interface{}{"projects": "x"}, `unknown constraint type "" on a.b`},
		{"nil", map[string]interface{}{"type": nil}, `unknown constraint type "" on a.b`},
		{"empty", map[string]interface{}{"type": ""}, `unknown constraint type "" on a.b`},
		{"typo", map[string]interface{}{"type": "jql_scop"}, `unknown constraint type "jql_scop" on a.b`},
	}
	for _, c := range refusals {
		err := check(c.constraint)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: error = %v, want %q", c.name, err, c.want)
		}
	}

	noEffect := 0
	for name, kind := range constraintTypes {
		if kind != constraintNoEffect {
			continue
		}
		noEffect++
		if err := check(map[string]interface{}{"type": name}); err != nil {
			t.Errorf("%s: no-effect type refused: %v", name, err)
		}
	}
	if noEffect != 5 {
		t.Errorf("checked %d no-effect types, want 5", noEffect)
	}

	// A type listed as checked but missing its case in CheckConstraints fails closed.
	constraintTypes["test_checked_no_case"] = constraintChecked
	defer delete(constraintTypes, "test_checked_no_case")
	err := check(map[string]interface{}{"type": "test_checked_no_case"})
	if want := `constraint type "test_checked_no_case" on a.b has no check`; err == nil || err.Error() != want {
		t.Errorf("checked type without a case: error = %v, want %q", err, want)
	}
}

func TestExampleOpsLoad(t *testing.T) {
	files, err := filepath.Glob("../../config.example/operations*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no example ops files found (err %v)", err)
	}
	for _, f := range files {
		if _, err := NewRegistry(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	// The check bites on a real file: one jql_scope typo is refused.
	data, err := os.ReadFile("../../config.example/operations.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(data), "type: jql_scope", "type: jql_scop", 1)
	if bad == string(data) {
		t.Fatal("config.example/operations.yaml has no `type: jql_scope` to break")
	}
	p := filepath.Join(t.TempDir(), "operations.yaml")
	if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(p); err == nil || !strings.Contains(err.Error(), `unknown constraint type "jql_scop"`) {
		t.Fatalf("broken copy: error = %v, want unknown constraint type", err)
	}
}

// A jql_scope whose projects is present but is neither a list nor a known
// $workspace source used to fall back to the workspace's jira_projects and, with
// none configured, scope nothing (EG0022). It is refused at load and in
// CheckConstraints, also when the workspace has no jira_projects.
func TestJQLScopeBadProjectsRefused(t *testing.T) {
	const loadWant = "operation a.b: jql_scope projects must be a list or one of " +
		"$workspace.bitbucket_repos, $workspace.jira_projects, $workspace.pg_allowed_tables"
	const checkWant = "jql_scope on a.b: projects must be a list or one of " +
		"$workspace.bitbucket_repos, $workspace.jira_projects, $workspace.pg_allowed_tables"
	rows := []struct {
		name, yaml string
		value      interface{}
	}{
		{"number", "7", 7},
		{"map", "{a: b}", map[string]interface{}{"a": "b"}},
		{"non-list non-workspace string", "DESK", "DESK"},
		{"$workspace.nope", "$workspace.nope", "$workspace.nope"},
		{"null", "null", nil},
	}
	r := &OperationRegistry{Operations: map[string]*Operation{}}
	for _, row := range rows {
		_, err := NewRegistry(constraintOpsFile(t, "    constraints:\n      - type: jql_scope\n        projects: "+row.yaml+"\n"))
		if err == nil || !strings.Contains(err.Error(), loadWant) {
			t.Errorf("%s: load error = %v, want it to contain %q", row.name, err, loadWant)
		}

		op := &Operation{ID: "a.b", Constraints: []map[string]interface{}{{"type": "jql_scope", "projects": row.value}}}
		for _, ws := range []*config.Workspace{
			{}, // empty JiraProjects: the old fallback scoped nothing
			{Policy: config.WorkspacePolicy{JiraProjects: []string{"DESK"}}},
		} {
			err := r.CheckConstraints(op, map[string]interface{}{"jql": "summary ~ x"}, ws)
			if err == nil || err.Error() != checkWant {
				t.Errorf("%s (jira_projects %v): CheckConstraints error = %v, want %q", row.name, ws.Policy.JiraProjects, err, checkWant)
			}
		}
	}

	// Valid shapes, and no projects key at all, still load.
	for _, v := range []string{
		"", "        projects: [DESK]\n", "        projects: $workspace.jira_projects\n",
		"        projects: $workspace.bitbucket_repos\n", "        projects: $workspace.pg_allowed_tables\n",
	} {
		if _, err := NewRegistry(constraintOpsFile(t, "    constraints:\n      - type: jql_scope\n"+v)); err != nil {
			t.Errorf("projects %q: unexpected load error: %v", v, err)
		}
	}
	// Each $workspace source scopes by its own policy list.
	ws := &config.Workspace{Policy: config.WorkspacePolicy{
		JiraProjects: []string{"J"}, BitbucketRepos: []string{"B"}, PgAllowedTables: []string{"T"},
	}}
	for source, want := range map[string]string{
		"$workspace.jira_projects": "J", "$workspace.bitbucket_repos": "B", "$workspace.pg_allowed_tables": "T",
	} {
		op := &Operation{ID: "a.b", Constraints: []map[string]interface{}{{"type": "jql_scope", "projects": source}}}
		p := map[string]interface{}{"jql": "summary ~ x"}
		if err := r.CheckConstraints(op, p, ws); err != nil || p["jql"] != "(summary ~ x) AND project in ("+want+")" {
			t.Errorf("%s: err %v, jql %q, want project in (%s)", source, err, p["jql"], want)
		}
	}
	// No projects key keeps the workspace fallback in CheckConstraints.
	op := &Operation{ID: "a.b", Constraints: []map[string]interface{}{{"type": "jql_scope"}}}
	p := map[string]interface{}{"jql": "summary ~ x"}
	if err := r.CheckConstraints(op, p, ws); err != nil || p["jql"] != "(summary ~ x) AND project in (J)" {
		t.Errorf("no projects key: err %v, jql %q, want the workspace scope", err, p["jql"])
	}
}
