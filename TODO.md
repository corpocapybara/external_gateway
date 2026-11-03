# TODO

Open gaps in egw, one item each, with the evidence that shows it. Remove an item once it ships.

- [ ] **Jira reads drop empty fields, so callers can't tell "unset" from "not returned"** (2026-11-03).
  `jira.issue` and `jira.search` shape with `fields.fixVersions[].name` and `fields.customfield_10340.value`
  (`operations.thp.yaml`). An empty `fixVersions` array or a null THP Service drops the key entirely, while
  `fields.labels` keeps `[]`. Seen on THP-21236 (no `fixVersions` key) and THP-17878 (neither key).
  `thp work-status` then prints "egw returns no fix version/labels/Team/THP Service yet" instead of "fix version
  missing". Fix: keep a projected key present as `[]` / `null` when the upstream field is empty or null.
- [ ] **An unknown operation returns HTTP 403** (2026-11-03). `jira.get` (no such op; it's `jira.issue`) answers
  `403 {"error":"denied","rule":"operation","detail":"operation not found"}` (`server.go` `writeDeny`), which reads
  as a permission denial. Return 404 and name the closest existing op.
- [ ] **`POST /feedback` should add each new `todo` item to this file** (2026-11-03). Today it only appends
  `timestamp | todo | text` to `feedback.log`, which is gitignored, so those items never reach the repo.
