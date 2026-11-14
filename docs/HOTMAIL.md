# Hotmail / Outlook.com: How To Connect

Personal Microsoft mailboxes (`@hotmail.com`, `@outlook.com`, `@live.com`) through the gateway's `email`
connector, over IMAP and SMTP with **XOAUTH2**. Agents call `hotmail.list_mailboxes`, `hotmail.search`,
`hotmail.read` and `hotmail.send`; the gateway holds the OAuth tokens in the SYSTEM Credential Manager and
refreshes the access token itself.

## Why basic auth fails

Outlook.com refuses passwords on both protocols:

- IMAP `LOGIN` (and `AUTHENTICATE PLAIN`) answers `NO AUTHENTICATE failed.`, even with a correct password or an
  app password.
- SMTP on `smtp-mail.outlook.com:587` offers only `AUTH LOGIN XOAUTH2` after STARTTLS; `AUTH PLAIN` gets
  `504 5.7.4 Unrecognized authentication type`.

The only way in for a third-party client is an OAuth 2.0 access token for the scopes
`https://outlook.office.com/IMAP.AccessAsUser.All` and `https://outlook.office.com/SMTP.Send`, sent with the
SASL mechanism XOAUTH2. (Microsoft Graph Mail would also work, but it is a different API: the gateway's
email policy, `email_allowed_mailboxes`, sender filters and so on, applies only to the IMAP/SMTP connector.)

## Prerequisite: a Microsoft Entra directory

An OAuth client needs an **app registration**, and an app registration lives in a Microsoft Entra
directory (tenant). Registering an app "outside of a directory" with a personal account is deprecated: the
Entra portal says so and no longer offers it. The directory does not need to contain the mailbox; the app is
registered there and then used by the personal account.

Check for a directory:

1. Sign in at <https://entra.microsoft.com> (or <https://portal.azure.com>) with the account you will manage
   the app with.
2. Top-right account menu, **Switch directory**. If a directory is listed (for example "Default Directory"),
   use it.

If none is listed, get one: creating a free Azure account (<https://azure.microsoft.com/free>) creates a
directory, and so does joining the Microsoft 365 Developer Program. A work or school account's directory works
too if its admins allow app registrations.

## Register the app

In the Entra admin center, **Applications > App registrations > New registration**:

1. **Name**: anything, e.g. `external-gateway-hotmail`.
2. **Supported account types**: **Personal Microsoft accounts only**.
3. **Redirect URI**: leave empty (the device code flow needs none). Register.
4. **Authentication** > **Advanced settings** > **Allow public client flows**: **Yes**. Save. Without it the
   device code flow fails with `AADSTS7000218` (client secret required).
5. Do **not** create a client secret: a public client has none, and the config has no `client_secret_ref`.
6. **API permissions**: nothing to add. The Outlook scopes are requested dynamically by the script below
   and consented on first sign-in.
7. Copy the **Application (client) ID** from the Overview page.

Keep the real client id and mailbox address in your deployment's gitignored notes (`.ai/LOCAL.md`), not in
committed files.

## Get the tokens (device code)

Run on the gateway machine, in a normal PowerShell, with the gateway running. The script asks Microsoft for a
device code, you sign in in a browser, and it stores the access token and the refresh token through
`/admin/secrets` without printing them.

```powershell
$clientId  = "<application-client-id>"
$workspace = "<ws>"                      # the workspace whose config has the hotmail site
$admin     = "http://127.0.0.1:8443/admin/secrets"   # your workspace's port
$adminTok  = "<admin token>"            # only if admin_token_sha256 is configured, else ""
$authority = "https://login.microsoftonline.com/consumers/oauth2/v2.0"
$scope     = "https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send offline_access"

$dc = Invoke-RestMethod -Method Post -Uri "$authority/devicecode" -Body @{ client_id = $clientId; scope = $scope }
Write-Host "Open $($dc.verification_uri) and enter code $($dc.user_code)"
Start-Process $dc.verification_uri

$body = @{ client_id = $clientId; grant_type = "urn:ietf:params:oauth:grant-type:device_code"; device_code = $dc.device_code }
$deadline = (Get-Date).AddSeconds($dc.expires_in)
$tok = $null
while (-not $tok -and (Get-Date) -lt $deadline) {
  Start-Sleep -Seconds $dc.interval
  try { $tok = Invoke-RestMethod -Method Post -Uri "$authority/token" -Body $body }
  catch {
    $err = ($_.ErrorDetails.Message | ConvertFrom-Json).error
    if ($err -notin "authorization_pending", "slow_down") { throw "token request failed: $err" }
  }
}
if (-not $tok) { throw "device code expired before sign-in" }

$headers = @{}
if ($adminTok) { $headers["Authorization"] = "Bearer $adminTok" }
foreach ($pair in @(@("hotmail", $tok.access_token), @("hotmail-refresh", $tok.refresh_token))) {
  $json = @{ workspace = $workspace; name = $pair[0]; value = $pair[1] } | ConvertTo-Json -Compress
  Invoke-RestMethod -Method Post -Uri $admin -Body $json -ContentType "application/json" -Headers $headers | Out-Null
  Write-Host "stored $($pair[0])"
}
Remove-Variable tok, body, dc
```

On the consent screen, sign in with the mailbox account and accept. The token response has `expires_in`
about 3600 and a `refresh_token` because of `offline_access`.

## Config

The site is a normal `email` site with `auth.type: oauth2` and an `oauth` block (no client secret):

```yaml
      hotmail:
        environments:
          prod:
            host: outlook.office365.com
            imap_port: 993
            smtp_host: smtp-mail.outlook.com
            smtp_port: 587            # STARTTLS; 465 would be implicit TLS
            user: you@hotmail.com
            auth:
              type: oauth2
            secret_ref: secret://<ws>/hotmail
            oauth:
              token_url: https://login.microsoftonline.com/consumers/oauth2/v2.0/token
              client_id: <application-client-id>
              refresh_secret_ref: secret://<ws>/hotmail-refresh
```

| Secret ref | Value |
|------------|-------|
| `secret://<ws>/hotmail` | Access token (stored by the script, then rewritten by the gateway on each refresh) |
| `secret://<ws>/hotmail-refresh` | Refresh token (stored by the script, then replaced by the rotated one on each refresh) |

How the gateway uses it:

- `auth.type: oauth2` makes IMAP log in with `AUTHENTICATE XOAUTH2` and SMTP with `AUTH XOAUTH2`; any other
  value keeps `LOGIN` / `AUTH PLAIN` (Gmail app passwords). The token is sent only over TLS.
- SMTP on any port but 465 connects in plain text and must complete STARTTLS before AUTH; a server that does
  not offer STARTTLS is refused. Port 465 uses implicit TLS.
- When the server refuses the login (IMAP `NO`, SMTP `535`), the gateway refreshes the access token once with
  the refresh token (no `scope` parameter: Microsoft returns the originally consented scopes), stores the new
  access token and the rotated refresh token, and retries the call once. A failed refresh returns an error
  saying "OAuth token refresh failed".
- `/admin/status` lists both secrets and does not try `LOGIN` with the access token (the env is an OAuth kind).

## Operations

`config.example/operations.globex.yaml` has the four ops; they mirror `email.*` with `site: hotmail`, and every
role that grants `email.X` should grant `hotmail.X`.

| Op | Params | Returns |
|----|--------|---------|
| `hotmail.list_mailboxes` | `env` | `mailboxes[].name` |
| `hotmail.search` | `env`, `mailbox` (default `INBOX`), `criteria` (substring of subject/from) | the last 50 messages: `uid`, `subject`, `from`, `date`, `flags` |
| `hotmail.read` | `env`, `mailbox`, `uid` (from search) | `uid`, `subject`, `from`, `to`, `date`, `body`, `flags` |
| `hotmail.send` | `env`, `to` (one bare address), `subject`, `body` | `status`, `to` |

The workspace email policy (`email_allowed_mailboxes`, `email_denied_mailboxes`, `email_allowed_senders`,
`email_denied_senders`) applies as for any email site; see [GMAIL.md](GMAIL.md#email-policy-whitelistblacklist).

```powershell
$h = @{ Authorization = "Bearer <agent-token>" }
$base = "http://127.0.0.1:<port>/v1/workspaces/<ws>/op"
Invoke-RestMethod -Method Post -Uri "$base/hotmail.search" -Headers $h -ContentType "application/json" `
  -Body (@{ env = "prod"; mailbox = "Inbox"; criteria = "invoice" } | ConvertTo-Json -Compress)
```

## Verify

```powershell
$h = @{ Authorization = "Bearer <admin token>" }   # omit if no admin token is configured
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:<port>/admin/diag" -Headers $h -ContentType "application/json" `
  -Body '{"workspace":"<ws>","site":"hotmail"}'
```

Expected: `secret_resolved: true`, `imap_login: ok`, `smtp_login: ok`. Diag goes through the same refresh path,
so an expired access token is refreshed here too. `imap_error` / `smtp_error` carry the server's text
(never the token).

## Known issues and blockers

- **Unverified-app consent screen.** A newly registered app is "unverified"; the consent page warns about it
  and may need **Accept** on an extra screen. This is expected for a personal app. Publisher verification needs
  a Microsoft Partner Network account and is not required.
- **Access token lifetime about 1 h.** The gateway refreshes it on the first rejected login; the first call
  after expiry costs one extra round trip.
- **Refresh token rotation and expiry.** Every refresh returns a new refresh token, which the gateway stores.
  A refresh token dies after a long inactivity (about 90 days unused), when the password changes, or when the
  consent is revoked at <https://microsoft.com/consent> (account > Privacy > Apps and services). Then every call
  fails with "OAuth token refresh failed": rerun the device code script.
- **Failed store of the rotated refresh token.** If Credential Manager refuses to store the new refresh token,
  the gateway only logs it and keeps the old one; Microsoft does not revoke a used refresh token right away,
  but if it expires the device code script must be rerun.
- **Concurrent refreshes.** Two requests rejected at the same moment each refresh, one after the other
  (refreshes are serialised). Microsoft does not revoke a used refresh token, so both succeed at the cost of an
  extra token call; the last stored refresh token wins.
- **SMTP 587 is STARTTLS only.** Outlook.com does not speak implicit TLS on 587. The connector does mandatory
  STARTTLS on every port except 465 (implicit TLS); this also changed Gmail's send path on 587 from a failing
  implicit-TLS dial to STARTTLS.
- **Mailbox names.** Outlook.com names the inbox `Inbox` (IMAP treats `INBOX` case-insensitively), the others
  `Sent`, `Deleted`, `Drafts`, `Junk`, `Archive`, plus localised names in the account's language (for example
  `Archiwum`) and nested folders as `Inbox/<name>`. Non-ASCII names arrive in modified UTF-7. Use the names
  `hotmail.list_mailboxes` returns in `mailbox` and in the mailbox policy lists.
- **Sending limits.** Outlook.com caps outgoing mail per day and per message (a few hundred messages a day for
  a personal account, lower for new accounts; Microsoft does not publish exact figures for every account) and
  may block an account that looks like a spammer. `hotmail.send` takes one recipient per call. Mail from a new
  account can land in the recipients' junk folder.
