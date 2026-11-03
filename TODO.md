# TODO

Open gaps in egw, one item each, with the evidence that shows it. Remove an item once it ships.

- [ ] **An unknown operation returns HTTP 403** (2026-11-03). `jira.get` (no such op; it's `jira.issue`) answers
  `403 {"error":"denied","rule":"operation","detail":"operation not found"}` (`server.go` `writeDeny`), which reads
  as a permission denial. Return 404 and name the closest existing op.
- [ ] **`POST /feedback` should add each new `todo` item to this file** (2026-11-03). Today it only appends
  `timestamp | todo | text` to `feedback.log`, which is gitignored, so those items never reach the repo.
