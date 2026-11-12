# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

none

## P1

none

## P2

- T0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser
- T0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and thp's docs call `kibana.*` dead
- T0008 · tested · server · M — `corpjira` can't download attachment content
- T0009 · draft · connectors · M — No Thunderpick admin-panel connector; agents capture the admin session token from the page
- T0010 · draft · connectors · M — `postgres.schema` has no `schema` param
- T0011 · draft · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags
- T0020 · draft · server · S — Undeclared request params leak upstream as query strings, also on write ops
- T0021 · draft · server · M — kibana_index_scope is never enforced: kibana.search can query any index
- T0022 · draft · server · S — jql_scope with a non-string, non-list projects value fails open
- T0023 · draft · server · S — A null operation in the ops YAML panics loadFromFile

## P3

- T0024 · draft · unset · M — Define flow rules per bucket in todo/_config.yaml (ToDo T0146)
