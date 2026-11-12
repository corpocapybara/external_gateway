# external_gateway

A compiled Go proxy that lets LLM agents call external services (Jira, GitLab, Kibana, PostgreSQL, Bitbucket, Clockify, Slack) without seeing credentials. Credentials live in Windows Credential Manager (`egw/<workspace>/<name>`), resolved at runtime by the gateway running as `LocalSystem`.

This file holds the project rules for every agent tool. `.ai/INDEX.md` (doc index, always loaded) lists the deeper docs; lazy-load them as needed.

**Before acting on this project, read `.ai/LOCAL.md` if it exists.** It holds workspace-specific domain knowledge (real URLs, tokens, deploy paths) that is gitignored and never committed. If absent, treat this project as a generic template with no specific deployment.

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
.\deploy\update.ps1     # Admin shell; rebuilds egw.exe itself
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

## Error responses

- `403 {"error":"denied","rule":"<rule>","detail":...}` — refused (authentication, grant, disabled, validation, constraint...).
- `404 {"error":"not_found","detail":"operation not found","op":"<id>","did_you_mean":["<id>",...]}` — no such op id. `did_you_mean` is an array of up to 3 ops the calling agent is granted in that workspace, best first (edit distance, then closer length, then name); omitted when none is close. An op that exists but is not granted stays 403 `rule:"grant"`. Both are audited as `deny`.

## Capabilities endpoint

`GET /capabilities?workspace=<ws>` (Bearer token, grant in `<ws>`) returns the operations the agent is granted there, config checksum and feature status map; no workspace policy. Its `credentials` list
has one entry per GitLab/GitHub token behind a granted, enabled site (`site`, `env`, `state` ok/rejected/unreachable/
missing, `expires_at`, `days_left`) with a `warning` to ask the owner when the token expires within 7 days, has
expired, is rejected (401/403) or is missing; cached 1 h after ok, 5 min otherwise, cleared by `/admin/secrets`
writes and `/admin/reload` (T0006). GitLab is detected only when the base URL contains `gitlab`, GitHub only for
`api.github.com`; no token, username or scopes are returned. `GET /help?workspace=<ws>` and `POST /feedback` also need a Bearer token (help: grant in `<ws>`, else 403); only the bare `/help` banner and `/healthz` are public.

## Key files

| File | Purpose |
|------|---------|
| `internal/server/server.go` | HTTP router, auth, op execution, admin API |
| `internal/policy/registry.go` | Operation definitions, constraints, validation |
| `internal/config/config.go` | YAML config loading, validation, checksum |
| `internal/secrets/resolver.go` | Credential Manager w/ 30s cache, cross-tenant isolation |
| `internal/connectors/*.go` | Connector implementations |
| `deploy/update.ps1` | Quick deploy (rebuild egw.exe, copy it + configs + ops, restart) |
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

## Briefing worktree-isolated subagents

A subagent launched with `isolation: "worktree"` runs in `.claude/worktrees/agent-*`, a checkout of committed files only; absolute main-checkout paths (`C:\dev\apps\external_gateway\...`) are refused there.
- Write brief paths repo-relative (`internal/...`), never absolute.
- Untracked or uncommitted inputs (`.ai/LOCAL.md`, `config.*.yaml`, `agent-keys.txt`) are not in the worktree: paste the needed text into the brief or commit it first; don't point at a main-checkout path.

## Other AI harnesses

Claude Code loads this file through `CLAUDE.md`; opencode loads it directly and the extra files through `opencode.json` `instructions`. For other harnesses:

| Harness | Config location | Reference |
|---------|----------------|-----------|
| Cursor | `.cursorrules` | Point to `AGENTS.md` and `.ai/` docs (Cursor reads project rules from root) |
| GitHub Copilot | `.github/copilot-instructions.md` | Link to `AGENTS.md` and `.ai/` as instructional context |
| Continue.dev | `.continue/config.json` | Add `AGENTS.md` and `.ai/*.md` as `contextProviders` |
| Aider | `.aider.conf.yml` | Use `--read=AGENTS.md,.ai/INDEX.md` or add to `read:` list |
