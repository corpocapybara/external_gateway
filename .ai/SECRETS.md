# Secrets & Credential Manager Guide

## Architecture

Secrets are stored in Windows Credential Manager under `egw/<workspace>/<name>` as generic credentials for the `LocalSystem` account. The gateway (running as `LocalSystem`) resolves them at runtime — agents never see credentials.

```
Admin API (POST /admin/secrets)
    │
    ▼
Resolver.Set(workspace, name, value)
    │
    ▼
wincred.NewGenericCredential("egw/acme/gitlab").Write()
    │
    ▼
Windows Credential Manager (SYSTEM store)
```

At request time:
```
GET /v1/workspaces/acme/op/gitlab.projects
    │
    ▼
Site config: secret_ref: secret://acme/gitlab
    │
    ▼
Resolver.Resolve("acme", "gitlab")
    │
    ▼ (checks 30s TTL cache first)
wincred.GetGenericCredential("egw/acme/gitlab")
    │
    ▼
Bearer token injected into upstream request
```

## Setting secrets

### Via egw-admin.exe (recommended)

```powershell
.\egw-admin.exe -workspace acme -name gitlab -value "glpat-xxx"
```

Flags:
- `-workspace` — workspace name (acme or globex)
- `-name` — secret name (gitlab, jira, kibana-prod, etc.)
- `-value` — the secret value (or pipe via stdin)
- `-action` — `set` (default) or `delete`
- `-admin` — admin API address (default: `http://127.0.0.1:8443/admin`)
- `-token` — admin bearer token (required if `admin_token_sha256` is set in config; see below)

**Note**: The default admin address is port 8443 (Globex). For Acme secrets, use `-admin http://127.0.0.1:8444/admin`.

### Via direct API call

```powershell
$body = @{workspace = "acme"; name = "kibana-prod"; value = "password"} | ConvertTo-Json -Compress
Invoke-RestMethod -Uri "http://127.0.0.1:8444/admin/secrets" -Method Post -Body $body -ContentType "application/json"
```

### Admin API authentication

If `admin_token_sha256` is set in the config, all admin API calls must include an `Authorization: Bearer <token>` header where the SHA256 of `<token>` matches the configured hash. Create an admin token:

```powershell
# Generate a token and its SHA
$adminToken = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$sha = [Security.Cryptography.SHA256]::Create()
$hash = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($adminToken))).Replace("-", "").ToLower()
Write-Host "Token: $adminToken`nSHA256: $hash"

# Then set admin_token_sha256: <hash> in config.<ws>.yaml, deploy, restart
# Use the token for admin calls:
Invoke-RestMethod -Uri "http://127.0.0.1:8444/admin/secrets" -Method Post `
  -Body $body -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $adminToken"}
```

With `egw-admin.exe`, pass the same token via `-token` (it sets the `Authorization: Bearer` header for you):

```powershell
.\egw-admin.exe -workspace acme -name gitlab -value "glpat-xxx" -admin http://127.0.0.1:8444/admin -token $adminToken
```

If `admin_token_sha256` is empty or unset, admin auth is bypassed (backwards compatible).

## Deleting secrets

> **Not yet supported server-side.** `egw-admin -action delete` issues an HTTP
> `DELETE /admin/secrets`, but the gateway's admin handler only accepts `POST`/`PUT`
> on that route and has no delete implementation, so the call returns `405 Method
> Not Allowed`. To remove a secret today, delete the `egw/<ws>/<name>` entry from the
> SYSTEM account's Credential Manager directly. Wiring a server-side delete is a TODO.

```powershell
.\egw-admin.exe -workspace acme -name gitlab -action delete   # returns 405 until server support is added
```

## Secret naming convention

```
secret://<workspace>/<name>
```

Where `<name>` matches the config's `secret_ref` suffix:
- `secret://acme/gitlab` → gitlab.com PAT with `read_api`, `read_repository`, `read_registry`
- `secret://acme/jira-prod` → Jira Cloud API token
- `secret://acme/kibana-prod` → Kibana/ES password for user `your-jira-username`
- `secret://acme/pg-dev` → PostgreSQL password for dev environment
- `secret://globex/jira` → Globex Jira API token
- `secret://globex/clockify` → Clockify API key
- `secret://acme/slack-xoxc` → Slack browser session token (`xoxc-…`), per-workspace
- `secret://acme/slack-xoxd` → Slack `d` session cookie (`xoxd-…`), shared across workspaces

Slack secrets are a **pair** (token + cookie) and expire; provision/rotate them with
`tools/slack-provision/Provision-Slack.ps1` rather than by hand. See `docs/SLACK.md`.

## Cache & taint

- **30-second TTL cache**: resolved secrets are cached in memory for 30 seconds
- **Taint registry**: when a secret is resolved, its SHA256 is registered. The redaction hook (`internal/redact/hook.go`) scans response bodies for tainted hashes to prevent accidental leak.

## Troubleshooting

### Secret resolves but upstream returns auth errors

The secret might be wrong. Delete and re-set. Check the gateway's audit logs for `upstream_status` values (401 = unauthorized, 403 = forbidden).

### "failed to resolve secret" in logs

The credential wasn't found in Credential Manager. Verify:
1. The secret name matches (case-sensitive)
2. The admin API was called on the correct gateway port
3. The gateway service is running

### Checking what's stored

Windows Credential Manager doesn't expose secrets easily. You can verify secret presence indirectly:
```powershell
# Test by calling the admin API (no GET, but you'll get an error if not found)
# Or check the audit log for successful upstream requests
```
