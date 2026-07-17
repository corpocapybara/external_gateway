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

The endpoint also accepts `PUT` and `DELETE` (`DELETE /admin/secrets?workspace=acme&name=jira`).

See `docs/` for per-service token/credential guides and `.ai/SECRETS.md` for architecture details.

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
| `/capabilities?workspace=<ws>` | GET | Operation schemas, params, example calls, config checksum, feature status |
| `/v1/workspaces/<ws>/op/<op-id>` | POST, GET | Execute an operation |
| `?dry_run=true` | — | Validate params + grants, skip upstream call |

### Admin (needs admin token if configured)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/admin/` | GET | List available admin endpoints |
| `/admin/secrets` | POST, PUT, DELETE | Set/delete a secret in SYSTEM Credential Manager |
| `/admin/reload` | POST | Hot-reload config + ops from disk |
| `/admin/diag` | POST | Test site auth (IMAP/SMTP/HTTP) without exposing passwords |

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

## Capabilities

| Capability | Status | Details |
|------------|--------|---------|
| Auth & isolation | ✓ | [Admin auth](.ai/SECRETS.md), [agent tokens](#agent-tokens), [cross-tenant](.ai/SECRETS.md#cross-tenant-isolation-checks-against-workspace-name-not-site-name) |
| Config & hot-reload | ✓ | [SHA256 checksum](#api-reference), [POST /admin/reload](.ai/PITFALLS.md#config-hot-reload-replaces-in-memory-state-atomically) |
| Response shaping | ✓ | [`.ai/OPERATIONS.md`](.ai/OPERATIONS.md) — `allow_fields` with dot-notation + array projection |
| PostgreSQL read-only | ✓ | [AST-based validation](.ai/PITFALLS.md#postgresql-sql-guard-uses-regex-not-ast) via pgparser; multi-statement + FOR UPDATE detection |
| Slack | ✓ | [Session token auth](docs/SLACK.md) — `xoxc` + `xoxd` cookie; [provision tool](docs/SLACK.md#provisioning--rotation-tool) |
| GitHub | ✓ | [Bearer PAT](docs/GITHUB.md), 13 read/write ops, repo scoping via `github_repos` |
| Trello | ✓ | [API key + token](docs/TRELLO.md), [list-scoping policy](docs/TRELLO.md#list-scoping-policy-trello_allowed_lists) |
| Email (IMAP/SMTP) | ✓ | [Gmail app password](docs/GMAIL.md), [whitelist/blacklist policy](docs/GMAIL.md#email-policy-whitelistblacklist) |
| OAuth auto-refresh | ✓ | [Google Docs](docs/GDOCS.md#auto-refresh) + [OneDrive](docs/ONEDRIVE.md#auto-refresh) — refresh on 401, retry once |
| Clockify | ✓ | [API key](docs/CLOCKIFY.md) time tracking — workspace, projects, entries, add, edit |
| Authentik | ✓ | [SSO integration](.ai/AUTHENTIK.md) — users, groups, tokens, blueprints |
| Jenkins | ✓ | [CSRF crumb](.ai/PITFALLS.md#jenkins-csrf-crumb-required-for-post-operations) auto-fetched before POST ops |
| Admin diagnostics | ✓ | [POST /admin/diag](#api-reference) — IMAP/SMTP/HTTP auth test, no password exposure |
| MCP | ✓ | Streamable HTTP — `tools/list` + `tools/call` |
| Form-session | ✓ | Connector defined, not wired to any operation |
| Capabilities examples | ✓ | `/capabilities` returns PowerShell example call per operation |
| Hotmail/Outlook | ✗ | Requires XOAUTH2 — [basic auth rejected](.ai/PITFALLS.md#hotmailoutlook-consumer-accounts-require-xoauth2) |
| Kibana SSO proxy | ✗ | [Upvote/Grafana proxy](.ai/PITFALLS.md#kibana-prod-behind-grafana-upvote-sso-proxy) blocks Basic Auth |
| Clockify scheduler | ✗ | Stub — not wired |
| Slack channel allow-list | ✗ | `slack_channels` exists but [no constraint enforces it](.ai/PITFALLS.md#stalled--incomplete-features) |

## .ai/ Documentation

Detailed guides live in `.ai/`:

| File | Covers |
|------|--------|
| `.ai/AGENTS.md` | Agent conventions — build, deploy, secrets, admin API, service lifecycle, architecture |
| `.ai/INDEX.md` | Doc index — quick reference to all docs |
| `.ai/DEVELOPMENT.md` | Build, deploy, testing, adding operations |
| `.ai/PITFALLS.md` | Known bugs, YAML pitfalls, design gotchas, session caveats |
| `.ai/RULES.md` | Code conventions, security rules, checklists |
| `.ai/SECRETS.md` | Credential Manager deep-dive + admin auth setup |
| `.ai/CONNECTORS.md` | Connector interface, HTTP/PG/Email/MCP/Local-service details |
| `.ai/OPERATIONS.md` | Operation YAML syntax, param types, constraints |
| `.ai/AUTHENTIK.md` | Authentik SSO integration — token setup, operations |
| `.ai/LOCAL.md` | **(gitignored)** Workspace-specific domain knowledge (real URLs/paths) |

Per-service token/auth guides in `docs/`:

| File | Covers |
|------|--------|
| `docs/SLACK.md` | Slack session token provisioning, rotation |
| `docs/CLOCKIFY.md` | Clockify time tracking — all operations with examples |
| `docs/TRELLO.md` | Trello API key + token setup, list-scoping policy |
| `docs/GITHUB.md` | GitHub PAT (classic + fine-grained) |
| `docs/GMAIL.md` | Gmail app password + email whitelist/blacklist policy |
| `docs/GDOCS.md` | Google Docs OAuth + auto-refresh |
| `docs/ONEDRIVE.md` | OneDrive/Microsoft Graph OAuth + auto-refresh |
