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
- EG0030 · in-testing · ops-config · S — No GitLab op posts a general MR note: gitlab.mr_note only replies, mr_discussion_create needs a diff position
- EG0031 · in-review · server · S — Kibana op paths use {{index}}, so the outer braces stay in the upstream URL
- EG0035 · in-progress · docs · M — Project review (rubric v1)
- EG0044 · draft · server · S — OAuth refresh cooldown after a still-rejected retry (email + http-rest) · blocked by EG0045
- EG0045 · in-review · server · S — Skip OAuth refresh when the stored access token already changed (concurrent rejections)
- EG0046 · in-testing · server · S — Email connector responses skip allow_fields shaping (hotmail/gmail return attrs, carbon-copy, seq)
- EG0048 · draft · deploy · M — Rewrite git history to purge the real workspace names and Jira key, then force-push · blocked by EG0030, EG0049

## P3

- EG0043 · refined · connectors · S — Back off form-session logins after a failed login
- EG0049 · designed · server · XS — Replace a real corporate host and a person's first name in tracked files with placeholders
- EG0051 · draft · server · S — allow_fields ignored by local-service, postgres and jira-worklog connectors

## Externally blocked

- EG0011 · in-progress · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags · externally blocked: acme automated-tests Draft MR !3508 (branch task/regression-default-tags, b6717327): Set versions falls back to the latest tag for empty tag params; parked by the owner 2026-11-16, not on develop. Re-check item 2 once merged
- EG0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser · externally blocked: No Teleport route to Grafana (owner 2026-11-12); needs a Teleport app credential and a service token
- EG0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and acme's docs call `kibana.*` dead · externally blocked: Kibana prod/dev behind Teleport app access, no route (owner 2026-11-12); same block as EG0001
- EG0009 · draft · connectors · M — No Acme admin-panel connector; agents capture the admin session token from the page · externally blocked: Admin-panel API behind Teleport app access, no route or service credential (owner 2026-11-12)
