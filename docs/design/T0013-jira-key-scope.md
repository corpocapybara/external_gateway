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
   - A walk that meets a non-object before the last segment (e.g. `fields` is a string, `fields.project` is
     null or an array) is a deny for every list, `absent` and `required` included (`path P: Y is not an
     object`), never "absent": a probe through a non-object must not pass.
   - Error order is fixed (absent, required, holders, issue_keys, project_keys; each in config order) so a
     request is always told the same thing.
   - Defaults are applied after constraints (`server.go:549`), so a param default is never scoped; the load
     check below refuses a default other than `{}` on any param that is the first segment of a listed path.
2. `CheckConstraints` gets `case "jira_key_scope":` (after T0003's `case "jira_fields":`) building the struct
   from the constraint map on every call, as `jira_fields` does: no parse cache on the op, so an `Operation`
   literal built by a test is checked exactly like a loaded one. A malformed map at check time (paths not a
   string list, missing `projects`) is an error, so `Check` fails closed. `projects` resolves like `jql_scope`
   (`$workspace.jira_projects` → `ws.Policy.JiraProjects`); `Restrict = len(Projects) > 0`, so an empty or unset
   workspace list leaves the project part unrestricted (matches `scopedIssueKey`,
   `internal/server/jira_worklog.go:407`) while shape, holder and absent checks still run. A literal list is never
   empty (load check below).
3. Load checks in `loadFromFile` via `validateJiraKeyScope(id, op)`, each failing with `operation <id>: ...`:
   `projects` missing; a literal list that is empty or has a non-string/empty item; a `$workspace.` name other
   than `jira_projects`; any path whose first segment is not a declared param, or with an empty segment or a
   `[`; a `required` path not in `issue_keys` or `project_keys`; a path in `absent` that is also in another
   list; a default other than `{}` on a first-segment param. Called from `loadFromFile` after T0003's
   `checkJiraFields`.
4. `Operation` gets `Precheck string \`yaml:"precheck"\``. Load check: `precheck` other than `""` or
   `jira_issue_link` is an error; `jira_issue_link` needs connector `http-rest`, no `slack_file_content` and no
   `multipart` (those route around `executeHttpConnector`), a declared `id` param with pattern `^[0-9]+$`, and
   a `jira_key_scope` constraint (its `projects` is what scopes the link ends).
5. The key regex is `policy.JiraIssueKeyRe` (exported, `^([A-Z][A-Z0-9_]*)-[0-9]+$` per the acceptance) and the
   pre-check uses it. `internal/server/jira_worklog.go:44` keeps its own stricter `-[1-9][0-9]*` pattern:
   aliasing would change worklog behaviour, which is out of scope.

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
   - GET status >= 400: return that status and body; the op request is not sent;
   - GET status 1xx/3xx (the httprest client does not follow redirects, `internal/connectors/httprest/
     connector.go:25-45`): `ConstraintDenied` ("issue link <id>: unexpected status N"); only 2xx goes on;
   - 2xx body not JSON, `inwardIssue`/`outwardIssue` missing, null or not an object, their `key` missing or not
     a string, or either key out of scope
     (same regex and `Projects`/`Restrict` as the op's `jira_key_scope`, resolved by a helper
     `policy.JiraKeyScopeFor(op, ws)`): return `&policy.ConstraintDenied{Detail: "issue link <id>: <reason>"}`;
   - both in scope: fall through and send the op's request; its status and body are returned unchanged, a DELETE
     404 included.
3. `executeOp`: right after the connector call, before `LogAllow`,
   `var cd *policy.ConstraintDenied; if errors.As(err, &cd) { s.writeDeny(..., "constraint", cd.Detail); return }`.
   So a scope failure is a 403 with one audit deny and no allow record.
4. Dry-run does not run the pre-check (it never calls upstream); its response is unchanged.

## Tests
Wall time of `go test ./internal/policy ./internal/server` is measured before and after and reported; all
cases are table rows against one httptest server, no loops or sleeps.
- `go test ./internal/policy -run TestJiraScope`: table over `CheckConstraints` (on `Operation` literals) with a
  workspace holding `jira_projects [THP]` and one with none; every named case from the acceptance, asserting
  the error names the path. Extra rows: leaf array / bool / "" for issue and project keys; full-width
  `ＴＨＰ-１`; holder null and holder array; walk through a non-object for an `absent` and a `required` path;
  `update` omitted (no spurious deny).
- `go test ./internal/policy -run TestJiraScopeLoad`: one YAML per load error, asserting `loadFromFile` fails and
  the message names the op; one valid file loads. Includes the precheck load errors: no `id` param, `id` without
  pattern `^[0-9]+$`, no `jira_key_scope`, unknown precheck name, non-http-rest connector, precheck with
  `multipart`; plus empty path segment, `[` in a path, a path in both `absent` and `issue_keys`, a non-`{}`
  default on a first-segment param.
- `go test ./internal/server -run TestJiraLinkDeletePrecheck`: its own fixture (not `newEnvFixture`, whose
  upstream is fixed): a recording `httptest` server whose handler is set per case, a workspace with
  `jira_projects [THP]` and a `jira` site with two environments (`prod`, `stg`, `default_env: prod`) on two
  recording servers, an op `jira.link_delete` as in the YAML above, a granted agent. Cases, each asserting the
  recorded request sequence:
  - both ends in scope: GET then DELETE, upstream 204 returned; the GET has an empty `RawQuery` although the call
    carried an extra param;
  - `env: stg`: both GET and DELETE hit the stg server, none the prod one;
  - DELETE answers 404: 404 returned;
  - inward out of scope, outward out of scope, key missing, key a number, `{}` body, `inwardIssue: null`,
    non-JSON 200 body, GET 302: 403 constraint, only the GET recorded, one audit deny and no allow for the
    request id;
  - GET 404: 404 returned, only the GET recorded;
  - transport error (the upstream closed before the call): the existing error JSON, one audit allow with
    status 0, nothing recorded;
  - id `12\n` and id as a JSON number: 403 validation, nothing recorded;
  - dry-run of jira.edit-like op with key ABC-1: 403 constraint (proves dry-run denies too).
  Audit readback: `audit.Logger` gets `func (l *Logger) Dir() string`; the test reads every `audit_*.log` in `audit.GetLogger().Dir()` and filters by the response's `X-Request-ID`, since
  `audit.Init` is process-global and the directory is shared across the package's tests.
- A very large GET body is read whole (connector behaviour today); accepted: the upstream is the workspace's own
  Jira, and no stress test is added.

## Docs
`.ai/OPERATIONS.md`: a `jira_key_scope` section (fields, semantics of empty `jira_projects`, one example) and a
`precheck: jira_issue_link` section (one example, the outcomes table above).

## Risk
High: agent-facing op API and a new upstream call path. Blast radius is limited to ops that opt in; existing
constraint types and ops are unchanged (no unknown-type load error here: that is T0016).
