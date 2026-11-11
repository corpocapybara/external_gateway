# T0014 design: thp Jira write ops (create, edit, link, link_delete, versions, editmeta)

Todo: `todo/T0014.md` (acceptance there is the contract; this note says how). Depends on T0013
(`jira_key_scope`, `precheck: jira_issue_link`; `docs/design/T0013-jira-key-scope.md`).

## Decision
Writes take Jira's own `fields` / `update` objects verbatim and are pinned by `jira_key_scope`, not rebuilt from
per-field string params. Reason: the browser precedent sends custom fields (`customfield_10117`, `customfield_10340`),
`assignee`, `parent` and `{update:{fixVersions:[{add}]}}`; a per-field op would need a new param for each and still
could not express `update`. Both object params are rendered by `formatParamValue` (JSON-marshalled map), so
`{"fields":{{fields}},"update":{{update}}}` is valid JSON for any object. The string-typed `fields` of the old thp
`jira.edit` was JSON-escaped into the body (0 of 16 calls succeeded); the object type fixes it.

Alternatives rejected: keep per-field create params and add `fields_extra` (two ways to set one field, both need
scoping); an allowlist of field names (owner chose pinning keys, not field names; T0003's `jira_fields` is for
search output, not writes).

## Ops (`config.example/operations.yaml`, mirrored in `operations.thp.yaml`)
| op | method, path | params | constraint |
|---|---|---|---|
| jira.create | POST /rest/api/2/issue | fields (object, required), update (object, default {}) | jira_key_scope: project_keys [fields.project.key], required [fields.project.key], issue_keys [fields.parent.key], absent [fields.issuelinks, update.issuelinks, update.parent, update.project] |
| jira.edit | PUT /rest/api/3/issue/{key} | key (string, required), fields, update (object, default {} each) | issue_keys [key, fields.parent.key], absent as create plus fields.project |
| jira.link | POST /rest/api/2/issueLink | type (string, required), inward, outward (string, required) | issue_keys [inward, outward] |
| jira.link_delete | DELETE /rest/api/2/issueLink/{id} | id (string, ^[0-9]+$) | precheck: jira_issue_link; jira_key_scope projects only |
| jira.versions | GET /rest/api/3/project/{project}/versions | project (enum from $workspace.jira_projects) | — (enum scopes); allow_fields id, name, released, archived, releaseDate |
| jira.editmeta | GET /rest/api/3/issue/{key}/editmeta | key | issue_keys [key] |

`jira.link` body: `{"type":{"name":"{{type}}"},"inwardIssue":{"key":"{{inward}}"},"outwardIssue":{"key":"{{outward}}"}}`
(string params, so they are escaped; the constraint already proved they are bare keys). All constraints use
`projects: $workspace.jira_projects`.

Create uses API v2 (wiki-markup `description` string, as the browser does); edit uses v3 (ADF). This asymmetry is
documented in `.ai/OPERATIONS.md`, not hidden.

Unchanged: op ids (grants in `config.example/config.yaml:29`, `config.example/config.globex.yaml:15`,
`config.example/roles.yaml:9,21,144` stay valid), the built-in `jira.create` (`internal/policy/registry.go:263`,
only without an ops file) and `operations.globex.yaml`.

## thp (gitignored, not in git)
`operations.thp.yaml`: the six ops as above, the old `jira.edit` replaced; `jira.issue` `allow_fields` gains
`fields.issuelinks[].id`, `fields.issuelinks[].type.name`, `fields.issuelinks[].inwardIssue.key`,
`fields.issuelinks[].outwardIssue.key`, `fields.fixVersions[].name` (so an agent can find a link id to delete and
see versions). `config.thp.yaml`: role dev-bot += create, link, link_delete, versions, editmeta; readonly +=
versions, editmeta. Backups of both files go to the scratchpad before editing.

## Tests
`go test ./internal/server -run TestExampleJiraOps` (new file `internal/server/example_jira_ops_test.go`): loads
`config.example/operations.yaml` through `policy.NewRegistry`, a config whose `jira` site points at an
`httptest` server recording method, path and body, and a granted agent; calls each op through the HTTP handler and
asserts exact method, path and body (the two bodies named in the acceptance compared as exact strings after
`json.Compact`). Denials (edit ABC-1, create project {id:10100}, update.issuelinks, update as a string) assert 403
and the right `rule`, and that the recorder saw no request. jira.link_delete's happy path GETs then DELETEs.

Manual (owner: no live writes): after an elevated `deploy/update.ps1`, reads only — versions, editmeta, issue with
links, capabilities — plus two denied writes (403, nothing sent). Recorded in the `tested` move note.

## Risk
High: agent-facing op API (create's params change shape). Callers of the example `jira.create` in docs are updated
in the same change; no Go code calls its old params (checked by grep in refinement r2).
