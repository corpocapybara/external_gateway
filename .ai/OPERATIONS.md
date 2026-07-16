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
| `required: true` | `project_id` | Request fails if missing |
| `default: "value"` | `per_page: 100` | Used when param not provided |
| `max:` / `maxlen:` | `maxResults: 50` | Fails if exceeded |
| `values: [a, b]` | Enum-like validation | Must be one of the listed values |
| `from:` | (not currently used) | Reserved for future use |

## Constraints

### readonly
Marks an operation as read-only. Write operations must NOT have this constraint.

### jql_scope
For Jira JQL operations. Validates that the JQL string contains at least one project from the workspace's `jira_projects` policy list.

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

### tunnel
Ensures a required VPN/tunnel is up before executing. Referenced via `requires_tunnel` in the environment config (not in the operation definition).

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
| jira.status | http-rest | jira | Transition issue status |
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
| jira.status | http-rest | jira | Transition status (**write**) |
| jira.transitions | http-rest | jira | Get transitions |
| jira.me | http-rest | jira | Get current user |
| jira.edit | http-rest | jira | Edit issue fields (**write**) |
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
| gitlab.commits | http-rest | gitlab | List commits |
| gitlab.branches | http-rest | gitlab | List branches |
| gitlab.projects | http-rest | gitlab | List/search projects |
| gitlab.whoami | http-rest | gitlab | Get current user |
| jenkins.jobs | http-rest | jenkins | List jobs/folders (`env`: dev/staging) |
| jenkins.job | http-rest | jenkins | Get job status + recent builds (+ child jobs for folders) |
| jenkins.build | http-rest | jenkins | Get build result/duration/culprits |
| jenkins.console | http-rest | jenkins | Get build console log (plain text) |
| jenkins.build_trigger | http-rest | jenkins | Start a build (**write**) |
| kibana.login | http-rest | kibana | Get ES API key |
| kibana.search | http-rest | kibana | Search logs |
| postgres.query | postgres | postgres | Run SELECT (read-only enforced) |
| postgres.tables | postgres | postgres | List tables |
| postgres.schema | postgres | postgres | Get table schema |
| slack.auth_test | http-rest | slack | Slack identity check |
| slack.channels | http-rest | slack | List channels |
| slack.messages | http-rest | slack | Read channel messages (top-level only) |
| slack.replies | http-rest | slack | Read a message's thread replies |
| slack.send | http-rest | slack | Post a message (**write**) |
