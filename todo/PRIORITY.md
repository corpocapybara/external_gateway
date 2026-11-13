# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

none

## P1

none

## P2

- EG0011 · designed · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags
- EG0021 · refined · server · M — kibana_index_scope is never enforced: kibana.search can query any index
- EG0022 · designed · server · S — jql_scope with a non-string, non-list projects value fails open
- EG0023 · designed · server · S — A null operation in the ops YAML panics loadFromFile
- EG0027 · designed · docs · S — Commit .claude/worktrees/ and .scratch/ ignores in .gitignore
- EG0030 · designed · ops-config · S — No GitLab op posts a general MR note: gitlab.mr_note only replies, mr_discussion_create needs a diff position
- EG0031 · draft · ops-config · S — Kibana op paths use {{index}}, so the outer braces stay in the upstream URL
- EG0033 · designed · docs · S — Switch this project's todo ids to the EG prefix (todo migrate-prefix --to EG)
- EG0035 · draft · unset · M — Project review (rubric v1)

## P3

- EG0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser
- EG0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and thp's docs call `kibana.*` dead
- EG0009 · draft · connectors · M — No Thunderpick admin-panel connector; agents capture the admin session token from the page
- EG0024 · draft · unset · M — Define flow rules per bucket in todo/_config.yaml (ToDo TD0146)
- EG0034 · designed · connectors · S — WinCredStore.Resolve panics when a credential lookup returns nil without an error
