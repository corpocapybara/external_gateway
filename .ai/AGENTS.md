# Agent Conventions

## Quick reference

| Thing | Globex | Acme |
|-------|--------|------|
| Port | 8443 | 8444 |
| Service name | `egw-globex` | `egw-acme` |
| Config source | `config.globex.yaml` | `config.acme.yaml` |
| Ops source | `operations.globex.yaml` | `operations.acme.yaml` |
| Deploy dir | `C:\Program Files\external_gateway\globex\` | `C:\Program Files\external_gateway\acme\` |
| Agent token | `<token>` (see `agent-keys.txt`) | `<token>` (see `agent-keys.txt`) |

Workspace convention: `config.<x>.yaml` is the single source of truth — everything else derives from `<x>`. See `.ai/RULES.md` for details.

## Build & Deploy

```powershell
& "C:\Program Files\Go\bin\go.exe" build -o egw.exe       ./cmd/egw/
& "C:\Program Files\Go\bin\go.exe" build -o egw-admin.exe  ./cmd/egw-admin/
& "C:\Program Files\Go\bin\go.exe" build -o egwctl.exe     ./cmd/egwctl/
.\deploy\update.ps1     # Admin shell
```

## Testing an operation

```powershell
$headers = @{"Authorization" = "Bearer <token>"}
$body = @{env = "prod"; param = "value"} | ConvertTo-Json -Compress
Invoke-RestMethod -Uri "http://127.0.0.1:8444/v1/workspaces/acme/op/<op-id>" `
  -Method Post -Body $body -ContentType "application/json" -Headers $headers
```

## Setting secrets

```powershell
.\egw-admin.exe -workspace acme -name gitlab -value "glpat-xxx"
```

If `admin_token_sha256` is configured, append `-token <admintoken>`.

## Slack

Session token (`xoxc` + `xoxd` cookie) — not a bot/app. Use the provisioning tool only:

```powershell
tools\slack-provision\Provision-Slack.ps1 -Workspace acme -Port 8444 -SlackDomain your-slack-subdomain -Deploy -Verify
tools\slack-provision\Provision-Slack.ps1 -ListTeams
```

See `docs/SLACK.md` for full details.

## Admin API

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/admin/secrets` | POST | Set a secret in SYSTEM Credential Manager |
| `/admin/reload` | POST | Hot-reload config and ops from disk |

## Capabilities endpoint

`GET /capabilities?workspace=<ws>` returns config checksum and feature status map.

## Key files

| File | Purpose |
|------|---------|
| `internal/server/server.go` | HTTP router, auth, op execution, admin API |
| `internal/policy/registry.go` | Operation definitions, constraints, validation |
| `internal/config/config.go` | YAML config loading, validation, checksum |
| `internal/secrets/resolver.go` | Credential Manager w/ 30s cache, cross-tenant isolation |
| `internal/connectors/*.go` | Connector implementations |
| `deploy/update.ps1` | Quick deploy (binary + configs + ops) |
| `tools/slack-provision/` | Slack session token provisioning |

## Service lifecycle

Services run as `LocalSystem` (kardianos/service). ImagePath includes `--config`, `--ops`, `--port`.

```powershell
Get-Service egw-acme, egw-globex
Restart-Service egw-acme
Get-CimInstance Win32_Service -Filter "Name='egw-acme'" | Select PathName
```

## Architecture

```
Agent ──Bearer token──> AuthN ──> Policy (grants) ──> Operation Registry
                                                          │
                    ┌─────────────────────────────────────┤
                    ▼                                     ▼
              Parameter validation              Connector dispatch
              Constraint checks                 (http-rest | postgres | local-service)
                    │                                     │
                    ▼                                     ▼
              Secret Resolution ──> Cross-tenant check ──> Upstream Execution
                    │                                             │
                    ▼                                             ▼
              Taint Registry                               Response Shaper
                                                               │
                                                               ▼
                                                         Audit log + Response
```

## Agent tokens

Token SHAs in config (`agents[].token_sha256`). Gateway hashes Bearer token and compares. Values in `agent-keys.txt` (treat as secrets).
