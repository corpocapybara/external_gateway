---
date: 2026-11-11
author: agent
todos: [T0003, T0018, T0019, T0021, T0022, T0023]
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
T0021 (draft, P2): enforce kibana_index_scope.
T0022 (draft, P2): jql_scope with a non-string, non-list projects value fails open (found in T0019 review).
T0023 (draft, P2): a null op in the ops YAML panics loadFromFile (found in T0019 adversarial pass).
Owner: deploy/update.ps1 to put T0003/T0018/T0019 live.
- Local gitignored operations.cm.yaml / .thp.yaml / .cc.yaml use only the 9 known types (critic YAML parse).
Load the local files with a throwaway `go run` (put a main in `_scratch/loadops`, inside the module, delete after).
## Open sessions and agents
none
## Next briefs
Refine T0021, T0022, T0023 per .ai/shared/rules/todo.md.
## Key files
internal/policy/registry.go (loadFromFile, checkJiraFields, CheckConstraints), internal/policy/jira_fields_test.go,
internal/server/admin_hardening_test.go (TestReloadConcurrentWithRequests pattern), internal/server/server.go
(reload ~2272, CheckConstraints call sites ~459/534), .ai/OPERATIONS.md (Constraints section),
config.example/operations*.yaml.
