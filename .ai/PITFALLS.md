# Pitfalls & Known Issues

## Critical bugs we've fixed

### `{param|urlencode}` path template not resolved (fixed 2026-07-09)

**Location**: `internal/server/server.go`, `executeHttpConnector()` function

**Symptom**: All GitLab operations using `{project_id|urlencode}` in their path (`gitlab.branches`, `gitlab.mr`, `gitlab.commits`, `gitlab.mr_comments`) returned 404 for every project — even public ones.

**Root cause**: The path template replacement loop iterated over `params` keys (e.g., `project_id`), checked if the key had `|urlencode` suffix (it never does — params are plain names), then replaced `{project_id}` in the path. But the template contained `{project_id|urlencode}`, not `{project_id}`, so the replacement was a no-op. The literal string `{project_id|urlencode}` was sent as the project name to GitLab.

**Fix**: Added a check before the standard replacement that looks for `{key|urlencode}` in the path template, URL-encodes the value, and replaces the full template placeholder.

### Query parameters never appended to GET URLs (fixed 2026-07-09)

**Location**: `internal/server/server.go`, `executeHttpConnector()` function

**Symptom**: `gitlab.projects` ignored `membership=true` and `per_page` parameters. The gateway built `baseURL + path` but never appended query string parameters from the `params` map.

**Root cause**: After path template replacement, params like `membership`, `per_page`, `search` existed in the params map but were never converted to URL query parameters.

**Fix**: After path replacement, scan all params that weren't consumed by path templates (excluding `env`/`cluster` routing params), build `url.Values`, and append to the URL.

### Secret empty check was a compile error (fixed 2026-07-09)

**Location**: `internal/secrets/resolver.go`, `checkEmpty()` and its call site

**Symptom**: Build failed with `assignment mismatch: 1 variable but checkEmpty returns 2 values`.

**Root cause**: `checkEmpty()` was defined as `func(secret []byte) ([]byte, error)` but called as `if err := checkEmpty(secret)` — discarding the first return value.

**Fix**: Changed call site to `if _, err := checkEmpty(secret)`.

### `len <= 1` empty detection (fixed 2026-07-09)

The resolver now treats `len(secret) <= 1` as "secret not set". This catches both empty strings and single-null-byte secrets that result from incomplete admin API writes.

### Old Go type assertion on concrete type (fixed 2026-07-09)

**Location**: `internal/redact/hook.go`, `getNestedValue()`

**Symptom**: Build failed with `invalid operation: current (variable of type map[string]interface{}) is not an interface` on Go <1.18.

**Root cause**: `current` was declared as `map[string]interface{}` then passed to `current.(map[string]interface{})` — a type assertion on a concrete type.

**Fix**: Changed `current := data` to `var current interface{} = data` so the type assertion works.

### Response shaper collapsed `field[].sub` to the first array element (fixed 2026-07-10)

**Location**: `internal/redact/hook.go`, `ResponseShaper`

**Symptom**: Any operation whose `response.allow_fields` used the `[]` marker returned only the **first** array element — `jira.search` one issue, `kibana.search` one hit, `slack.channels`/`slack.messages` one item — regardless of `limit`.

**Root cause**: `splitPath` discarded the `[]` marker, so `channels[].id` parsed identically to `channels.id`. `getNestedValue` then walked into the array and returned the *first* element's field; `setNestedValue` wrote a single object.

**Fix**: `splitPath` now preserves `[]` as a segment; a recursive `project` maps the remaining path over every array element (index-aligned) and `deepMerge` collects multiple `allow_fields` onto the same elements. Covered by `internal/redact/hook_test.go`. Numeric indices (`[0]`) remain unsupported (no operation uses them).

## Design gotchas

### `env` and `cluster` params are routing-only

The `env` param selects which environment from a site's configuration to use (base_url, auth, proxy, etc.). It is NOT sent upstream. Same for `cluster` (used in Globex Kibana). Guard against accidentally using these as upstream parameters.

### Token SHAs must match config exactly

The config stores `token_sha256` (SHA256 hex digest). If a token is regenerated, the SHA must be recomputed and the config updated. The deploy script must NOT patch SHAs — config is the source of truth.

### Credential Manager: per-SYSTEM, not per-user

Secrets are stored under `egw/<workspace>/<name>` in the SYSTEM account's Windows Credential Manager (the service runs as LocalSystem). Setting secrets via `egw-admin.exe` POSTs to the gateway's admin API, which writes to the SYSTEM store. Direct `cmdkey` or per-user vault operations won't affect the gateway.

### Admin API auth requires `admin_token_sha256` in config

**Fixed**: `authenticateAdmin()` now validates the Bearer token SHA against `config.admin_token_sha256`. If the field is empty/unset, admin auth is bypassed for backwards compatibility. To lock down admin: set `admin_token_sha256` in the config file, deploy, restart, and pass the token as `Authorization: Bearer <token>` on all admin requests.

### Cross-tenant isolation checks against workspace name, not site name

`ResolveWithWorkspaceCheck(wsName, secretName, workspace)` compares the secret reference's workspace prefix (e.g. `acme` in `secret://acme/gitlab`) against the request's workspace name (the `:workspace` URL segment). If they don't match, resolution fails. The third argument must be the workspace name string, not `ws.Site`.

### Config hot-reload replaces in-memory state atomically

`POST /admin/reload` loads config and ops from disk, validates both, then atomically swaps `s.cfgPtr` (`atomic.Pointer`) and replaces `s.reg`. If either load fails, the running state is unchanged. All config reads go through `s.cfg()` which calls `s.cfgPtr.Load()`.

### Empty base_url now returns a clear error at request time

Before, an unset `base_url` silently caused the upstream URL to be malformed (e.g. just `/path`). Now `executeHttpConnector` checks both `baseURL == ""` and `baseURL == "-"` and returns a descriptive error mentioning the environment name.

### Response shaping is now wired

`op.Response.AllowFields` is no longer a no-op. After receiving the upstream JSON response, the gateway unmarshals, applies `ResponseShaper.Shape()` to keep only allowed fields, and re-marshals. Non-JSON responses pass through unchanged.

### Windows service restart requires Admin

`Restart-Service`, `Stop-Service`, `Start-Service` all require Administrator privileges. The gateway processes run as LocalSystem. Deployment scripts must be run elevated.

### PowerShell 5.1 UTF-8 corruption

`Set-Content`/`Get-Content` without `-Encoding UTF8` corrupts non-ASCII characters. Always use `-Encoding UTF8`. JSON config files must stay valid UTF-8.

### Jira API version: /rest/api/3/ required

Jira Cloud deprecated `/rest/api/2/search`. All Jira operations in both workspaces use `/rest/api/3/` paths. Do not use v2 endpoints.

### GitLab project ID encoding

GitLab API requires URL-encoded project paths: `group/project` → `group%2Fproject`. The `{project_id|urlencode}` template handle is the correct way — don't pre-encode values in params.

### PostgreSQL SQL guard uses regex, not AST

The read-only enforcement in `postgres/connector.go` uses `\b` word-boundary regex to block DDL/DML keywords. This is simpler than a full SQL parser but can miss edge cases (e.g., SQL keywords in string literals). The solution proposal calls for `pg_query_go` AST parsing as a future enhancement.

## Stalled / incomplete features

- **Clockify auto-PR scheduler**: Stub in `scheduler.go` — returns "not implemented; requires POST /admin/jobs endpoint and connector wiring"
- **Postgres proxy/Teleport**: Not implemented — direct connections only
- **Unit tests**: only `internal/redact/hook_test.go` (response-shaper array projection) exists; the rest of the project is untested
- **No rate limiting**
- **formsession connector** exists but is unused
- **Response shaping array paths**: wildcard `[]` now projects across all elements (fixed 2026-07-10); numeric indices (`[0]`) are still unsupported
- **Hotmail/Outlook OAuth2**: Removed from the affected workspace (2026-07-16). Consumer Outlook accounts now require XOAUTH2 for both IMAP and SMTP. go-imap `Login()` and net/smtp `PlainAuth` are rejected. Re-add when XOAUTH2 support is implemented.

## YAML content issues (2026-07-16)

### Trailing whitespace breaks YAML parsing

A `config.<ws>.yaml` failed with `"line <N>: did not find expected key"` after edits that left trailing spaces on certain lines. Go's `yaml.v3` parser is strict about trailing whitespace. Fix: trim all trailing spaces before saving.

### `@` in YAML values must be quoted

Field names like `@microsoft.graph.downloadUrl` in `response.allow_fields` need quoting (`"@microsoft.graph.downloadUrl"`). `@` is a reserved YAML directive character and causes `"found character that cannot start any token"`.

## Session pitfalls (2026-07-16)

### Email dispatch: ops named `gmail.search`, not `email.search`

Operations are named `<site>.<action>` (e.g. `gmail.list_mailboxes`, `hotmail.read`). The email dispatch used `switch op.ID` with exact strings like `case "email.list_mailboxes":`. Since `op.ID` is `gmail.list_mailboxes` or `hotmail.list_mailboxes`, all email ops hit the `default:` case returning `"unknown email operation"`. Fix: extract the action suffix (everything after first `.`) and switch on that instead.

### SMTP on port 587: STARTTLS, not direct TLS

Port 587 uses plain TCP + `STARTTLS` command. Port 465 uses direct TLS (`tls.Dial`). Using `tls.Dial("tcp", "host:587", tlsCfg)` produces `"tls: first record does not look like a TLS handshake"`. Fix: `net.Dial` + `StartTLS(...)`.

### `buildBody` leaves unreplaced literals for optional params

When a `body_template` includes `{{optional_param}}` and the param is not in the request, the literal `{{optional_param}}` stays in the body. Clockify rejects `"projectId":"{{project_id}}"` as an invalid project ID. Fix: post-loop sanitization that strips remaining `{{...}}` placeholders.

### Admin handler: missing `case "reload":` after adding `diag`

When inserting a new `case "diag":` between `secrets` and `reload` in the admin switch, the `case "reload":` label was eaten (it was at the boundary of the replaced string). The reload code ran as dead code inside the diag case body. Symptom: `POST /admin/reload` returned `"unknown admin endpoint"` even though the root help page listed it. Fix: always verify all `case` labels remain after editing switch statements.

### Deploy script overwrites deployed config — set `token_sha256` in source

`update.ps1` copies `config.<x>.yaml` over the deployed `config.yaml`. If `token_sha256` was only set in the deployed copy (not the source), each deploy wipes it and the service fails to start with `"agent has no token_sha256"`. Fix: keep `token_sha256` set in the source `config.<x>.yaml` file.

### `{site.org}` path template not auto-resolved

The path resolver only handled `{site.workspace_id}`, `{site.user_id}`, `{site.workspace}`. `{site.org}` and `{site.group}` were not replaced, leaving literal `{site.org}` in URLs. GitHub operations like `/repos/{site.org}/{repo}/issues` broke. Fix: add `strings.Replace(path, "{site.org}", env.Org, -1)` and `{site.group}`.

### Jenkins CSRF crumb required for POST operations

Jenkins with CSRF protection rejects POST requests without a `Jenkins-Crumb` header. GET ops work fine. Fix: fetch `/crumbIssuer/api/json` with the same auth headers, then include the crumb in the main request header.

### Port conflicts from `deploy/update.ps1`

The deploy script assigns ports alphabetically from a base of 8443. Adding a new workspace config with an explicit `server.port` that collides with another workspace's auto-assigned port creates a conflict. Pin an explicit `server.port` in every config to keep ports stable and conflict-free.

### `clockify.add` missing body_template — empty POST silently fails

Without `body_template`, the POST to Clockify's time entry endpoint had an empty body. Clockify returned HTTP 200 with an empty response (no error, no entry created). Seven attempts with different body formats all produced the same result. Fix: add `body_template` with the JSON payload.

### Hotmail/Outlook consumer accounts require XOAUTH2

Newer consumer Outlook.com accounts reject basic auth for both IMAP (`"AUTHENTICATE failed."`) and SMTP (`"Unrecognized authentication type"` — AUTH PLAIN not supported). Gmail still works with app passwords via both IMAP and SMTP. Hotmail removed from the affected workspace pending XOAUTH2 support.
