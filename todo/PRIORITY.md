# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

- EG0047 · in-testing · cli-tools · M — Add GIT PreCommit Hook to sanitize data

## P1

none

## P2

- EG0021 · refined · server · M — kibana_index_scope is never enforced: kibana.search can query any index
- EG0030 · in-testing · ops-config · S — No GitLab op posts a general MR note: gitlab.mr_note only replies, mr_discussion_create needs a diff position
- EG0031 · draft · ops-config · S — Kibana op paths use {{index}}, so the outer braces stay in the upstream URL
- EG0035 · draft · unset · M — Project review (rubric v1)
- EG0037 · draft · unset · S — AGENTS.md: add ToDo section, mark acme/globex as placeholders, fix T ids
- EG0044 · draft · server · S — OAuth refresh cooldown after a still-rejected retry (email + http-rest)
- EG0045 · draft · server · S — Skip OAuth refresh when the stored access token already changed (concurrent rejections)
- EG0046 · draft · server · S — Email connector responses skip allow_fields shaping (hotmail/gmail return attrs, carbon-copy, seq)
- EG0048 · draft · deploy · M — Rewrite git history to purge the real workspace names and Jira key, then force-push

## P3

- EG0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser
- EG0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and acme's docs call `kibana.*` dead
- EG0009 · draft · connectors · M — No Acme admin-panel connector; agents capture the admin session token from the page
- EG0043 · draft · server · S — Back off form-session logins after a failed login
- EG0049 · draft · server · XS — Replace a real corporate host and a person's first name in tracked files with placeholders
