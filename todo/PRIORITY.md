# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

none

## P1

none

## P2

- EG0021 · refined · server · M — kibana_index_scope is never enforced: kibana.search can query any index
- EG0054 · draft · server · S — Test notes from the project review: affected-subset command, speed tiers, live-config tests, untested critical paths (review R3-R6)

## P3

- EG0051 · draft · server · S — allow_fields ignored by local-service, postgres and jira-worklog connectors
- EG0055 · draft · server · S — Doc drift and dead code from the project review: connector list, admin endpoints, unused TLS/listen_addr, unwired scheduler, body logging (review R7-R10)
- EG0059 · draft · cli-tools · S — leakcheck tests take 34 s (history scan tests add ~11 s): cut git process count
- EG0064 · draft · server · S — Audit records carry the configured agent id, not the token SHA-256 (review R14)
- EG0065 · draft · server · S — Audit file retention and a persisted service log under the SCM (review R13)
- EG0066 · draft · server · M — Runtime metrics: per-op request count and latency, process CPU and RSS (review R13)
- EG0067 · draft · server · XS — Update the 4 outdated direct Go modules: wincred, go-sasl, zerolog, x/sys (review R11)
- EG0068 · draft · server · XS — Run govulncheck ./... and record the result in the project review (review R11)

## Externally blocked

- EG0011 · in-progress · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags · externally blocked: acme automated-tests Draft MR !3508 (branch task/regression-default-tags, b6717327): Set versions falls back to the latest tag for empty tag params; parked by the owner 2026-11-16, not on develop. Re-check item 2 once merged
- EG0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser · externally blocked: No Teleport route to Grafana (owner 2026-11-12); needs a Teleport app credential and a service token
- EG0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and acme's docs call `kibana.*` dead · externally blocked: Kibana prod/dev behind Teleport app access, no route (owner 2026-11-12); same block as EG0001
- EG0009 · draft · connectors · M — No Acme admin-panel connector; agents capture the admin session token from the page · externally blocked: Admin-panel API behind Teleport app access, no route or service credential (owner 2026-11-12)
