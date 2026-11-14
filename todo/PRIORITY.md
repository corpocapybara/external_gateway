# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

- EG0039 · designed · connectors · M — Connector to hotmail account
- EG0040 · designed · ops-config · S — Hotmail live hookup in the cc workspace (tokens, config, deploy, end-to-end) · blocked by EG0039

## P1

none

## P2

- EG0011 · designed · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags
- EG0021 · refined · server · M — kibana_index_scope is never enforced: kibana.search can query any index
- EG0022 · designed · server · S — jql_scope with a non-string, non-list projects value fails open
- EG0030 · in-testing · ops-config · S — No GitLab op posts a general MR note: gitlab.mr_note only replies, mr_discussion_create needs a diff position
- EG0031 · draft · ops-config · S — Kibana op paths use {{index}}, so the outer braces stay in the upstream URL
- EG0035 · draft · unset · M — Project review (rubric v1)
- EG0036 · draft · server · S — /feedback duplicate check globs todo/T*.md, so it misses every todo once the repo's id_prefix is not T (this repo is EG now)
- EG0037 · draft · unset · S — AGENTS.md: add ToDo section, mark acme/globex as placeholders, fix T ids
- EG0038 · draft · unset · S — Remove real workspace name from tracked .ai/OPERATIONS.md and old T ids

## P3

- EG0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser
- EG0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and thp's docs call `kibana.*` dead
- EG0009 · draft · connectors · M — No Thunderpick admin-panel connector; agents capture the admin session token from the page
- EG0024 · draft · unset · M — Define flow rules per bucket in todo/_config.yaml (ToDo TD0146)
- EG0041 · in-progress · server · S — Ops YAML loading refuses an op with an empty or unknown connector and names bad ops in sorted order
- EG0043 · draft · server · S — Back off form-session logins after a failed login
