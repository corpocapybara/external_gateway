# Gmail App Password

Gmail uses **Basic auth** with an **App Password** (a 16-character code). Your regular
Google account password will not work — you must generate an app-specific password.

## Prerequisites

You must have **2-Step Verification** enabled on your Google account. If not enabled:

1. Open https://myaccount.google.com/security
2. Under **How you sign in to Google**, enable **2-Step Verification**

## Generating an App Password

1. Open https://myaccount.google.com/apppasswords
2. You may need to sign in and verify your identity
3. Under **Select app**, choose **Mail**
4. Under **Select device**, choose **Other (custom name)** and enter "external-gateway"
5. Click **Generate**
6. A 16-character password will appear (e.g. `xxxx xxxx xxxx xxxx`)
7. **Copy it** — you won't see it again

> **Note:** The app password grants full access to your Gmail account via IMAP/SMTP.
> Treat it like your account password.

## Store in the gateway

```powershell
.\egw-admin.exe -workspace <ws> -name gmail -value "<16-char-app-password>" -admin http://127.0.0.1:<port>/admin
```

The `user` field in `config.<ws>.yaml` must be set to your full Gmail address
(e.g. `yourname@gmail.com`).

## Known issues

- App passwords may stop working if you change your Google account password or
  revoke the app. Generate a new one if IMAP login fails.
- Gmail IMAP must be enabled: https://mail.google.com → Settings → Forwarding and
  POP/IMAP → **Enable IMAP**.

## Email policy (whitelist/blacklist)

The gateway enforces workspace-level email policies to restrict access to specific
mailboxes and senders. The policy is defined in `config.<ws>.yaml` under `workspaces.<ws>.policy`:

```yaml
    policy:
      email_allowed_senders:
        - "*@company.com"       # allow emails from anyone at company.com
      email_allowed_mailboxes:
        - "INBOX"
        - "Praca"
        - "[Gmail]/*"           # glob: all [Gmail] sub-mailboxes
      email_denied_senders:
        - "*@spam-domain.com"   # hard block (takes precedence over allowed)
      email_denied_mailboxes:
        - "[Gmail]/Spam"        # hard block (takes precedence over allowed)
```

### How it works

| Rule | Effect |
|------|--------|
| `email_allowed_senders` | Whitelist of sender addresses. Empty = allow all. Affects `read` and `search` results. |
| `email_allowed_mailboxes` | Whitelist of mailbox names. Empty = allow all. Affects `list_mailboxes`, `search`, `read`. |
| `email_denied_senders` | **Hard blacklist** — takes precedence over `allowed_senders`. Affects `send`, `read`, `search`. |
| `email_denied_mailboxes` | **Hard blacklist** — takes precedence over `allowed_mailboxes`. Affects `list_mailboxes`, `search`, `read`. |

### Pattern matching

- Exact match: `"INBOX"` matches only INBOX
- Glob: `"*@company.com"` matches any address at company.com
- Glob: `"[Gmail]/*"` matches all Gmail system folders
- Case-insensitive: `"inbox"` matches `"INBOX"`
- Denied patterns always win over allowed patterns

### Enforcement

- `list_mailboxes`: returns only allowed + non-denied mailboxes
- `search`: rejects denied mailboxes; filters out denied senders from results
- `read`: rejects denied mailboxes; denies messages from denied senders
- `send`: rejects recipients matching denied patterns or not matching allowed patterns
