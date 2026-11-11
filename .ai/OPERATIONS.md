# Operations Guide

## Operation definition structure

Operations are defined in YAML files (`operations.globex.yaml`, `operations.acme.yaml`) and loaded at startup into the operation registry.

```yaml
operations:
  <operation-id>:
    connector: http-rest          # Which connector to use
    site: gitlab                  # References site in config.<ws>.yaml
    method: GET                   # HTTP method (for http-rest connector)
    path: /projects/{project_id|urlencode}/repository/branches
    params:                       # Parameter definitions
      project_id:
        type: string
        required: true
      search:
        type: string              # Optional param, no required field
      per_page:
        type: int
        default: 100
        max: 100
    body_template: |              # Optional: JSON body template for POST/PUT
      {"name":"{{name}}", "maxResults":{{maxResults}}}
    constraints:                  # Optional: validation constraints
      - type: readonly
      - type: jql_scope
    response:                     # Optional: field allowlisting
      allow_fields:
        - name
        - commit.short_id
        - commit.committed_date
```

## Path template features

### Parameter substitution

`{param_name}` — replaced with the param's string value (from request body params).

Raw `{param_name}` values are rejected if they contain `/`, `%`, `..`, `?`, `#`, `\` or control chars (path injection). A param whose real values contain `/` (Jenkins `job`, GitHub file `path`, Bitbucket `branch`) must set `multi_segment: true` in its param def; `..`, `?` etc. are still rejected. A `multi_segment` value may carry `%XX` escapes (Jenkins branch jobs: `job/repo/job/release%252F9.19`), except escapes decoding to `/`, `\`, `.`, `?`, `#` or a control char (`%2F`, `%2e`, …). Otherwise use `|urlencode`. Placeholders are expanded in a single pass, so a value containing `{other}` stays literal.

### URL encoding

`{param_name|urlencode}` — replaced with URL-path-encoded value. Essential for GitLab project IDs containing `/`.

### Site variables

- `{site.workspace_id}` — Bitbucket workspace ID
- `{site.user_id}` — Bitbucket user ID
- `{site.workspace}` — Bitbucket workspace slug

### Query parameter paths

When a path contains `?path={key}` or `&path={key}`, the value is additionally URL-query-escaped (double-encoding for path-through-query scenarios, e.g., Kibana console proxy).

## Body templates

Use `{{param_name}}` for string values, `{{param_name}}` (no quotes) for numbers:

```yaml
body_template: |
  {"query":{"bool":{"must":[{"query_string":{"query":"{{query}}"}}]}},"size":{{size}}}
```

The template is filled in one pass: text inside a substituted value (even `{{other}}`) is never substituted again,
and placeholders with no matching param render as empty.

## Extra headers and file uploads

`headers:` adds fixed request headers to one operation (e.g. `X-Atlassian-Token: no-check` for Jira uploads, or
`X-ExperimentalApi: opt-in` for experimental Service Management endpoints). Auth headers are set after them.

`multipart:` sends the body as `multipart/form-data` with one file part. The agent passes the file name and the
file's bytes base64-encoded; the gateway never reads a path from disk. Both params must be declared (give the name a
`pattern` and the content a `maxlen`), and the op cannot also have a `body_template`. The audit log records the
content param as its length only. An op call's JSON body is capped at 20 MB (HTTP 413 above that).

```yaml
multipart:
  field: file                 # form field name
  filename_param: filename
  content_param: content_base64
```

The `{{now}}` substitution is available for generating timestamps:
```yaml
body_template: |
  {"name":"gateway-login-{{now}}"}
```

## Parameter types & features

| Feature | Example | Notes |
|---------|---------|-------|
| `type: string` | JQL query, project ID | String value |
| `type: int` | `maxResults`, `per_page` | Integer (Go type conversion handled) |
| `type: float` | Numeric thresholds | Float64 |
| `type: object` | `fields` | A JSON object, rendered into `{{name}}` as raw JSON (place it unquoted in the body template). Absent keys are simply not sent, so optional fields need no empty-string placeholder |
| `type: list` | `labels`, jira.search `fields` | A JSON array, rendered into `{{name}}` as raw JSON (place it unquoted). No validation of its own: guard it with a constraint (e.g. `jira_fields`) |
| `keys: [a, b]` | `[resolution]` | For `type: object`: every key the agent sends must be listed, otherwise the request is denied |
| `pattern: '^KEY-[0-9]+$'` | Issue key scoped to one project | For `type: string`: the value must match this Go regexp (anchor it with `^`/`$`) |
| `required: true` | `project_id` | Request fails if missing |
| `default: "value"` | `per_page: 100` | Used when param not provided |
| `max:` / `maxlen:` | `maxResults: 50` | Fails if exceeded |
| `values: [a, b]` | Enum-like validation | Must be one of the listed values |
| `from:` | (not currently used) | Reserved for future use |

A parameter the operation does not declare is denied (`env`/`cluster` are always accepted). A missing or unknown
parameter is denied with rule `validation` and a detail naming the operation's parameters (`env` always among them),
so an agent's retry needs no guess: `unknown parameter: fields (valid: env, jql, maxResults, nextPageToken; required: jql)`.

## Constraints

### readonly
Marks an operation as read-only. Write operations must NOT have this constraint.

### jql_scope
For Jira JQL operations. Rewrites the `jql` param to `(<jql>) AND project in (<jira_projects>)`, so a query can never leave the workspace's `jira_projects` policy list.

A trailing `ORDER BY` is split off first and re-appended after the scope (JQL rejects it inside parentheses): `status = Open ORDER BY updated DESC` becomes `(status = Open) AND project in (A,B) ORDER BY updated DESC`. The clause may only list sort fields (`created`, `"Story Points" DESC`, `cf[10117] ASC`, comma-separated). Anything else after `ORDER BY`, and unbalanced parentheses or quotes anywhere in the JQL, are denied with rule `constraint` — a stray `)` would otherwise close the wrapper early and let an `OR (...)` branch escape the scope.

```yaml
constraints:
  - type: jql_scope
```

Workspace policy:
```yaml
policy:
  jira_projects:
    - MD
    - SW
    - Acme
```

For a second Jira whose projects differ from `jira_projects`, pin the scope in the operation with a literal list.
An empty literal list denies the request rather than leaving it unscoped:
```yaml
constraints:
  - type: jql_scope
    projects: [DESK]
```

### jira_fields
For Jira searches. Lets the caller choose which fields Jira returns, from an allowlist. It checks only the `fields`
param: absent passes (the param's `default` is sent); present must be a non-empty JSON array of strings, each in
`allowed`. Refusals are rule `constraint`, with one of two details:
- `parameter fields must be a non-empty JSON array of field names (allowed: summary, status)` (null, a string, an
  object, `[]`, a non-string item);
- `field "*all" is not allowed (allowed: summary, status)` (the first item not listed; Jira's `*all`,
  `*navigable` and `-field` forms are refused unless listed literally).

```yaml
params:
  fields:
    type: list
    default: [summary, status]
body_template: |
  {"jql":"{{jql}}","maxResults":{{maxResults}},"fields":{{fields}}}
constraints:
  - type: jira_fields
    allowed: [summary, status, customfield_10333]
response:
  allow_fields:
    - issues[].fields.summary
    - issues[].fields.status
    - issues[].fields.customfield_10333
```

The ops file fails to load (`operation <id>: jira_fields ...`) when `allowed` is missing, empty, not a list or holds
a non-string; when the op does not declare `fields` as `type: list`; when `body_template` lacks `{{fields}}` (the
value would go out as a query param, unguarded); when `fields` has no `default` (an omitted `fields` would render
`"fields":,`, invalid JSON); or when the `default` is not a non-empty list of names from `allowed`. An op built in code with a bad `allowed` refuses every request that sends `fields`.
Response shaping is unchanged: every allowed field needs an `allow_fields` entry, or its value is stripped from the
reply even though Jira returned it.

### jira_key_scope
Scopes Jira issue keys and pinned project keys at dotted paths inside the params (first segment a declared param,
later segments object members, no array indexing). Denied with 403 `rule: constraint`, the detail naming the path.

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
```

- `projects` (required): `$workspace.jira_projects` or a non-empty literal list. An empty or unset workspace list
  leaves the project part unrestricted (as the worklog ops) while every shape, object, required and absent check
  still applies.
- `issue_keys`: a present value must be a string exactly matching `^[A-Z][A-Z0-9_]*-[0-9]+$` with its project in
  `projects`. An absent path passes.
- `project_keys`: a present value must equal one of `projects`. The object holding a pinned key (`fields.project`,
  `fields.parent`) must have `key` as its only member: `{id: ..}`, `{key: .., id: ..}`, a string or null is denied.
- `required`: paths that must be present (each must also be in `issue_keys` or `project_keys`). `absent`: paths that
  must not be present at all (any value, null included).
- Walking through a non-object (`update` a string) is a deny, never "absent". Defaults apply after constraints, so a
  first-segment param may only default to `{}`.
- Load errors (`operation <id>: ...`): `projects` missing, an empty literal list, an unknown `$workspace.` field, a
  path whose first segment is not a declared param (or an empty segment or `[`), a `required` path not in
  `issue_keys`/`project_keys`, an `absent` path also in another list.

### precheck: jira_issue_link
An op-level field (not a constraint) for ops that act on an issue link by id, which carries no issue key itself.
Needs an http-rest op (no `slack_file_content`, no `multipart`), an `id` param with `pattern: '^[0-9]+$'` and a
`jira_key_scope` constraint (its `projects` scopes both ends). The gateway first GETs
`/rest/api/2/issueLink/{id}` through the same site, env, credential and proxy, then:

```yaml
jira.link_delete:
  connector: http-rest
  site: jira
  method: DELETE
  path: /rest/api/2/issueLink/{id}
  params:
    id: {type: string, required: true, pattern: '^[0-9]+$'}
  precheck: jira_issue_link
  constraints:
    - type: jira_key_scope
      projects: $workspace.jira_projects
```

| GET outcome | Result |
|-------------|--------|
| 2xx, `inwardIssue.key` and `outwardIssue.key` both in scope | the op's request is sent; its status and body are returned (a DELETE 404 included) |
| 2xx, either key out of scope, missing or not a string; body not a JSON object | 403 `constraint`, audit deny, op request not sent |
| status below 200 or 300-399 (redirects are not followed) | 403 `constraint`, op request not sent |
| status >= 400 | that status and body returned, op request not sent |
| transport error | the usual upstream error response, op request not sent |

Dry-run does not run the precheck (it never calls upstream).

### tunnel
Ensures a required VPN/tunnel is up before executing. Referenced via `requires_tunnel` in the environment config (not in the operation definition).

## Environment selection

A request targets the environment named by `env` (or `cluster`, or an op param default), else the site's
`default_env`, else the site's only environment. A site with several environments and no `default_env` denies
an env-less request with `missing required parameter: env`; an unknown name is denied too. egw never guesses.
Give a multi-environment site a default (checked at config load):
```yaml
sites:
  jira:
    default_env: prod      # never a VPN-only env
    environments:
      prod: { ... }
      corpo: { ... }
```

## Response field filtering

The `allow_fields` list in an operation definition restricts which fields are returned from upstream. Supports nested dot-notation and array wildcards:

```yaml
response:
  allow_fields:
    - hits.total
    - hits.hits[]._source.@timestamp
    - hits.hits[]._source.message
    - hits.hits[]._source.level
```

Fields not listed are stripped from the response before returning to the agent. This is wired in `server.go:executeHttpConnector()` — JSON unmarshal → `ResponseShaper.Shape()` → re-marshal. Non-JSON responses pass through unchanged.

A listed path whose upstream field is present but empty or null stays in the response: an empty array projects to `[]` (`fields.fixVersions[].name` gives `"fixVersions": []`) and a null parent projects to `null` (`fields.customfield_10340.value` gives `"customfield_10340": null`). A path the upstream never sent is omitted.

## Complete operation reference

### Globex Workspace (operations.globex.yaml)

| Operation | Connector | Site | Description |
|-----------|-----------|------|-------------|
| jira.issue | http-rest | jira | Get issue details |
| jira.comments | http-rest | jira | Get issue comments |
| jira.search | http-rest | jira | JQL search (scoped to MD/SW) |
| jira.comment | http-rest | jira | Add comment to issue |
| jira.assign | http-rest | jira | Assign issue |
| jira.transitions | http-rest | jira | Get available transitions |
| jira.status_history | http-rest | jira | Get status change history |
| jira.status | http-rest | jira | Transition issue status (**write**; `transition_id` from jira.transitions, not a name) |
| jira.me | http-rest | jira | Get current user |
| jira.create | http-rest | jira | Create issue |
| jira.edit | http-rest | jira | Edit issue fields (**write**) |
| confluence.search | http-rest | confluence | CQL search |
| confluence.page | http-rest | confluence | Get page (body.storage) |
| confluence.comments | http-rest | confluence | Get page comments |
| confluence.me | http-rest | confluence | Get current user |
| confluence.edit | http-rest | confluence | Update page (**write**) |
| confluence.export | http-rest | confluence | Export page as rendered HTML |
| mcp.tools | mcp | mcp | List Atlassian Rovo MCP tools |
| mcp.call | mcp | mcp | Invoke a Rovo MCP tool |
| bitbucket.pr | http-rest | bitbucket | Get pull request |
| bitbucket.diff | http-rest | bitbucket | Get PR diff |
| bitbucket.comments | http-rest | bitbucket | Get PR comments |
| bitbucket.commits | http-rest | bitbucket | List commits |
| bitbucket.branches | http-rest | bitbucket | List branches |
| bitbucket.create_pr | http-rest | bitbucket | Create PR |
| bitbucket.pr_by_branch | http-rest | bitbucket | Find PR by branch |
| bitbucket.update_pr | http-rest | bitbucket | Update PR |
| kibana.login | http-rest | kibana | Get ES API key |
| kibana.search | http-rest | kibana | Search logs |
| clockify.workspace | http-rest | clockify | Get workspaces |
| clockify.projects | http-rest | clockify | Get projects |
| clockify.user | http-rest | clockify | Get user |
| clockify.entries | http-rest | clockify | List time entries |
| clockify.today | http-rest | clockify | Today's entries |
| clockify.active | http-rest | clockify | Active timer |
| clockify.add | http-rest | clockify | Add time entry |
| postgres.query | postgres | postgres | Run SELECT |
| postgres.tables | postgres | postgres | List tables |
| postgres.schema | postgres | postgres | Get table schema |
| wireguard.status | local-service | wireguard | Check tunnel |
| wireguard.test | local-service | wireguard | TCP test |

### Acme Workspace (operations.acme.yaml)

| Operation | Connector | Site | Description |
|-----------|-----------|------|-------------|
| jira.issue | http-rest | jira | Get issue details |
| jira.comments | http-rest | jira | Get issue comments |
| jira.search | http-rest | jira | JQL search (scoped to Acme/SUP/OPS) |
| jira.status | http-rest | jira | Transition status (**write**; `transition_id` from jira.transitions, HTTP 204 = ok via `upstream_status`) |
| jira.transitions | http-rest | jira | Get transitions |
| jira.me | http-rest | jira | Get current user |
| jira.edit | http-rest | jira | Edit issue fields (**write**) |
| jira.worklog_report | jira-worklog | jira | Your worklogs for a day/week/month with week and month counters vs the Polish norm (`docs/WORKLOG.md`) |
| jira.worklog_issue | jira-worklog | jira | Hours spent on one issue: estimate, by author, yours, every worklog |
| jira.worklog_add | jira-worklog | jira | Log time on `key` or a mapped `activity` (**write**) |
| jira.worklog_update | jira-worklog | jira | Change one of your worklogs (**write**) |
| jira.worklog_delete | jira-worklog | jira | Delete one of your worklogs (**write**) |
| jira.worklog_activities | jira-worklog | jira | Activity → issue map for non-ticket work |
| jira.worklog_activity_set / _remove | jira-worklog | jira | Change the activity map (**write**) |
| confluence.search | http-rest | confluence | CQL search |
| confluence.page | http-rest | confluence | Get page (body.storage) |
| confluence.comments | http-rest | confluence | Get page comments |
| confluence.me | http-rest | confluence | Get current user |
| confluence.edit | http-rest | confluence | Update page (**write**) |
| confluence.export | http-rest | confluence | Export page as rendered HTML |
| mcp.tools | mcp | mcp | List Atlassian Rovo MCP tools (JSON-RPC `tools/list`) |
| mcp.call | mcp | mcp | Invoke a Rovo MCP tool (`tools/call`) |
| gitlab.mr | http-rest | gitlab | Get merge request — **metadata only** (no diff) |
| gitlab.mr_changes | http-rest | gitlab | Get MR **diff**: changed files + unified diff (the code). Use this, not `gitlab.mr`, when you need the changes. |
| gitlab.mr_comments | http-rest | gitlab | Get MR comments |
| gitlab.mr_update | http-rest | gitlab | Replace an MR's description (**write**) |
| gitlab.mr_create | http-rest | gitlab | Open an MR for a pushed branch: `project_id`, `source_branch`, `target_branch`, `title`, `remove_source_branch` (bool, default true); no description (**write**, 201) |
| gitlab.mr_note | http-rest | gitlab | Reply on an existing MR thread: `project_id`, `mr_iid` (int), `discussion_id` (from `gitlab.mr_discussions`), `body` (**write**, 201) |
| gitlab.mr_discussion_resolve | http-rest | gitlab | Resolve or unresolve a whole MR thread: `project_id`, `mr_iid` (int), `discussion_id` (40-hex), `resolved` (bool) (**write**); returns `id`, `notes[].resolved` |
| gitlab.commits | http-rest | gitlab | List commits |
| gitlab.branches | http-rest | gitlab | List branches |
| gitlab.projects | http-rest | gitlab | List/search projects |
| gitlab.whoami | http-rest | gitlab | Get current user |
| jenkins.jobs | http-rest | jenkins | List jobs/folders (`env`: dev/staging) |
| jenkins.job | http-rest | jenkins | Get job status + recent builds (+ child jobs for folders) |
| jenkins.build | http-rest | jenkins | Get build result/duration/culprits |
| jenkins.console | http-rest | jenkins | Get build console log (plain text) |
| jenkins.build_trigger | http-rest | jenkins | Start a build (**write**) |
| jenkins.build_with_params | http-rest | jenkins | Start a parameterized build, `PUBLISH` pinned to `false` (**write**) |
| kibana.login | http-rest | kibana | Get ES API key |
| kibana.search | http-rest | kibana | Search logs |
| postgres.query | postgres | postgres | Run SELECT (read-only enforced) |
| postgres.tables | postgres | postgres | List tables |
| postgres.schema | postgres | postgres | Get table schema |
| slack.auth_test | http-rest | slack | Slack identity check |
| slack.channels | http-rest | slack | List channels |
| slack.messages | http-rest | slack | Read channel messages (top-level only) |
| slack.replies | http-rest | slack | Read a message's thread replies |
| slack.file | http-rest | slack | Slack file metadata + short preview (`files.info`, by file id `F…`) |
| slack.file_content | http-rest | slack | Text content of a Slack snippet/text file by id (fetches `url_private` on files.slack.com, text only, ≤1 MB; op flag `slack_file_content`) |
| slack.send | http-rest | slack | Post a message (**write**) |
