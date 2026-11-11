# T0013 design: `jira_key_scope` constraint and `precheck: jira_issue_link`

Todo: `todo/T0013.md` (acceptance there is the contract; this note says how).

## Decision
One Jira-specific constraint type, `jira_key_scope`, checks issue keys and pinned project keys at dotted paths
inside the request params, and one op-level field, `precheck: jira_issue_link`, makes the HTTP connector GET an
issue link and scope both of its ends before it sends the op's own request (a link id carries no issue key, so a
param check cannot scope it).

```yaml
jira.create:
  params:
    fields: {type: object, required: true}
    update: {type: object, default: {}}
  body_template: '{"fields":{{fields}},"update":{{update}}}'
  constraints:
    - type: jira_key_scope
      projects: $workspace.jira_projects      # or a literal list [THP, SUP]
      issue_keys: [fields.parent.key]
      project_keys: [fields.project.key]
      required: [fields.project.key]
      absent: [fields.issuelinks, update.issuelinks, update.parent, update.project]

jira.link_delete:
  method: DELETE
  path: /rest/api/2/issueLink/{id}
  params:
    id: {type: string, required: true, pattern: '^[0-9]+$'}
  precheck: jira_issue_link
  constraints:
    - type: jira_key_scope
      projects: $workspace.jira_projects
```

Alternatives rejected: per-param `pattern` (hard-coded project list, the T0017 problem; can't reach into an
object); a generic JSON-path allow/deny constraint (more surface than the owner asked for; Jira key shape and
pinned-object rules are Jira-specific); scoping link delete by trusting an agent-supplied key (the id alone decides
what Jira deletes).

## Code: `internal/policy/registry.go`
1. `JiraKeyScopeConstraint{Projects []string; Restrict bool; IssueKeys, ProjectKeys, Required, Absent []string}`
   with `Check(op, params, ws)`, beside `JQLScopeConstraint`. Path lookup `lookupPath(params, "a.b.c")` walks
   `map[string]interface{}` members only and returns `(value, present bool, err)`: a non-object on the way
   (string, array, null) is an error for a pinned/listed path ("path X: Y is not an object"), never "absent".
   - `issue_keys`: present value must be a `string` matching `^([A-Z][A-Z0-9_]*)-([0-9]+)$` (Go `$` is end of
     text, so a trailing newline fails); when `Restrict`, group 1 must be in `Projects`. Error:
     `path P: "V" is not an in-scope issue key (projects: A, B)`; a non-string: `path P must be an issue key string`.
   - `project_keys`: present value must be a string equal to one of `Projects` (when `Restrict`), else
     `path P: project "V" is not in scope`.
   - Pinned holder: for every `issue_keys`/`project_keys` path with more than one segment whose holder (the path
     minus its last segment) is present, the holder must be a JSON object whose only member is the last segment
     (`fields.project` = `{key: ...}` only). `{id:..}`, `{key:THP,id:1}`, `{name:..}`, `"THP"` are denied:
     `path H must be an object with only "key"`. This closes the "pin by id" bypass.
   - `required`: each path must be present (`missing required path P`).
   - `absent`: each path must not be present at all, any value including null (`path P is not allowed`).
   - Absent issue/project paths pass (an edit without a parent is fine).
   - Error order is fixed (absent, required, holders, issue_keys, project_keys; each in config order) so a
     request is always told the same thing.
2. `CheckConstraints` gets `case "jira_key_scope":` building the struct. `projects` resolves like `jql_scope`
   (`$workspace.jira_projects` → `ws.Policy.JiraProjects`); `Restrict = len(Projects) > 0`, so an empty or unset
   workspace list leaves the project part unrestricted (matches `scopedIssueKey`,
   `internal/server/jira_worklog.go:407`) while shape, holder and absent checks still run. A literal list is never
   empty (load check below).
3. Load checks in `loadFromFile` via `validateJiraKeyScope(id, op)`, each failing with `operation <id>: ...`:
   `projects` missing; a literal list that is empty or has a non-string/empty item; a `$workspace.` name other
   than `jira_projects`; any path whose first segment is not a declared param, or with an empty segment or a
   `[`; a `required` path not in `issue_keys` or `project_keys`. Paths are parsed once and kept on the op
   (`op.keyScopes`, unexported) so `Check` does no parsing; the constraint map stays the source for dry-run
   messages.
4. `Operation` gets `Precheck string \`yaml:"precheck"\``. Load check: `precheck` other than `""` or
   `jira_issue_link` is an error; `jira_issue_link` needs connector `http-rest`, a declared `id` param and a
   `jira_key_scope` constraint (its `projects` is what scopes the link ends).

Constraint errors already become `403 {"error":"denied","rule":"constraint"}` with an audit deny in
`executeOp` (`internal/server/server.go:534`) and `handleDryRun` (`:459`); no server change for the constraint.

## Code: `internal/server/server.go` (pre-check)
1. `policy.ConstraintDenied{Detail string}` error type (in `internal/policy`, so the server and later prechecks
   share it).
2. `executeHttpConnector` starts, after env/base URL resolution is proven possible, with
   `if op.Precheck == "jira_issue_link" { if st, body, err := s.precheckJiraLink(ws, op, params, workspace); ... }`.
   `precheckJiraLink` builds a synthetic op `{Connector: http-rest, Site: op.Site, Method: GET,
   Path: /rest/api/2/issueLink/{id}, Params: {id: string}}` and calls `executeHttpConnector` with params
   `{id, env (if set)}` only, so it uses the same site, env, credential, proxy and OAuth refresh and no other
   param leaks into its query string. Outcomes:
   - transport error: returned as the op's error (existing error JSON, audited allow with status 0, like any
     upstream failure) and the op request is not sent;
   - GET status >= 400: return that status and body; the op request is not sent (3xx cannot reach here: Go's
     client follows redirects, and a final 3xx with no Location falls into the non-JSON / missing-key deny);
   - body not JSON, or `inwardIssue.key` / `outwardIssue.key` missing or not a string, or either out of scope
     (same regex and `Projects`/`Restrict` as the op's `jira_key_scope`, resolved by a helper
     `policy.JiraKeyScopeFor(op, ws)`): return `&policy.ConstraintDenied{Detail: "issue link <id>: <reason>"}`;
   - both in scope: fall through and send the op's request; its status and body are returned unchanged, a DELETE
     404 included.
3. `executeOp`: right after the connector call, before `LogAllow`,
   `var cd *policy.ConstraintDenied; if errors.As(err, &cd) { s.writeDeny(..., "constraint", cd.Detail); return }`.
   So a scope failure is a 403 with one audit deny and no allow record.
4. Dry-run does not run the pre-check (it never calls upstream); its response is unchanged.

## Tests
- `go test ./internal/policy -run TestJiraScope`: table over `CheckConstraints` with a workspace holding
  `jira_projects [THP]` and one with none; every named case from the acceptance, asserting the error names the path.
- `go test ./internal/policy -run TestJiraScopeLoad`: one YAML per load error, asserting `loadFromFile` fails and
  the message names the op; one valid file loads. Includes the precheck load errors: no `id` param, `id` without
  pattern `^[0-9]+$`, no `jira_key_scope`, unknown precheck name, non-http-rest connector.
- Transport-error case in `TestJiraLinkDeletePrecheck` (upstream closed): asserts the existing error JSON, one
  audit allow with status 0, and no op request.
- `go test ./internal/server -run TestJiraLinkDeletePrecheck`: `httptest` upstream recording each request; a test
  config whose Jira site points at it; cases: both ends in scope (GET then DELETE, upstream 204 returned), DELETE
  returns 404 (returned), inward out of scope / outward out of scope / key missing / key a number (403
  constraint, one audit deny, only the GET recorded), GET 404 (404 returned, only the GET recorded). Audit is
  read back from the test logger.

## Docs
`.ai/OPERATIONS.md`: a `jira_key_scope` section (fields, semantics of empty `jira_projects`, one example) and a
`precheck: jira_issue_link` section (one example, the outcomes table above).

## Risk
High: agent-facing op API and a new upstream call path. Blast radius is limited to ops that opt in; existing
constraint types and ops are unchanged (no unknown-type load error here: that is T0016).
