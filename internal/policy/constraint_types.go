package policy

import "fmt"

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
	"jql_scope":   constraintChecked,
	"jira_fields": constraintChecked,

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
