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
"postgres"      → executePostgresConnector    → postgres.Connector
"local-service" → executeLocalServiceConnector → localservice.Connector
"mcp"           → executeMcpConnector         (JSON-RPC 2.0 over Streamable HTTP)
"email"         → executeEmailConnector       → email.Connector (IMAP/SMTP)
```

`form-session`/`formsession.Connector` is implemented but **not** in the dispatch
chain — no operation routes to it. An `op.Connector` value that matches none of the
above falls through with an empty body and status `0`.

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

### Response shaping (wired)

If an operation defines `response.allow_fields`, the server applies `ResponseShaper` after receiving the upstream response. The JSON body is unmarshalled, filtered to only the allowed fields (dot-notation supported), and re-marshalled. Non-JSON responses pass through unchanged. See `internal/redact/hook.go`.

### Cross-tenant isolation

All secret resolution calls in the HTTP connector path use `ResolveWithWorkspaceCheck()` which compares the secret reference's workspace prefix against the request's workspace name. A `secret://acme/gitlab` reference cannot be resolved during a Globex workspace request.

## PostgreSQL Connector (`postgres`)

Read-only SQL execution with safety guards:

- `\b(?:INSERT|UPDATE|DELETE|DROP|CREATE|ALTER|TRUNCATE|GRANT|REVOKE|COPY|VACUUM|REINDEX)\b` — blocks DDL/DML
- `FOR UPDATE` — blocked
- Multiple statements rejected (any internal or trailing `;`)
- Only `SELECT` and `WITH` (CTE) statements are allowed
- Wraps execution in `BEGIN TRANSACTION READ ONLY` with a 5s `statement_timeout`, caps results at 1000 rows
- Connects using `lib/pq` driver with `sslmode=disable` (traffic runs over a WireGuard tunnel, not TLS)
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
   ```
2. Optionally add tables to `pg_allowed_tables` policy

## Local Service Connector (`localservice`)

Used for WireGuard tunnel checks and TCP connectivity tests.

- `wireguard.status` — runs `sc query WireGuardTunnel$<name>` to check if tunnel is running
- `wireguard.test` — TCP connect to `<host>:<port>` to verify connectivity through the tunnel
- GlobalProtect check — runs `sc query PanGPS` for VPN status

## MCP Connector (`mcp`)

Speaks JSON-RPC 2.0 over MCP Streamable HTTP to an upstream MCP server (e.g. Atlassian Rovo). Implemented inline in `server.go` (`executeMcpConnector`), reusing the `httprest` client for transport rather than a standalone connector package.

- Resolves the site's environment `base_url` and auth header (`resolveMcpAuthHeader`).
- Performs the `initialize` handshake, captures the `Mcp-Session-Id` response header, then reuses it on the follow-up call.
- Sends `Accept: application/json, text/event-stream`, so it handles both plain JSON and SSE responses.
- Operations: `mcp.tools` (`tools/list`) and `mcp.call` (`tools/call`).

## Email Connector (`email`)

IMAP read + SMTP send, implemented in `internal/connectors/email/` and dispatched via `executeEmailConnector`. Defaults to IMAP `:993` (TLS) and SMTP `:587` (TLS). Works with Gmail app passwords. Sender/mailbox access is gated by the workspace `email_allowed_senders/mailboxes` and `email_denied_senders/mailboxes` policy lists. See `docs/GMAIL.md`.

## Form Session Connector (`formsession`)

Exists but is **not wired** into any operation. Would handle form-based login to obtain session cookies (intended for CodiMD). Implementation is present in `internal/connectors/formsession/connector.go` but unused.

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
