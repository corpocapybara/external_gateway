package policy

import (
	"testing"

	"github.com/external_gateway/internal/config"
)

func ws(p config.WorkspacePolicy) *config.Workspace {
	return &config.Workspace{Policy: p}
}

func TestValidateParamsRejectsUnknown(t *testing.T) {
	r := &OperationRegistry{}
	op := &Operation{Params: map[string]ParamDef{"a": {Type: "string"}}}
	err := r.ValidateParams(op, map[string]interface{}{"a": "x", "sudo": "root"}, ws(config.WorkspacePolicy{}))
	if err == nil {
		t.Fatal("expected unknown parameter to be rejected")
	}
	// env/cluster are always permitted even if not declared.
	if err := r.ValidateParams(op, map[string]interface{}{"a": "x", "env": "prod"}, ws(config.WorkspacePolicy{})); err != nil {
		t.Fatalf("env should be permitted: %v", err)
	}
}

func TestValidateParamsEnforcesRepoAllowlist(t *testing.T) {
	r := &OperationRegistry{}
	op := &Operation{Params: map[string]ParamDef{"repo": {Type: "enum", From: "$workspace.github_repos"}}}
	w := ws(config.WorkspacePolicy{GithubRepos: []string{"allowed-repo"}})

	if err := r.ValidateParams(op, map[string]interface{}{"repo": "allowed-repo"}, w); err != nil {
		t.Fatalf("allowed repo rejected: %v", err)
	}
	if err := r.ValidateParams(op, map[string]interface{}{"repo": "secret-repo"}, w); err == nil {
		t.Fatal("expected disallowed repo to be rejected")
	}
	// Unconfigured list => no restriction.
	if err := r.ValidateParams(op, map[string]interface{}{"repo": "anything"}, ws(config.WorkspacePolicy{})); err != nil {
		t.Fatalf("unconfigured allow-list must not restrict: %v", err)
	}
}

func TestValidateParamsIntMax(t *testing.T) {
	r := &OperationRegistry{}
	op := &Operation{Params: map[string]ParamDef{"n": {Type: "int", Max: 100}}}
	if err := r.ValidateParams(op, map[string]interface{}{"n": float64(50)}, ws(config.WorkspacePolicy{})); err != nil {
		t.Fatalf("value within max rejected: %v", err)
	}
	if err := r.ValidateParams(op, map[string]interface{}{"n": float64(100000)}, ws(config.WorkspacePolicy{})); err == nil {
		t.Fatal("expected value over max to be rejected")
	}
}

func TestValidateParamsBoolRequiresJSONBoolean(t *testing.T) {
	r := &OperationRegistry{}
	op := &Operation{Params: map[string]ParamDef{"flag": {Type: "bool"}}}
	for _, val := range []interface{}{true, false} {
		if err := r.ValidateParams(op, map[string]interface{}{"flag": val}, ws(config.WorkspacePolicy{})); err != nil {
			t.Fatalf("boolean %v rejected: %v", val, err)
		}
	}
	for _, val := range []interface{}{"yes", "true", float64(1), nil} {
		if err := r.ValidateParams(op, map[string]interface{}{"flag": val}, ws(config.WorkspacePolicy{})); err == nil {
			t.Fatalf("expected non-boolean %#v to be rejected", val)
		}
	}
}

func TestJQLScopeInjectsFilter(t *testing.T) {
	c := &JQLScopeConstraint{}
	w := ws(config.WorkspacePolicy{JiraProjects: []string{"MD", "PROJ"}})
	params := map[string]interface{}{"jql": "project = SECRET OR status = Open"}
	if err := c.Check(nil, params, w); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := params["jql"].(string)
	want := "(project = SECRET OR status = Open) AND project in (MD,PROJ)"
	if got != want {
		t.Fatalf("jql = %q, want %q", got, want)
	}
}

func TestJQLScopeNoProjectsNoop(t *testing.T) {
	c := &JQLScopeConstraint{}
	params := map[string]interface{}{"jql": "status = Open"}
	if err := c.Check(nil, params, ws(config.WorkspacePolicy{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := params["jql"].(string); got != "status = Open" {
		t.Fatalf("jql should be unchanged when no projects configured, got %q", got)
	}
}

func TestJQLScopeKeepsTrailingOrderBy(t *testing.T) {
	c := &JQLScopeConstraint{}
	w := ws(config.WorkspacePolicy{JiraProjects: []string{"MD", "PROJ"}})
	cases := []struct{ name, jql, want string }{
		{"desc", `status = Open ORDER BY updated DESC`, `(status = Open) AND project in (MD,PROJ) ORDER BY updated DESC`},
		{"lowercase multi", `status = Open order by priority desc, created`, `(status = Open) AND project in (MD,PROJ) order by priority desc, created`},
		{"custom field", `status = Open ORDER BY cf[10117] ASC, "Story Points" DESC`, `(status = Open) AND project in (MD,PROJ) ORDER BY cf[10117] ASC, "Story Points" DESC`},
		{"only order by", `ORDER BY created DESC`, `project in (MD,PROJ) ORDER BY created DESC`},
		{"order by in string", `summary ~ "x ORDER BY y"`, `(summary ~ "x ORDER BY y") AND project in (MD,PROJ)`},
		{"word ending in or", `labels = color ORDER BY key`, `(labels = color) AND project in (MD,PROJ) ORDER BY key`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{"jql": tc.jql}
			if err := c.Check(nil, params, w); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := params["jql"].(string); got != tc.want {
				t.Fatalf("jql = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJQLScopeRejectsScopeEscapes(t *testing.T) {
	c := &JQLScopeConstraint{}
	w := ws(config.WorkspacePolicy{JiraProjects: []string{"MD"}})
	for name, jql := range map[string]string{
		"stray close paren":   `project = SECRET) OR (project = SECRET`,
		"unclosed paren":      `(project = MD`,
		"unclosed quote":      `summary ~ "abc`,
		"paren in order by":   `status = Open ORDER BY created) OR (project = SECRET`,
		"predicate in order":  `status = Open ORDER BY created, project = SECRET`,
		"function in order":   `status = Open ORDER BY now()`,
		"second order by":     `status = Open ORDER BY created ORDER BY key`,
		"order by no field":   `status = Open ORDER BY`,
		"order by then query": `status = Open ORDER BY created DESC OR project = SECRET`,
	} {
		t.Run(name, func(t *testing.T) {
			params := map[string]interface{}{"jql": jql}
			if err := c.Check(nil, params, w); err == nil {
				t.Fatalf("expected %q to be rejected, got jql %q", jql, params["jql"])
			}
		})
	}
}
