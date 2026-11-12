# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

none

## P1

none

## P2

- T0010 · tested · server · S — `postgres.schema` has no `schema` param
- T0011 · designed · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags
- T0021 · refined · server · M — kibana_index_scope is never enforced: kibana.search can query any index
- T0022 · designed · server · S — jql_scope with a non-string, non-list projects value fails open
- T0023 · designed · server · S — A null operation in the ops YAML panics loadFromFile
- T0027 · designed · docs · S — Commit .claude/worktrees/ and .scratch/ ignores in .gitignore
- T0029 · in-review · ops-config · S — Add GitLab note award_emoji op to egw
- T0030 · designed · ops-config · S — No GitLab op posts a general MR note: gitlab.mr_note only replies, mr_discussion_create needs a diff position
- T0031 · draft · ops-config · S — Kibana op paths use {{index}}, so the outer braces stay in the upstream URL
- T0032 · in-progress · server · S — postgres.tables has no schema param, so tables outside the configured schema cannot be listed
- T0033 · draft · unset · S — Switch this project's todo ids to the EG prefix (todo migrate-prefix --to EG)

## P3

- T0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser
- T0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and thp's docs call `kibana.*` dead
- T0009 · draft · connectors · M — No Thunderpick admin-panel connector; agents capture the admin session token from the page
- T0024 · draft · unset · M — Define flow rules per bucket in todo/_config.yaml (ToDo T0146)
