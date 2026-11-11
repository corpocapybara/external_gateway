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
- T0005 · draft · ops-config · M — GitLab writes and MR fields missing, so GitLab work falls back to the browser
- T0006 · draft · server · M — An expired GitLab PAT fails silently until a call returns `invalid_token`
- T0007 · draft · ops-config · M — No Jenkins test-report op
- T0008 · draft · ops-config · M — `corpjira` can't download attachment content
- T0009 · draft · connectors · M — No Thunderpick admin-panel connector; agents capture the admin session token from the page
- T0010 · draft · connectors · M — `postgres.schema` has no `schema` param
- T0011 · draft · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags
- T0013 · in-progress · server · M — No configurable Jira key-scope constraint or pre-checked issue-link delete
- T0014 · designed · ops-config · M — The thp Jira write ops don't cover create, links, versions or editmeta, and jira.edit never works · blocked by T0013
- T0015 · draft · ops-config · S — cm jira.edit sends invalid JSON: fields is typed string · blocked by T0014
- T0019 · refined · server · S — Unknown constraint types in ops YAML are silently ignored
- T0020 · draft · server · S — Undeclared request params leak upstream as query strings, also on write ops
- T0021 · draft · server · M — kibana_index_scope is never enforced: kibana.search can query any index

## P3

- T0016 · draft · server · S — cm jira.create's project_scope constraint is silently ignored · blocked by T0013
- T0017 · draft · ops-config · S — thp jira.comment pins issue keys with a hard-coded pattern · blocked by T0013
- T0018 · tested · server · S — A jira_fields op loads without {{fields}} in body_template or without a fields default
