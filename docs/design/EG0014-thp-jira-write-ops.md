# T0014 design: thp Jira write ops (create, edit, link, link_delete, versions, editmeta)

Todo: `todo/T0014.md` (acceptance there is the contract; this note says how). Depends on T0013
(`jira_key_scope`, `precheck: jira_issue_link`; `docs/design/EG0013-jira-key-scope.md`).

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
Needs T0013 merged first (`jira_key_scope`, `precheck`); the T0014 branch is cut from master after it.
Wall time of `go test ./internal/server` is measured before and after and reported; one call per case, no loops.

`go test ./internal/server -run TestExampleJiraOps` (new file `internal/server/example_jira_ops_test.go`) builds
its own fixture, not `newEnvFixture` (whose ops map and role are hand-made): `policy.NewRegistry(
"../../config.example/operations.yaml")`; a config with workspace `thp`, `Policy.JiraProjects: [THP]`, a site
`jira` with ONE environment whose `base_url` is a recording `httptest` server (method, path, `RawQuery`, body per
request; handler set per case), an agent whose role grants the six ops, `audit.Init(t.TempDir(), 1)`; calls go
through `ServeHTTP` with the Bearer token.
- Happy paths, exact method + path + body, empty `RawQuery`:
  - create: body exactly `{"fields":{"issuetype":{"name":"Bug"},"project":{"key":"THP"},"summary":"s"},"update":{}}`
    (json.Marshal sorts keys);
  - edit with only `update`: `{"fields":{},"update":{"fixVersions":[{"add":{"name":"9.21.0"}}]}}`; edit with only
    `fields`: `"update":{}` defaulted;
  - link: `{"type":{"name":"Blocks"},"inwardIssue":{"key":"THP-1"},"outwardIssue":{"key":"THP-2"}}`;
  - link_delete: GET `/rest/api/2/issueLink/10` (answered with in-scope keys) then DELETE, 204 passed through
    (the asserted status/body is whatever `executeOp` renders for an empty 204 today, read once and pinned);
  - versions: GET `/rest/api/3/project/THP/versions`, empty body; the fake also returns `self` and
    `description`, and the test asserts `allow_fields` strips them;
  - editmeta: GET `/rest/api/3/issue/THP-1/editmeta`, empty body.
- Denials, each asserting the recorder saw no request: edit key ABC-1 (403 constraint); create
  `fields.project {id:10100}` (403 constraint); create without `fields.project` (403 constraint, required);
  edit `update {issuelinks:[...]}` (403 constraint); edit `fields.project {key:THP}` (403 constraint, absent on
  edit); link inward OPS-1 (403 constraint); editmeta key ABC-1 (403 constraint); versions project OPS (403
  validation, enum); `update` as a JSON string, as null, as an array (403 validation).
- Passthrough, documented not tested: edit with neither object sends `{"fields":{},"update":{}}` (Jira 400s);
  link inward == outward; link_delete id `007`.
- Undeclared params: `executeHttpConnector` (`server.go:788-809`) sends any param not in path or body as a
  query string, on every op in every workspace. That is gateway-wide behaviour, not this todo's; recorded as its
  own todo. The happy-path `RawQuery` assertions keep these ops from adding any.

`/help`: the generic Examples line for `jira.create` (`internal/server/server.go:1907`, old project/summary
params) is removed; `TestHelpExamples` asserts no `jira.create` line in a jira workspace's Examples block.

Manual (owner: no live writes), after an elevated `deploy/update.ps1`, recorded in the `tested` note: versions
THP returns names; editmeta on a named THP issue returns fields; `jira.issue` on a named THP issue that has links
returns `fields.issuelinks[].id`; edit key ABC-1 and create `fields.project.key ABC` return 403 constraint, and
the deployed audit log shows a deny and no allow for those request ids; `GET /capabilities?workspace=thp` with
the dev-bot token lists the five new grants; `Select-String` on the deployed `config.thp.yaml` shows readonly's
two.

## Notes on scope
- `jira.versions`' enum and the constraint both leave the project unscoped when `jira_projects` is empty (same
  fallback as T0013); thp sets it.
- `env`: the example ops declare none (the example Jira site has one environment). The thp copies keep an
  optional `env` string param like thp's `jira.issue` and `jira.edit` today (`config.thp.yaml` jira site:
  `default_env: prod`, plus a corpo env); the T0013 pre-check passes `env` through to its GET.
- `jira.link` `type` stays free text (JSON-escaped, Jira rejects unknown types); the acceptance asks no more.
- `.ai/OPERATIONS.md`: both tables that list `jira.create`/`jira.edit` (~lines 228-229, 271) are updated.

## Risk
High: agent-facing op API (create's params change shape). Callers of the example `jira.create` in docs are updated
in the same change; the only Go reference to its old params is the generic `/help` example, removed here.
