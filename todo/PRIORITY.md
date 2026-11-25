# Priority list

Every open todo, in the order to take it: priority, then `order`, then id. The ToDo tool rewrites this file
in every todo commit; do not edit it while the tool is available. Without the tool, update it by hand in the
same commit as the todo change (todo rules, section "Without the tool").

## P0

none

## P1

none

## P2

- EG0074 · draft · unset · S — Apply ToDo tool changes to this repo's docs

## P3

- EG0076 · draft · ops-config · S — egw: open notes from EG0075 (second workspace mcp.call, mcp dry-run test, live ops drift)

## Externally blocked

- EG0011 · in-progress · ops-config · M — `jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags · externally blocked: acme automated-tests Draft MR !3508 (branch task/regression-default-tags, b6717327): Set versions falls back to the latest tag for empty tag params; parked by the owner 2026-11-16, not on develop. Re-check item 2 once merged
- EG0001 · draft · ops-config · M — No Grafana op: prod logs and metrics go through the browser · externally blocked: No Teleport route to Grafana (owner 2026-11-12); needs a Teleport app credential and a service token
- EG0002 · draft · ops-config · M — `kibana.search` has no stg/dev `base_url`, and acme's docs call `kibana.*` dead · externally blocked: Kibana prod/dev behind Teleport app access, no route (owner 2026-11-12); same block as EG0001
- EG0009 · draft · connectors · M — No Acme admin-panel connector; agents capture the admin session token from the page · externally blocked: Admin-panel API behind Teleport app access, no route or service credential (owner 2026-11-12)
