---
date: 2026-11-11
author: agent
todos: [T0003, T0018, T0019, T0021]
branches: []
sessions: []
---
# Handover: t0019-constraint-types (2026-11-11)
## Done
T0003 done (jira.search allowlisted `fields` via `jira_fields` constraint; merge 059edae). Owner still runs
`deploy/update.ps1` in an Admin shell to put it live (thp ops already edited locally).
T0018 done (jira_fields load check also requires `{{fields}}` in body_template and a fields default; merge 5c6c771).
T0021 filed (draft, P2): kibana_index_scope is never enforced; owner said own todo, not this run.
## Left, in order
T0019 is `refined` (refinement r1 2 lanes + r2 PASS; move note has risk: high and `design: needed`). Next: assign it,
write the design, run design critics (2 lanes in r1: code+scope, tests+edges; size S but risk high), move to
`designed`, implement in `.worktrees/T0019`, review with all four lanes + an adversarial pass (risk high), full
suite once, `--no-ff` merge, push, `todo done --apply`, remove worktree/branch, release the claim.
Design points already settled by critics (put them in the design):
- One known-types list in internal/policy, used by loadFromFile and by CheckConstraints' new default case.
- Run the type check in loadFromFile BEFORE checkJiraFields, so a mixed-error op reports deterministically.
- Request-time detail `unknown constraint type "<t>" on <id>`: a nil/missing type must print as empty (`""`),
  never `%!q(<nil>)`.
- The all-9-types load row needs: jira_fields -> `allowed`, a `fields` list param with a default, and
  `{{fields}}` in body_template (T0018); the others need no params at load.
- readonly and allowed_tables are markers (Check returns nil); allowed_tables is enforced by the postgres connector.
- No-effect reasons: project_scope/repo_scope ops scope the param by a literal enum (can drift from workspace
  policy); max_rows/statement_timeout are hard-coded 1000 rows / 5000 ms in internal/connectors/postgres/connector.go
  (other YAML values ignored); kibana_index_scope not enforced (T0021).
- A refused file at startup stops the service (cmd/egw/main.go NewRegistry error); /admin/reload returns 500 and
  keeps the old registry (internal/server/server.go ~2286).
- Local gitignored operations.cm.yaml / .thp.yaml / .cc.yaml use only the 9 known types (critic YAML parse).
Load the local files with a throwaway `go run` (put a main in `_scratch/loadops`, inside the module, delete after).
## Open sessions and agents
none
## Next briefs
Deliver T0019 to done per .ai/shared/rules/todo.md, starting from `refined`. Acceptance in todo/T0019.md is the
contract (7 items).
## Key files
internal/policy/registry.go (loadFromFile, checkJiraFields, CheckConstraints), internal/policy/jira_fields_test.go,
internal/server/admin_hardening_test.go (TestReloadConcurrentWithRequests pattern), internal/server/server.go
(reload ~2272, CheckConstraints call sites ~459/534), .ai/OPERATIONS.md (Constraints section),
config.example/operations*.yaml.
