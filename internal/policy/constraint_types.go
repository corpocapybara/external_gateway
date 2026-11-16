package policy

import (
	"fmt"

	"github.com/external_gateway/internal/config"
)

// constraintKind says what a known constraint type does at request time.
type constraintKind int

const (
	constraintChecked  constraintKind = iota // CheckConstraints enforces it
	constraintMarker                         // CheckConstraints accepts it; a tag, or enforced elsewhere
	constraintNoEffect                       // accepted, but nothing enforces it (owner 2026-11-11)
)

// constraintTypes is the single list of constraint types an ops file may use.
// loadFromFile refuses any other type and CheckConstraints fails closed on one.
var constraintTypes = map[string]constraintKind{
	"jql_scope":      constraintChecked,
	"jira_fields":    constraintChecked,
	"jira_key_scope": constraintChecked,

	// A tag; PostgresReadOnlyConstraint.Check is a no-op.
	"readonly": constraintMarker,
	// AllowedTablesConstraint.Check is a no-op; the postgres connector enforces
	// the workspace's pg_allowed_tables.
	"allowed_tables": constraintMarker,

	// Their ops scope the param by a literal enum, which can drift from the
	// workspace policy; the constraint itself checks nothing.
	"project_scope": constraintNoEffect,
	"repo_scope":    constraintNoEffect,
	// The postgres connector hard-codes 1000 rows and a 5000 ms statement
	// timeout; any other value in the YAML is ignored.
	"max_rows":          constraintNoEffect,
	"statement_timeout": constraintNoEffect,
	// Not enforced (T0021).
	"kibana_index_scope": constraintNoEffect,
}

// checkConstraintTypes refuses, at load time, a constraint whose type is
// missing, not a non-empty string, or not exactly one of constraintTypes.
func checkConstraintTypes(id string, op *Operation) error {
	for _, constraint := range op.Constraints {
		t, ok := constraint["type"].(string)
		if !ok || t == "" {
			return fmt.Errorf("operation %s: constraint has no type", id)
		}
		if _, known := constraintTypes[t]; !known {
			return fmt.Errorf("operation %s: unknown constraint type %q", id, t)
		}
	}
	return nil
}

// jqlScopeSources are the $workspace names a jql_scope projects value may name,
// each with the policy list it reads.
var jqlScopeSources = map[string]func(*config.WorkspacePolicy) []string{
	"$workspace.jira_projects":     func(p *config.WorkspacePolicy) []string { return p.JiraProjects },
	"$workspace.bitbucket_repos":   func(p *config.WorkspacePolicy) []string { return p.BitbucketRepos },
	"$workspace.pg_allowed_tables": func(p *config.WorkspacePolicy) []string { return p.PgAllowedTables },
}

// jqlScopeProjectsRule is the error text for a projects value jql_scope does not accept.
const jqlScopeProjectsRule = "projects must be a list or one of " +
	"$workspace.bitbucket_repos, $workspace.jira_projects, $workspace.pg_allowed_tables"

// validJQLScopeProjects reports whether v is a projects value jql_scope accepts:
// a list (its items are checked per request) or a jqlScopeSources name.
func validJQLScopeProjects(v interface{}) bool {
	switch v := v.(type) {
	case []interface{}:
		return true
	case string:
		return jqlScopeSources[v] != nil
	}
	return false
}

// checkJQLScope refuses, at load time, a jql_scope whose projects key is present
// but not a value validJQLScopeProjects accepts; such a value used to fall back
// to the workspace's jira_projects and, with none configured, scope nothing
// (EG0022). No projects key keeps that fallback.
func checkJQLScope(id string, op *Operation) error {
	for _, constraint := range op.Constraints {
		if t, _ := constraint["type"].(string); t != "jql_scope" {
			continue
		}
		if v, present := constraint["projects"]; present && !validJQLScopeProjects(v) {
			return fmt.Errorf("operation %s: jql_scope %s", id, jqlScopeProjectsRule)
		}
	}
	return nil
}
