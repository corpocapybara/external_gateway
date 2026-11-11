# T0019 design: refuse unknown constraint types

Todo: `todo/T0019.md` (acceptance there is the contract; this note says how).

## Problem (verified)
`CheckConstraints` (`internal/policy/registry.go` ~571) switches on `constraint["type"]` with no `default`, and
`loadFromFile` (~303) never looks at the type. A typo (`jql_scop`, `jira_field`) loads fine and the guard silently
disappears; for `jira_fields` any list then passes, since `ValidateParams` has no list case.

## Decision
One table of known types in `internal/policy`, read by the load check and by `CheckConstraints`' new default case.
Unknown types are refused at load and fail closed at request time. The 5 types no code reads stay accepted with no
effect (owner 2026-11-11); kibana enforcement is T0021, out of scope.

```go
// constraintKind says what a known constraint type does at request time.
type constraintKind int

const (
    constraintChecked  constraintKind = iota // CheckConstraints enforces it
    constraintMarker                         // CheckConstraints accepts it; enforced elsewhere or a tag
    constraintNoEffect                       // accepted, nothing enforces it (owner 2026-11-11)
)

// constraintTypes is the single list of constraint types an ops file may use.
var constraintTypes = map[string]constraintKind{
    "jql_scope":   constraintChecked,
    "jira_fields": constraintChecked,
    "readonly":       constraintMarker, // tag; PostgresReadOnlyConstraint.Check is a no-op
    "allowed_tables": constraintMarker, // the postgres connector enforces pg_allowed_tables
    // project_scope / repo_scope: their ops scope the param by a literal enum, which can drift from workspace policy.
    "project_scope": constraintNoEffect,
    "repo_scope":    constraintNoEffect,
    // max_rows / statement_timeout: the postgres connector hard-codes 1000 rows and 5000 ms; YAML values are ignored.
    "max_rows":          constraintNoEffect,
    "statement_timeout": constraintNoEffect,
    // kibana_index_scope: not enforced (T0021).
    "kibana_index_scope": constraintNoEffect,
}
```

A map with a kind (not three slices) keeps one list; the kind lets the request-time test loop over exactly the
no-effect types, so list and test cannot drift. `tunnel` is not a constraint type (it is `requires_tunnel` on the
environment) and stays out of the list; OPERATIONS.md's `### tunnel` heading moves out of the Constraints section's
type list, or says so, so nobody writes `type: tunnel`.

### Load check
New `checkConstraintTypes(id string, op *Operation) error` in registry.go, called in `loadFromFile` **before**
`checkJiraFields` (so an op with both a bad jira_fields and a typo reports the type error, deterministically):

- `t, ok := constraint["type"].(string)`; `!ok || t == ""` → `operation <id>: constraint has no type`
  (covers a missing key, `type: null`, `type: ""`, `type: 7`, and a `- ~` entry, which decodes to a nil map).
- `t` not a key of `constraintTypes` → `operation <id>: unknown constraint type "<t>"` (`%q`). Matching is exact:
  `JQL_SCOPE` and `"jql_scope "` are refused.
- A constraint entry that is a scalar (`- jql_scope`) already fails `yaml.Unmarshal` into `[]map[string]interface{}`
  with `parsing operations file: ...`; unchanged, covered by a test row.

`NewRegistry` wraps it as `loading operations from <path>: ...`. Ops are a map, so with several bad ops the one
reported is arbitrary; each op's own error is deterministic.

Blast radius: `cmd/egw/main.go` NewRegistry error stops the service at start; `/admin/reload`
(`internal/server/server.go` ~2286) returns 500 `ops reload failed: ...` and keeps the old registry (it stores the
new state only after a successful load); `cmd/egw-desktop/configedit.go:106` shows the error before publishing.
Every committed `config.example/operations*.yaml` and the local gitignored `operations.cm/.thp/.cc.yaml` use only
the 9 types (checked by test and by a throwaway load).

### Request-time default
`CheckConstraints` gets:

```go
default:
    if _, known := constraintTypes[constraintType]; !known {
        return fmt.Errorf("unknown constraint type %q on %s", constraintType, op.ID)
    }
```

`constraintType` comes from `constraint["type"].(string)` with the ok dropped, so a missing, nil or non-string type
is `""` and prints `unknown constraint type "" on <id>`, never `%!q(<nil>)`. Known types without a case (the 5
no-effect ones) fall through to `nil`. The existing `readonly` / `allowed_tables` cases stay. Both server call
sites (server.go 459, 534) already map a CheckConstraints error to `writeDeny(..., "constraint", ...)` → 403
`rule: constraint`; no server code changes. This only bites for ops built in code (tests, built-ins), since
loaded ops are already filtered.

### Interaction with T0013 (in progress, branch T0013)
T0013 adds a `jira_key_scope` type. Whichever of T0019/T0013 merges second must add `"jira_key_scope":
constraintChecked` to `constraintTypes`. If T0019 lands first, T0013's own load tests (`TestJiraScopeLoad`) fail
until it does, so the omission cannot ship silently. T0019's closing report names this for the T0013 session.

## Tests
`internal/policy/constraint_types_test.go`:
- `TestConstraintTypeLoadErrors`: one temp ops file per row (a minimal http-rest op plus the constraint YAML).
  Refused with exact substrings: `jql_scop`, missing type, `type: null`, `type: ""`, `type: 7`, `JQL_SCOPE`,
  `"jql_scope "`; `- jql_scope` refused with `parsing operations file`. Loads: `constraints: null`,
  `constraints: []`, no key, and one op carrying all 9 types (jira_fields with `allowed`, a `fields: {type: list,
  default: [summary]}` param and `{{fields}}` in `body_template`; the others need nothing at load). The all-9 row
  builds its constraint list from `constraintTypes` keys, so a new type with load-time needs fails here first.
  One extra row: an op with an unknown type AND a bad jira_fields reports the type error.
- `TestCheckConstraintsUnknownTypeFailsClosed`: op built in code; exact detail for missing, `""` and `jql_scop`;
  loops over `constraintTypes` and asserts every `constraintNoEffect` type returns nil (asserts the loop ran 5 times).
- `TestExampleOpsLoad`: `filepath.Glob("../../config.example/operations*.yaml")`, fails if none found, each loads;
  then copies `operations.yaml` with the first `type: jql_scope` replaced by `type: jql_scop` (asserts the
  replace changed something) into a temp dir and asserts NewRegistry refuses it with `unknown constraint type
  "jql_scop"`.

`internal/server/admin_hardening_test.go`:
- `TestReloadRefusesUnknownConstraintType`: setup as `TestReloadConcurrentWithRequests`, but the role grants
  `a.b`. A dry-run of `a.b` answers 200 first; the ops file is rewritten so `a.b` carries `type: jql_scop`;
  `POST /admin/reload` → 500, body contains `ops reload failed` and `unknown constraint type`; the same dry-run
  still answers 200 (had the new registry been installed, the default case would 403 it).

Mutation checks (author, before review): drop the load-check call → load rows fail; drop the default case →
request-time rows fail; both reverted.

## Docs
`.ai/OPERATIONS.md` Constraints section: the refusal at load and its consequences (service won't start;
`/admin/reload` keeps old ops, 500; egw-desktop pre-publish check shows it); enforced types (`jql_scope`,
`jira_fields`), markers (`readonly`, `allowed_tables` enforced by the postgres connector via `pg_allowed_tables`),
and the 5 no-effect types with what does or does not enforce each; `tunnel` noted as not a constraint type.

## Risk
High (a load refusal can stop a deployed gateway; policy boundary). Review: all four lanes plus an adversarial
pass; full suite once before the merge.
