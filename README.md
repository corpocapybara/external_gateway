# external_gateway

A hardened, compiled proxy that lets LLM agents call external services (Jira, GitLab, Kibana, PostgreSQL, Bitbucket, Confluence, Slack, Jenkins, Authentik, GitHub, Trello, Gmail)
**without ever seeing the credentials** and **only through explicitly allow-listed operations**.

## Quick Start

```powershell
# 1. Build
& "C:\Program Files\Go\bin\go.exe" build -o egw.exe      ./cmd/egw/
& "C:\Program Files\Go\bin\go.exe" build -o egw-admin.exe ./cmd/egw-admin/
& "C:\Program Files\Go\bin\go.exe" build -o egwctl.exe    ./cmd/egwctl/

# 2. Run in foreground (pick a workspace)
.\egw.exe --config config.example.yaml --ops operations.example.yaml --port 8443

# 3. Test it
Invoke-RestMethod http://127.0.0.1:8443/healthz

# 4. Set up secrets (gateway must be running)
.\egw-admin.exe -workspace example -name jira -value "your-jira-token"
```

## Prerequisites

- **Go 1.23+** — installed at `C:\Program Files\Go\bin\go.exe`
- **Windows** — the gateway uses Windows Credential Manager and runs as a Windows service
- **Administrator** access for service installation and tunnel setup
- **WireGuard** or **GlobalProtect** for VPN-locked environments

## Build

```powershell
& "C:\Program Files\Go\bin\go.exe" build -o egw.exe      ./cmd/egw/
& "C:\Program Files\Go\bin\go.exe" build -o egw-admin.exe ./cmd/egw-admin/
& "C:\Program Files\Go\bin\go.exe" build -o egwctl.exe    ./cmd/egwctl/
```

Produces three binaries in the project root. Pass `-v` for verbose logging.

## Configure

A workspace is a config file + an operations file, named by an opaque token `<x>`:
`config.<x>.yaml` and `operations.<x>.yaml`. The example setup ships two:

| Workspace | Config | Operations |
|-----------|--------|------------|
| Acme | `config.acme.yaml` | `operations.acme.yaml` |
| Globex | `config.globex.yaml` | `operations.globex.yaml` |

`<x>` is the single source of truth: deploy dir, `egw-<x>` service, Credential
Manager namespace `egw/<x>/…`, the `/v1/workspaces/<x>/…` API path, and the port
(8443 + alphabetical index, override per-config via `server.port`, conflicts
abort) all derive from it. Add a workspace by dropping in a `config.<x>.yaml` —
`deploy/*.ps1` discover the rest. See `.ai/RULES.md` (Workspace convention section).

### Key config fields

```yaml
server:
  listen_addr: "127.0.0.1"   # listen address
  tls_cert: ""               # TLS cert path (optional)
  tls_key: ""                # TLS key path (optional)

admin_token_sha256: ""       # if set, all /admin/* endpoints require
                              # Authorization: Bearer <token> with SHA256 match

agents:                      # tokens that agents use to call the gateway
  - id: team-lead
    token_sha256: "abc123..."
    grants:
      - workspace: acme
        role: acme-lead

roles:
  acme-lead:
    operations:
      - jira.search
      - gitlab.mr
      - gitlab.branches
```

### Configuring admin auth (optional)

```powershell
# Generate an admin token
$adminToken = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$sha = [Security.Cryptography.SHA256]::Create()
$hash = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($adminToken))).Replace("-", "").ToLower()
Write-Host "Token: $adminToken`nSHA256: $hash"
```

Set `admin_token_sha256: <hash>` in the config, deploy, restart. All `/admin/*` calls now require `Authorization: Bearer <token>`.

## Set secrets

Secrets are stored in the **SYSTEM account's** Windows Credential Manager. The gateway (running as `LocalSystem`) resolves them at runtime.

### Via egw-admin.exe (gateway must be running)

```powershell
.\egw-admin.exe -workspace acme -name jira -value "<token>"
.\egw-admin.exe -workspace acme -name gitlab -value "<gitlab-pat>"
.\egw-admin.exe -workspace acme -name pg-dev -value "<password>"
```

Flags: `-workspace`, `-name`, `-value` (reads from stdin if empty), `-action` (set/delete), `-token` (required if `admin_token_sha256` is configured), `-admin` (default: `http://127.0.0.1:8443/admin`)

### Via direct API

```powershell
$body = @{workspace = "acme"; name = "kibana-prod"; value = "password"} | ConvertTo-Json
Invoke-RestMethod -Uri "http://127.0.0.1:8444/admin/secrets" -Method Post `
  -Body $body -ContentType "application/json"
```

If `admin_token_sha256` is configured, add `-Headers @{"Authorization" = "Bearer <token>"}`.

The endpoint also accepts `PUT`.

### Required secrets per workspace

Secrets are referenced in config by the pattern `secret://<workspace>/<name>`.
The actual list varies per deployment. Example secret references found in configs:

**Acme:**
- `secret://acme/jira` — Jira API token
- `secret://acme/gitlab` — GitLab PAT
- `secret://acme/kibana-prod` — Kibana/ES password
- `secret://acme/kibana-stg` — Kibana staging password
- `secret://acme/kibana-dev` — Kibana dev password
- `secret://acme/pg-dev` — PostgreSQL password
- `secret://acme/pg-stg` — PostgreSQL password
- `secret://acme/pg-prod` — PostgreSQL password
- `secret://acme/jenkins-dev` — Jenkins API token
- `secret://acme/jenkins-staging` — Jenkins API token
- `secret://acme/slack-xoxc` — Slack browser session token (`xoxc-…`) — see `docs/SLACK.md`
- `secret://acme/slack-xoxd` — Slack `d` session cookie (`xoxd-…`)

> Slack uses a session token instead of an app/bot. Provision (and rotate) both secrets
> with `tools/slack-provision/Provision-Slack.ps1` — do not set them by hand.

**Globex:**
- `secret://globex/jira` — Jira API token
- `secret://globex/bitbucket` — Bitbucket app password
- `secret://globex/kibana-umbrella` — Kibana/ES password
- `secret://globex/kibana-globex` — Kibana/ES password
- `secret://globex/clockify` — Clockify API key
- `secret://globex/pg-staging` — PostgreSQL password
- `secret://globex/pg-staging-admin` — PostgreSQL password
- `secret://globex/authentik-token` — Authentik API token
- `secret://globex/slack-xoxc` — Slack session token
- `secret://globex/slack-xoxd` — Slack session cookie

## Run

### Foreground (development)

```powershell
.\egw.exe --config config.acme.yaml --ops operations.acme.yaml --port 8443
.\egw.exe --config config.globex.yaml --ops operations.globex.yaml --port 8444
```

### Windows Service (production)

**Install** (run as Administrator):

```powershell
.\egw.exe --install --config "C:\Program Files\external_gateway\acme\config.yaml" `
  --ops "C:\Program Files\external_gateway\acme\operations.yaml" --svc-name egw-acme

.\egw.exe --install --config "C:\Program Files\external_gateway\globex\config.yaml" `
  --ops "C:\Program Files\external_gateway\globex\operations.yaml" --svc-name egw-globex --port 8444
```

**Manage:**

```powershell
Start-Service egw-acme
Stop-Service egw-globex
Restart-Service egw-acme
Get-Service egw-*

.\egw.exe --remove --svc-name egw-acme   # uninstall
```

Services run as `LocalSystem`. The `ImagePath` includes `--config`, `--ops`, `--port` flags.
Default port is 8443. Default `--svc-name` is `external_gateway`.

### Deploy script

```powershell
# Copies binaries + configs + ops to C:\Program Files\external_gateway\{acme,globex}\
# and restarts both services. Run as Administrator.
.\deploy\update.ps1
```

## Test it works

```powershell
Health check:
Invoke-RestMethod http://127.0.0.1:8443/healthz

Who am I (needs auth):
Invoke-RestMethod http://127.0.0.1:8443/whoami `
  -Headers @{"Authorization" = "Bearer <agent-token>"}

List capabilities:
Invoke-RestMethod "http://127.0.0.1:8443/capabilities?workspace=acme" `
  -Headers @{"Authorization" = "Bearer <agent-token>"}

Execute an operation:
$body = @{env = "prod"; project_id = "acmeco/core"} | ConvertTo-Json
$r = Invoke-RestMethod "http://127.0.0.1:8444/v1/workspaces/acme/op/gitlab.branches" `
  -Method Post -Body $body -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer <token>"}
```

## Agent Tokens

Token values are stored in `agent-keys.txt` (gitignored, never committed). The config stores SHA256 hashes:

```yaml
agents:
  - id: team-lead
    token_sha256: "abc123..."  # SHA256 of the bearer token
```

To generate a token: `go run ./cmd/tools/gen-token`, then place the SHA256 in config.

## API Reference

All endpoints that return JSON. Operations require `Authorization: Bearer <token>`.

### Public (no auth)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Service alive check |
| `/help` | GET | Plain-text summary of workspaces/operations |
| `/feedback` | POST | Submit feedback (stored to `feedback.log`) |

### Agent (needs bearer token)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/whoami` | GET | Current agent identity, roles, grants |
| `/capabilities?workspace=<ws>` | GET | Operation schema + config checksum + feature status |
| `/v1/workspaces/<ws>/op/<op-id>` | POST, GET | Execute an operation |
| `?dry_run=true` | — | Validate params + grants, skip upstream call |

### Admin (needs admin token if configured)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/admin/` | GET | List available admin endpoints |
| `/admin/secrets` | POST, PUT | Set a secret in SYSTEM Credential Manager |
| `/admin/reload` | POST | Hot-reload config + ops from disk |

### egwctl CLI

```powershell
.\egwctl.exe -server "http://127.0.0.1:8443" -token "<token>" healthz
.\egwctl.exe -token "<token>" whoami
.\egwctl.exe -token "<token>" capabilities
.\egwctl.exe -token "<token>" -workspace acme -op gitlab.branches -params '{"env":"prod","project_id":"acmeco/core"}'
```

## File Structure

```
external_gateway/
├── cmd/
│   ├── egw/              # Gateway service binary
│   ├── egw-admin/        # Operator CLI for secrets management
│   ├── egwctl/           # Agent CLI client
│   └── tools/            # Debug and utility tools
├── internal/
│   ├── server/           # HTTP routing, auth, op execution, admin API
│   ├── config/           # YAML config loading + validation
│   ├── policy/           # Operation registry, param validation, constraints
│   ├── secrets/          # Credential Manager resolver + taint registry
│   ├── audit/            # Append-only JSONL audit logger
│   ├── redact/           # ResponseShaper + taint-based leak prevention
│   ├── scheduler/        # Clockify auto-PR scheduler stub
│   └── connectors/
│       ├── httprest/     # Generic HTTP (Jira, GitLab, Kibana, etc.)
│       ├── postgres/     # Read-only SQL with safety guards
│       ├── localservice/ # WireGuard + TCP connectivity checks
│       ├── email/        # IMAP read + SMTP send
│       ├── formsession/  # Form-based login with session cookie capture (defined, not wired)
│       └── connector.go  # Connector interface + request/response types
├── deploy/
│   ├── update.ps1        # Quick deploy (build + copy + restart)
│   ├── provision.ps1     # One-click full provisioning
│   ├── setup.ps1         # Service account + ACL setup
│   └── workspaces.ps1    # Workspace discovery module
├── config.example/       # Template configs + operation catalogs
│   ├── config.globex.yaml
│   ├── config.yaml
│   ├── operations.globex.yaml
│   ├── operations.yaml
│   ├── roles.yaml
│   └── tunnels.yaml
├── agent-keys.txt        # Generated agent tokens (gitignored)
├── SECURITY.md           # Security policy
├── CLAUDE.md             # Agent guide for the project
└── tools/
    └── slack-provision/  # Slack session token provisioning tool
                          # (debug tools live under cmd/tools/: gen-token, bb-debug, jira-debug, pg-debug, acme-check)
```

## Architecture

```
Agent ──Bearer token──> AuthN ──> Policy (grants) ──> Operation Registry
                                                           │
                     ┌─────────────────────────────────────┤
                     ▼                                     ▼
               Parameter validation              Connector dispatch
               Constraint checks                 (http-rest | postgres |
               Tunnel access check               local-service | mcp |
                                                 form-session)
                     │                                     │
                     ▼                                     ▼
               Secret Resolution ──> Cross-tenant ──> Upstream Execution
                     │                check                   │
                     ▼                                        ▼
               Taint Registry                          Response Shaper
                                                              │
                                                              ▼
                                                        Audit log + Response
```

## Features

| Feature | Status | Notes |
|---------|--------|-------|
| Admin auth | ✓ | SHA256 token verification; opt-in via `admin_token_sha256` |
| Config checksum | ✓ | SHA256 of config file exposed in `/capabilities` |
| Hot-reload | ✓ | `POST /admin/reload` — atomic pointer swap on success |
| Cross-tenant isolation | ✓ | Secret workspace prefix must match request workspace |
| Response shaping | ✓ | `response.allow_fields` filters JSON responses via `ResponseShaper` |
| Empty URL detection | ✓ | Returns descriptive error when `base_url` is unset |
| Credential Manager | ✓ | 30s TTL cache + taint registry for leak prevention |
| Postgres read-only | ✓ | Multi-statement detection + regex-based DDL/DML guard |
| URL-encoded paths | ✓ | `{param\|urlencode}` template support |
| Query params | ✓ | Unused params appended as query string |
| Slack session auth | ✓ | `slack-session` type injects `xoxc` Bearer + `xoxd` `d` cookie (no app/bot) |
| GitHub | ✓ | `bearer` PAT auth, `{site.org}` template, 13 operations |
| Trello | ✓ | `trello` auth (key + token query params), `trello_allowed_lists` scoping |
| Email (IMAP/SMTP) | ✓ | go-imap + net/smtp, works with Gmail app passwords |
| Email policy whitelist | ✓ | `email_allowed_senders/mailboxes`, `email_denied_senders/mailboxes` |
| OAuth auto-refresh | ✓ | `oauth` config block, refresh on 401, retry once |
| Admin diagnostics | ✓ | `POST /admin/diag` tests IMAP/SMTP/HTTP auth without exposing passwords |
| MCP (Model Context Protocol) | ✓ | JSON-RPC 2.0 Streamable HTTP, `tools/list` + `tools/call`, SSE support |
| Form-session connector | ✓ | Form-based login with session cookie capture |
| Outloofq/Hotmail | ✗ | Removed — requires XOAUTH2 (OAuth 2.0), go-imap/net-smtp basic auth rejected |
| Slack channel allow-list | ✗ | `slack_channels` policy field exists but no constraint enforces it yet |
| Kibana SSO proxy | ✗ | Basic Auth doesn't work through Upvote/Grafana proxy |
| Clockify scheduler | ✗ | Stub — not wired to gateway |
| Postgres AST parsing | ✗ | Uses regex + multi-statement detect instead of `pg_query_go` |
| Unit tests | ✓ | 3 test files: param formatting (`buildbody_test.go`), response shaping (`hook_test.go`), SQL injection (`quote_test.go`) |

## .ai/ Documentation

Detailed guides live in `.ai/`:

| File | Covers |
|------|--------|
| `.ai/AGENTS.md` | Agent conventions — build, deploy, secrets, admin API, architecture |
| `.ai/INDEX.md` | Doc index — quick reference to all docs |
| `.ai/DEVELOPMENT.md` | Build, deploy, testing, adding operations |
| `.ai/PITFALLS.md` | Known bugs, YAML issues (trailing whitespace, `@` quoting), design gotchas |
| `.ai/RULES.md` | Code conventions, security rules, checklists |
| `.ai/SECRETS.md` | Credential Manager deep-dive + admin auth setup |
| `.ai/CONNECTORS.md` | Connector interface, HTTP/PG/Email/MCP/Local-service details |
| `.ai/OPERATIONS.md` | Operation YAML syntax, param types, constraints |
| `.ai/LOCAL.md` | Workspace-specific domain knowledge (gitignored; real URLs/paths) |

Per-service token/auth guides in `docs/`:

| File | Covers |
|------|--------|
| `docs/AUTHENTIK.md` | Authentik SSO integration — token setup, operations |
| `docs/SLACK.md` | Slack session token provisioning, rotation |
| `docs/CLOCKIFY.md` | Clockify API key setup |
| `docs/TRELLO.md` | Trello API key + token setup, list-scoping policy |
| `docs/GITHUB.md` | GitHub PAT (classic + fine-grained) |
| `docs/GMAIL.md` | Gmail app password + email whitelist/blacklist policy |
| `docs/GDOCS.md` | Google Docs OAuth + auto-refresh |
| `docs/ONEDRIVE.md` | OneDrive/Microsoft Graph OAuth + auto-refresh |
