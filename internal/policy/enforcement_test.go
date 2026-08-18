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
