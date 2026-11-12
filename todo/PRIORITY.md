# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

none

## P1

none

## P2

- T0010 · draft · connectors · M — `postgres.schema` has no `schema` param
- T0011 · draft · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags
- T0021 · draft · server · M — kibana_index_scope is never enforced: kibana.search can query any index
- T0022 · draft · server · S — jql_scope with a non-string, non-list projects value fails open
- T0023 · draft · server · S — A null operation in the ops YAML panics loadFromFile
- T0027 · designed · docs · S — Commit .claude/worktrees/ and .scratch/ ignores in .gitignore
- T0029 · draft · unset · M — Add GitLab note award_emoji op to egw
- T0030 · draft · ops-config · M — No GitLab op posts a general MR note: gitlab.mr_note only replies, mr_discussion_create needs a diff position
- T0031 · draft · ops-config · S — Kibana op paths use {{index}}, so the outer braces stay in the upstream URL

## P3

- T0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser
- T0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and thp's docs call `kibana.*` dead
- T0009 · draft · connectors · M — No Thunderpick admin-panel connector; agents capture the admin session token from the page
- T0024 · draft · unset · M — Define flow rules per bucket in todo/_config.yaml (ToDo T0146)
