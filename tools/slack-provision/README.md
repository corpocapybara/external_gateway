# slack-provision

Provision (or rotate) a **Slack browser/desktop session token** into external_gateway
for any gateway workspace and any signed-in Slack team — no Slack app/bot registration.
Reuses your logged-in Slack desktop session.

## What it does

1. **Discovers** every Slack workspace signed into the desktop app and its `xoxc` token
   (from `%APPDATA%\Slack\Local Storage\leveldb`, via the `slack-extract` Go helper).
2. **Extracts** the shared `xoxd` `d` cookie from `%APPDATA%\Slack\Network\Cookies`
   (AES-256-GCM, decrypted with the DPAPI-protected key in `Local State`).
3. **Stores** both as `secret://<ws>/<prefix>-xoxc` and `secret://<ws>/<prefix>-xoxd`
   in the SYSTEM Credential Manager, via the gateway admin API.
4. Optionally **deploys** (`-Deploy`) the current `egw.exe` + configs, and **verifies**
   (`-Verify`) with `slack.auth_test` + `slack.channels` through the gateway.

Secrets are held only in memory and POSTed to the local admin API — never written to
disk, never on a command line, only masked previews are printed.

## Prerequisites

- **Go** (builds `slack-extract.exe` on first run) and **Python 3** (reads the Cookies SQLite).
- **Slack fully quit** while extracting the cookie (its DB is exclusively locked while running).
- **Elevated shell** only when using `-Deploy` (writes `C:\Program Files\...` + restarts services).
- The target workspace's config must declare a `slack` site using `slack-session` auth
  (run `-ShowConfig` to print the YAML block).

## Usage

```powershell
# List signed-in Slack workspaces (masked tokens)
.\Provision-Slack.ps1 -ListTeams

# Print the config block to add for a workspace
.\Provision-Slack.ps1 -Workspace acme -ShowConfig

# First-time provisioning: wire Slack into the gateway, deploy, verify (ELEVATED, Slack quit)
.\Provision-Slack.ps1 -Workspace acme -Port 8444 -SlackDomain your-slack-subdomain -Deploy -Verify

# Rotate an existing pair when the session expires (Slack quit; no deploy needed)
.\Provision-Slack.ps1 -Workspace globex -Port 8443 -SlackDomain your-slack-subdomain

# Switch the connected workspace / refresh just the token (Slack can stay running, no elevation)
.\Provision-Slack.ps1 -Workspace acme -Port 8444 -SlackDomain your-slack-subdomain -TokenOnly -Verify
```

## Parameters

| Param | Purpose |
|-------|---------|
| `-Workspace` | Gateway workspace name (e.g. `globex`, `acme`) — which gateway hosts the secrets |
| `-Port` | Gateway admin API port (default `8443`) |
| `-SlackDomain` | Slack team subdomain (e.g. `your-slack-subdomain`); see `-ListTeams` |
| `-Prefix` | secret-name prefix (default `slack` → `slack-xoxc`, `slack-xoxd`) |
| `-Deploy` | rebuild `egw.exe` + run `deploy\update.ps1` + wait for health (needs elevation) |
| `-Verify` | call `slack.auth_test` + `slack.channels` (token auto-read from `agent-keys.txt`, or `-AgentToken`) |
| `-TokenOnly` | swap only the per-workspace `xoxc` token, keeping the shared `xoxd` cookie — no Slack quit, no elevation |
| `-Diagnose` | don't store — re-extract and call Slack `auth.test` directly; needs Slack quit |
| `-ListTeams` / `-ShowConfig` | inspection helpers; exit without mutating anything |
| `-Rebuild` | force rebuild of `slack-extract.exe` |

## Operational notes

- **Rotation:** `xoxc`/`d` expire; re-run (Slack quit) to refresh. This is the recurring
  cost of the no-app approach.
- **Scope:** the gateway `slack_channels` policy field is **not yet enforced** — the
  only scoping is that the session sees the channels you're in.
- **ToS:** driving Slack's client auth for automation is discouraged by Slack's ToS;
  keep usage low-rate and prefer read-only.
