# T0003 design: `jira.search` takes an allowlisted `fields` param

Todo: `todo/T0003.md` (acceptance there is the contract; this note says how).

## Decision
A Jira-specific constraint `jira_fields` (owner 2026-11-11: no generic `values:` on `type: list`). The op
declares `fields` as a `type: list` param with a default; the constraint holds the allowlist. Response shaping
(`allow_fields`) is unchanged and still the only thing that decides what reaches the agent.

```yaml
jira.search:
  params:
    fields:
      type: list
      default: [summary, status]
  body_template: |
    {"jql":"{{jql}}","maxResults":{{maxResults}},"fields":{{fields}}}
  constraints:
    - type: jql_scope
      projects: $workspace.jira_projects
    - type: jira_fields
      allowed: [summary, status, customfield_10333]
```

Alternatives rejected: generic list `values:` (owner); a pattern-checked comma string (Jira's POST
`/search/jql` wants an array; a GET would move the JQL into the URL); deriving the allowlist from `allow_fields`
(couples request and response policy, and `allow_fields` paths like `parent.fields.summary` don't map 1:1 to
field names).

## Code (all in `internal/policy/registry.go`)
1. `JiraFieldsConstraint{Allowed []string}` with `Check(op, params, ws)`, beside `JQLScopeConstraint`:
   - `v, ok := params["fields"]`; `!ok` -> nil (default comes later from `ApplyDefaults`, which runs after
     constraints on both the dry-run and execute paths, `server.go:454-467`, `529-549`).
   - `list, ok := v.([]interface{})`; `!ok || len(list)==0` or any item not a `string` -> shape error.
     JSON `null` decodes to a present nil, so it is refused, never treated as absent.
   - First string item not in `Allowed` -> item error. Duplicates of allowed items pass.
   - Details (exact):
     `parameter fields must be a non-empty JSON array of field names (allowed: a, b, c)`
     `field "X" is not allowed (allowed: a, b, c)`; list joined with `", "` in config order.
2. `CheckConstraints` gets `case "jira_fields":` building the constraint from `constraint["allowed"]`
   (`[]interface{}` of strings, validated at load). Ops built in code skip the load check, so a missing,
   non-list, empty or non-string `allowed` fails closed at run time: the request is refused
   (`jira_fields on <id> lists no allowed fields`), never let through. The server maps any constraint error to
   403 `rule:"constraint"` (`writeDeny`), so no server change.
3. `loadFromFile`: a helper `checkJiraFields(id, op) error` called per op, for each constraint with
   `type: jira_fields`. Errors read `operation <id>: jira_fields ...`; `NewRegistry` wraps them as
   `loading operations from <path>: ...`, so tests assert "contains". They cover: `allowed` missing / not a
   list / empty / non-string item; param `fields` undeclared or not `type: list`; a set default that is not a
   non-empty list of strings each in `allowed`. T0018 (2026-11-11) also refuses an unset default and a
   `body_template` without `{{fields}}`.
4. `registerBuiltIn` fallback `jira.search` unchanged (out of scope).

Values never break out of the body: `buildBody` renders a list with `json.Marshal` (`formatParamValue`,
`server.go:1567`). `fields` is named in the template, so it is not also added as a query param (`server.go:796`).

## Ops
- `config.example/operations.yaml`, `operations.globex.yaml`: `fields` default and `allowed` = the field names
  under `issues[].fields` in their `allow_fields`; new `body_template` carries `jql`, `maxResults`, `fields`
  (jql/maxResults move from query string to the POST body, which is what `/rest/api/3/search/jql` reads).
- `operations.thp.yaml` (gitignored): default = today's 13; allowed adds `components`, `customfield_10333`;
  `allow_fields` adds `issues[].fields.components[].name`, `issues[].fields.customfield_10333`.

## Tests
- `internal/policy/jira_fields_test.go`: `TestJiraFieldsConstraint` (table, one pass per case),
  `TestJiraFieldsConstraintLoadErrors` (temp files via `t.TempDir()`), `TestExampleJiraSearchFields`.
- `internal/server/jira_fields_test.go`: `TestJiraSearchFields` uses `newEnvFixture` (`env_select_test.go:37`)
  only for server/agent/role setup (`send`). It replaces `f.sites["jira"]` with a single-environment site
  (`prod` only, so `checkEnvSelection` needs no `env`) whose BaseURL is the test's own `httptest.Server`; that
  server has its own atomic hit counter and a mutex-guarded slice of POST bodies and returns 2 issues. The ops
  are built directly as `policy.Operation` (POST `/rest/api/3/search/jql`, `BodyTemplate`, `Response.AllowFields`,
  `Constraints` with `allowed` as `[]interface{}`), not via `addOp` (GET + `key`). No secrets needed (empty
  `Auth.Type`). The upstream body's `fields` is compared after `json.Unmarshal` with `reflect.DeepEqual`. Dry run
  is asserted for the refusal only (a dry-run allow never calls upstream). Control op without the constraint.
- `.ai/OPERATIONS.md` param-type table also gets a `list` row (JSON array, rendered as JSON into a body
  template, no validation of its own).
- Run: `go vet ./...`, `go test ./internal/policy/ ./internal/server/`.

## Docs
`.ai/OPERATIONS.md` constraint section gains `jira_fields`.
