# Connector Architecture

## Interface

All connectors implement the `Connector` interface (`internal/connectors/connector.go`):

```go
type Connector interface {
    Name() string
    Execute(ctx context.Context, req *Request) (*Response, error)
}

type Request struct {
    Method      string
    Path        string
    Headers     map[string]string
    Body        []byte
    Credentials *Credentials
}

type Credentials struct {
    Username string
    Password string
    Token    string
    APIKey   string
}
```

## Connector dispatch

In `server.go`, the operation-execution path routes to the appropriate connector based on `op.Connector` (an if/else chain, not a `switch`):

```
"http-rest"     → executeHttpConnector       → httprest.Connector
"form-session"  → executeHttpConnector       → formsession login + httprest.Connector
"postgres"      → executePostgresConnector    → postgres.Connector
"local-service" → executeLocalServiceConnector → localservice.Connector
"mcp"           → executeMcpConnector         (JSON-RPC 2.0 over Streamable HTTP)
"email"         → executeEmailConnector       → email.Connector (IMAP/SMTP)
"jira-worklog"  → executeJiraWorklog          (Tempo API worklogs + Polish working-hours norm)
```

`"form-session"` also goes to `executeHttpConnector` (EG0042): it logs in with
`formsession.Connector.Login` and sends the session cookie in place of an auth header
(see "Form Session Connector" below). Loading an ops file refuses an op whose `connector`
is missing, empty or none of the above (`operation a.b: unknown connector "http_rest"`,
EG0041); the accepted names are `knownConnectors` in `internal/policy/connectors.go`.

## HTTP REST Connector (`httprest`)

The most-used connector. Handles Jira, GitLab, Kibana, Bitbucket, Clockify.

Key behaviors:
- Copies headers from `req.Headers` to the HTTP request
- Handles Basic Auth (`Authorization: Basic base64(user:pass)`)
- Handles Bearer Auth (`Authorization: Bearer <token>`)
- Handles API Key auth (looks for `apikey` in header keys)
- Taints credentials after execution (prevents accidental log leaks)
- Returns `upstream_status` and `upstream_body` in the response

Auth types the server injects before calling this connector (`env.auth.type`): `basic`,
`bearer`, `api-key`, and `slack-session` (injects an `xoxc` Bearer token **and** an `xoxd`
`d` cookie from two secrets — `secret_ref` + `cookie_secret_ref`; see `docs/SLACK.md`).

**Important**: Credentials are typically passed via `Headers["Authorization"]` set by the server (not via `req.Credentials`). The server resolves secrets and injects the `Authorization` header before calling the connector.

Response bodies are capped at 20 MB (`httprest.DefaultMaxBodyBytes`, which also covers `form-session` data requests) unless the request
sets `MaxBodyBytes`; a larger body fails with "response exceeds N bytes". Redirects are not followed.

### Response shaping (wired)

If an operation defines `response.allow_fields`, the server applies `ResponseShaper` after receiving the upstream response. The JSON body is unmarshalled, filtered to only the allowed fields (dot-notation supported), and re-marshalled. Non-JSON responses pass through unchanged. See `internal/redact/hook.go`.

### Cross-tenant isolation

All secret resolution calls in the HTTP connector path use `ResolveWithWorkspaceCheck()` which compares the secret reference's workspace prefix against the request's workspace name. A `secret://acme/gitlab` reference cannot be resolved during a Globex workspace request.

## PostgreSQL Connector (`postgres`)

Read-only SQL execution with safety guards:

- The query is parsed to a Postgres AST (`validateQueryAST`): every statement must be a `SELECT` (incl. `WITH`, or
  `EXPLAIN` of one); anything else (DDL/DML, `COPY`, `SET`, ...) is refused
- `SELECT ... FOR UPDATE/SHARE` — refused
- Wraps execution in `BEGIN TRANSACTION READ ONLY` with a 5s `statement_timeout`, caps results at 1000 rows
- Connects using `lib/pq` driver; `sslmode` comes from the environment's `sslmode:` field (`disable|allow|prefer|require|verify-ca|verify-full`), default `prefer` (TLS if the server offers it, else plaintext). Set `sslmode: disable` to force the old behaviour.
- `pg_allowed_tables` (workspace policy): the SELECT is walked generically (every node, scalar subqueries included) with CTE names scoped to their defining SELECT; any table outside the list is refused, as are SQL-executing / server-touching functions (`query_to_xml*`, `table_to_xml*`, `dblink*`, `pg_read_*`, `pg_ls_*`, `lo_*`, `pg_sleep*`, `set_config`, ...). `postgres.tables` lists only allowed tables and `postgres.schema` refuses others. See `internal/connectors/postgres/walk.go`.
- Values interpolated into `information_schema` lookups (`postgres.tables`/`postgres.schema`) are escaped via `postgres.QuoteLiteral` — the read-only regex guard alone does **not** stop injection inside a single `SELECT`

### How to add a new PG environment

1. Add to `config.<ws>.yaml` under `sites.postgres.environments.<name>`:
   ```yaml
   new-env:
     host: hostname
     port: 5432
     databases:
       mydb:
         db: databasename
         schema: public
     secret_ref: secret://ws/pg-newenv
     requires_tunnel: tunnel-name  # optional
     sslmode: prefer               # optional; disable|allow|prefer|require|verify-ca|verify-full
   ```
2. Optionally add tables to `pg_allowed_tables` policy

## Local Service Connector (`localservice`)

Used for WireGuard tunnel checks and TCP connectivity tests.

- `wireguard.status` — runs `sc query WireGuardTunnel$<name>` to check if tunnel is running
- `wireguard.test` — TCP connect to `<host>:<port>` to verify connectivity through the tunnel
- GlobalProtect check — connected when the `PANGP Virtual Ethernet Adapter*` network adapter is up. Not the
  `PanGPS` service: it keeps running while the VPN is disconnected.

## MCP Connector (`mcp`)

Speaks JSON-RPC 2.0 over MCP Streamable HTTP to an upstream MCP server (e.g. Atlassian Rovo). Implemented inline in `server.go` (`executeMcpConnector`), reusing the `httprest` client for transport rather than a standalone connector package.

- Resolves the site's environment `base_url` and auth header (`resolveMcpAuthHeader`).
- Performs the `initialize` handshake, captures the `Mcp-Session-Id` response header, then reuses it on the follow-up call.
- Sends `Accept: application/json, text/event-stream`, so it handles both plain JSON and SSE responses.
- Operations: `mcp.tools` (`tools/list`) and `mcp.call` (`tools/call`).

## Email Connector (`email`)

IMAP read + SMTP send, implemented in `internal/connectors/email/` and dispatched via `executeEmailConnector`. Defaults to IMAP `:993` (TLS) and SMTP `:587` (TLS). Works with Gmail app passwords. Sender/mailbox access is gated by the workspace `email_allowed_senders/mailboxes` and `email_denied_senders/mailboxes` policy lists. See `docs/GMAIL.md`.

- Send: CR/LF or other control characters in subject/from/to are refused; `to` must be one bare address
  (`net/mail`), checked against the policy as sent. Non-ASCII subjects are Q-encoded.
- Read: message bodies are capped at 10 MB (`body_truncated: true`). IMAP has a 10 s dial timeout and a 60 s
  per-command deadline; on a plaintext IMAP port STARTTLS is used when the server offers it.

## Jira Worklog Connector (`jira-worklog`)

Time tracking on Tempo worklogs, in `internal/server/jira_worklog.go`; the working-day calendar is `internal/workcal`.
Several upstream calls per op, so it is not an `http-rest` op. The op's `path` names the action. Worklogs are read and
written through the Tempo REST API v4 on the workspace site `tempo` (bearer token), because Tempo writes them to Jira
under its app user. The op's site (Jira Cloud v3, default environment only) supplies the account, issue keys and the
scope check. Issue keys must be in `jira_projects`; update/delete refuse worklogs by another author. See
`docs/WORKLOG.md`.

## Form Session Connector (`formsession`)

Form-based login for session-cookie sites (CodiMD), wired through `executeHttpConnector` (EG0042, design `docs/design/EG0042-form-session-dispatch.md`). Data requests use the http-rest path (path template, query, body, headers, proxy_host, insecure_skip_verify, no redirects, 20 MB cap, `allow_fields` shaping); the package only logs in and caches sessions.

- **Config** (site environment): `base_url`, `user` (login email), `secret_ref` (login password); `auth` is ignored. Missing `user` or `secret_ref` is an error before any login. `proxy_host`/`proxy_port` and `insecure_skip_verify` apply to the login as well.
- **Login**: POST `<base>/login`, form `email` + `password`, no redirects. Success = a `connect.sid` cookie and neither 401/403/5xx nor a redirect to `/login` (a 302 to `/` with the cookie is the usual answer). The password is resolved with `secrets.ResolveWithWorkspaceCheck` on a cache miss only; the cookie is tainted.
- **Sessions** (`formsession.Sessions`): one cookie per workspace + site + resolved env, one lock per key (concurrent first calls log in once), failed logins not cached. `/admin/reload` and every `/admin/secrets` write or delete clear them.
- **Re-login**: a 401, or a 3xx whose `Location` path ends in `/login`, drops the stale cookie, logs in once more and retries once; a second signal or a failed re-login is an error. A 403 or a redirect elsewhere passes through.
- **Errors** (login, re-login, missing fields) return `status:"error"` and are audited as allow with no upstream status.
- CodiMD's exact login responses are unverified (tests use httptest only, `internal/server/form_session_test.go`).

## Adding a new connector

1. Create `internal/connectors/<name>/connector.go`
2. Implement the `Connector` interface
3. If the connector needs secrets, use:
   ```go
   secret, err := secrets.GetResolver().Resolve(workspace, name)
   ```
4. Register taint after use:
   ```go
   secrets.GetTaintRegistry().Taint(secret)
   ```
5. Wire into `server.go` by adding a case in the connector dispatch:
   ```go
   case "my-connector":
       resp, err = s.myConnector.Execute(ctx, &connectors.Request{...})
   ```
