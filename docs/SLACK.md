# Slack Integration Guide

Slack access through the gateway using a **browser/desktop session token** — no Slack
app or bot registration. Agents call allow-listed `slack.*` operations; the gateway
injects the session credentials from the SYSTEM Credential Manager, so agents never see
them (same model as Jira/GitLab/Postgres).

> Roadmap note: this delivers the "Slack" half of **M4**. CodiMD/Clockify remain pending.

## Why session token (not a bot/OAuth app)

The requirement was "reuse the Slack I'm already logged into, no workspace-admin approval,
no app registration." That rules out bot tokens (`xoxb`) and user-OAuth tokens (`xoxp`,
which need an installed app). The only remaining mechanism is the browser/desktop client
session: the `xoxc-…` token plus the `xoxd-…` `d` cookie.

Trade-offs (know these):
- **Rotation:** `xoxc`/`d` expire (the `d` cookie is long-lived; `xoxc` rotates on
  logout/session refresh). When calls start returning `invalid_auth`, re-run the
  provisioning tool to refresh both secrets. This is the recurring cost of the no-app path.
- **ToS:** driving Slack's client auth for automation is discouraged by Slack's ToS.
  Keep it low-rate and prefer read.
- The materially more robust upgrade, if the constraint ever relaxes, is a user-token
  Slack app (`xoxp`): stable token, documented API, saner rate limits, no cookie.

## What was added

| Area | Change |
|------|--------|
| Auth injection | New `slack-session` auth type in `internal/server/server.go` (executeHttpConnector) that injects **two** secrets: `Authorization: Bearer <xoxc>` + `Cookie: d=<xoxd>` |
| Config schema | `cookie_secret_ref` field on `Environment` in `internal/config/config.go` (second secret for cookie-based auth) |
| Live config | `slack` site under `workspaces.acme.sites` in `config.acme.yaml`; `slack.*` ops granted to the `dev-bot` role |
| Operations | `slack.auth_test`, `slack.channels`, `slack.messages`, `slack.send` in `operations.acme.yaml` |
| Tooling | `tools/slack-provision/` — reusable provisioning/rotation tool (see below) |

No new connector was needed — `slack-session` reuses the existing `http-rest` connector,
which sends whatever headers the server hands it.

## Auth mechanics (`slack-session`)

`internal/server/server.go`, in the auth-injection block of `executeHttpConnector`:

```go
} else if env.Auth.Type == "slack-session" && env.SecretRef != "" {
    // xoxc token -> Authorization: Bearer
    tWs, tName := parseSecretRef(env.SecretRef)
    if tok, err := secrets.ResolveWithWorkspaceCheck(tWs, tName, workspace); err == nil {
        headers["Authorization"] = "Bearer " + strings.TrimSpace(string(tok))
    }
    // xoxd cookie -> Cookie: d=...
    if env.CookieSecretRef != "" {
        cWs, cName := parseSecretRef(env.CookieSecretRef)
        if cook, err := secrets.ResolveWithWorkspaceCheck(cWs, cName, workspace); err == nil {
            headers["Cookie"] = "d=" + strings.TrimSpace(string(cook))
        }
    }
}
```

Notes:
- Both resolutions pass `workspace` (the request workspace) to
  `ResolveWithWorkspaceCheck`, so cross-tenant isolation still holds and the secrets
  actually resolve. (The `basic`/`bearer`/`api-key` branches do the same.)
- Both secrets are tainted by the resolver, so the redaction hook would scrub them from
  any response body.

## Config

`config.acme.yaml` → `workspaces.acme.sites.slack`:

```yaml
      slack:
        environments:
          prod:
            base_url: https://slack.com/api
            auth:
              type: slack-session
            secret_ref: secret://acme/slack-xoxc
            cookie_secret_ref: secret://acme/slack-xoxd
```

- Single environment `prod`; ops omit `env`, so the server's single-env fallback selects it.
- `base_url: https://slack.com/api` works for the standard Web API with an `xoxc` token.
  If a workspace ever rejects it (`invalid_auth`/`team_not_found`), switch to the team
  subdomain, e.g. `https://your-slack-subdomain.slack.com/api`.

## Secrets

| secret_ref | value |
|------------|-------|
| `secret://acme/slack-xoxc` | the workspace `xoxc-…` client token |
| `secret://acme/slack-xoxd` | the `d` cookie value (`xoxd-…`) — shared across all your Slack workspaces |

Stored via `egw-admin` (fed by **stdin**, never `-value`, so the secret never hits argv):

```powershell
$xoxc | .\egw-admin.exe -workspace acme -name slack-xoxc -admin http://127.0.0.1:8444/admin
$xoxd | .\egw-admin.exe -workspace acme -name slack-xoxd -admin http://127.0.0.1:8444/admin
```

## Operations (Acme `dev-bot` role)

| Op | Method | Slack endpoint | Notes |
|----|--------|----------------|-------|
| `slack.auth_test` | POST | `/auth.test` | health/identity check; returns team + user |
| `slack.channels`  | GET  | `/conversations.list` | params: `types` (default `public_channel,private_channel`), `limit`, `cursor` |
| `slack.messages`  | GET  | `/conversations.history` | params: `channel` (required), `limit`, `cursor`, `oldest`, `latest` |
| `slack.send`      | POST | `/chat.postMessage` | params: `channel`, `text` (≤4000) |

Each shapes its response via `allow_fields` (e.g. `messages[].ts/user/text`,
`response_metadata.next_cursor` for pagination).

**`slack.send` design:** it deliberately has **no** `body_template`. The server's
`buildBody` does raw `{{key}}` substitution with no JSON escaping, so a `text` containing
`"`/newlines would produce invalid JSON. Instead, `channel`/`text` are auto-appended as
query params (the server appends any non-path param to the query string), which Slack
accepts for `chat.postMessage`. This avoids the escaping hazard entirely.

## Provisioning / rotation tool

`tools/slack-provision/` — parameterized for any workspace + team. See its `README.md`.
It is a **separate Go module** (`slack-extract`) so its `goleveldb` dependency never
enters the gateway binary's dependency graph.

```powershell
# discover signed-in workspaces (masked tokens)
.\Provision-Slack.ps1 -ListTeams

# first-time (ELEVATED, Slack quit): wire Slack into gateway, deploy, verify
.\Provision-Slack.ps1 -Workspace acme -Port 8444 -SlackDomain your-slack-subdomain -Deploy -Verify

# rotate when the session expires (Slack quit; no deploy)
.\Provision-Slack.ps1 -Workspace acme -Port 8444 -SlackDomain your-slack-subdomain

# switch the connected workspace / refresh just the token — cookie is shared, so
# no cookie re-extraction, no Slack quit, no elevation
.\Provision-Slack.ps1 -Workspace acme -Port 8444 -SlackDomain your-slack-subdomain -TokenOnly -Verify
```

### What the tool does under the hood
1. `slack-extract.exe` (goleveldb) reads `%APPDATA%\Slack\Local Storage\leveldb` and
   returns each signed-in team's `{domain,name,url,token}`.
2. `read_cookie.py` reads the encrypted `d` cookie from `%APPDATA%\Slack\Network\Cookies`
   (SQLite).
3. PowerShell DPAPI-unwraps the AES key from `%APPDATA%\Slack\Local State`
   (`os_crypt.encrypted_key`, minus the 5-byte `DPAPI` prefix) and AES-256-GCM-decrypts
   the cookie (`v10`/`v11`: 3-byte tag + 12-byte nonce + ciphertext + 16-byte GCM tag;
   strips a 32-byte domain-hash prefix if present).
4. Stores both secrets via `egw-admin` (stdin) → falls back to `POST /admin/secrets`.
5. `-Deploy` rebuilds `egw.exe` + runs `deploy\update.ps1`; `-Verify` calls
   `slack.auth_test` + `slack.channels`.

## Extraction internals (why it's non-trivial)

Durable gotchas discovered while building this, in case the store layout changes:

- **Tokens are snappy-compressed** inside the LevelDB SSTables. A raw byte scan only
  recovers `xoxc-` fragments (~17 chars); you need a snappy-aware LevelDB reader
  (goleveldb) to get the full ~110-char tokens. They live in the `localConfig` value in
  Local Storage.
- **The Cookies DB is exclusively locked** while Slack runs — no file API can open/copy
  it. Slack must be fully quit (tray → Quit) to read the cookie. (The LevelDB files, by
  contrast, are share-readable while Slack runs — copy them, excluding `LOCK`.)
- **The `d` cookie is shared** across all workspaces signed into the session; the `xoxc`
  token is per-workspace.

## Verification

```powershell
$h = @{ Authorization = "Bearer <Acme token>" }
Invoke-RestMethod -Uri http://127.0.0.1:8444/v1/workspaces/acme/op/slack.auth_test `
  -Method Post -Headers $h -ContentType application/json -Body '{}'
# expect: {"ok":true,"team":"Globex …","user":"…", ...}
```

If `{"ok":false,"error":"invalid_auth"}`: the token/cookie expired (re-run the tool) OR
this workspace needs the team subdomain — set `base_url: https://<domain>.slack.com/api`
and redeploy.

## Current deployment state

Both gateway workspaces have a `slack` site wired (site config + `slack.*` ops + `dev-bot`
grants):

- **Globex** (`:8443`) → Globex Slack (`your-slack-subdomain`)
- **Acme** (`:8444`) → Acme Slack (`your-slack-subdomain`)

The gateway workspace is only an isolation boundary — it need not match the Slack team
name; the team is whichever token is provisioned into `secret://<ws>/slack-xoxc`. Each
workspace needs its **own** `slack-xoxc` + `slack-xoxd` secrets (the underlying `d` cookie
value is shared across Slack workspaces, but it is stored per gateway workspace). Provision
per workspace via `Provision-Slack.ps1`.

## Caveats / known limitations

- **Channel allow-list not enforced.** `config.go` has a `slack_channels` policy field,
  but no constraint consumes it yet. The only scoping is that the session sees exactly the
  channels the user is in. (To enforce, add a constraint type in
  `internal/policy/registry.go` analogous to `allowed_tables`.)
- **Rotation is manual** — see above.
- **`slack.send` is enabled.** Drop it from `operations.acme.yaml` + the `dev-bot` role for
  read-only.
