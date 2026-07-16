# Trello Integration

Trello access through the gateway using the legacy **API key + user token** auth
(Trello does not yet accept Atlassian API tokens). Agents call allow-listed `trello.*`
operations; the gateway appends both credentials as query parameters.

## Auth model

The `trello` auth type sends `key=<apiKey>&token=<userToken>` as URL query parameters
on every request — the only method that doesn't require an OAuth library or Power-Up
iframe.

## Getting credentials

### 1. Create a Power-Up and get your API key

1. Open https://trello.com/power-ups/admin
2. Click **Create Power-Up** (or manage an existing one)
3. Fill in a name (e.g. "external-gateway") and save
4. Go to the **API Key** tab
5. Click **Generate a new API Key**
6. Copy the **API key** value

### 2. Generate a user token

Visit this URL in your browser (replace `{yourKey}` with your API key):

```
https://trello.com/1/authorize?expiration=never&scope=read,write,account&response_type=token&key={yourKey}
```

Click **Allow** — you'll be redirected to a page with your token. Copy it.

> **Token scope:** `read,write,account` grants full access to your Trello account
> (boards, cards, members, organizations). The token never expires. Revoke it at any
> time from the same Power-Up page.

### 3. Store in the gateway

```powershell
.\egw-admin.exe -workspace <ws> -name trello-key -value "<api-key>" -admin http://127.0.0.1:<port>/admin
.\egw-admin.exe -workspace <ws> -name trello-token -value "<user-token>" -admin http://127.0.0.1:<port>/admin
```

## Config

`config.<ws>.yaml` → `workspaces.<ws>.sites.trello`:

```yaml
      trello:
        environments:
          prod:
            base_url: https://api.trello.com/1
            auth:
              type: trello
            secret_ref: secret://<ws>/trello-key
            cookie_secret_ref: secret://<ws>/trello-token
```

- `secret_ref` holds the API key (appended as `?key=...`)
- `cookie_secret_ref` holds the user token (appended as `&token=...`)

## Secrets

| Secret ref | Value |
|------------|-------|
| `secret://<ws>/trello-key` | Trello Power-Up API key |
| `secret://<ws>/trello-token` | Trello user token (from the authorize flow) |

## Operations

| Op | Method | Path | Description |
|----|--------|------|-------------|
| `trello.boards` | GET | `/members/me/boards` | List boards (filter: all/open/closed) |
| `trello.board` | GET | `/boards/{id}` | Board details + lists |
| `trello.lists` | GET | `/boards/{boardId}/lists` | Lists on a board |
| `trello.cards` | GET | `/lists/{listId}/cards` | Cards in a list |
| `trello.card` | GET | `/cards/{id}` | Card details + labels |
| `trello.create_card` | POST | `/cards` | Create a card |
| `trello.update_card` | PUT | `/cards/{id}` | Move/rename card |
| `trello.add_comment` | POST | `/cards/{id}/actions/comments` | Comment on a card |
| `trello.members` | GET | `/boards/{boardId}/members` | Board members |
| `trello.labels` | GET | `/boards/{boardId}/labels` | Board labels |
| `trello.checklists` | GET | `/cards/{id}/checklists` | Card checklists |

## Usage examples

```powershell
$token = "<agent-token>"
$base = "http://127.0.0.1:<port>/v1/workspaces/<ws>/op"

# List boards
Invoke-RestMethod -Uri "$base/trello.boards" -Method Post `
  -Body '{}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Create a card
Invoke-RestMethod -Uri "$base/trello.create_card" -Method Post `
  -Body '{"idList":"abc123","name":"My card","desc":"Description"}' `
  -ContentType "application/json" -Headers @{"Authorization" = "Bearer $token"}
```

## Caveats

- **Trello API key + token is the only option** — Atlassian API tokens (from
  `id.atlassian.com`) do not work with Trello. They only authenticate Jira, Confluence,
  and Jira Align.
- **OAuth 2.0 is coming** — Atlassian has announced Trello OAuth 2.0 (RFC-89), which
  will introduce Bearer tokens with 1-hour expiry + refresh tokens. Not yet available.
- **API key is public** — by design, the API key can be visible (it identifies your
  Power-Up). The user token is a secret and must be stored securely.
- **Token never expires** — unless revoked from the Power-Up admin page.

## List-scoping policy (`trello_allowed_lists`)

To restrict which lists the agent can access, define `trello_allowed_lists` in the
workspace policy. When configured, any list-targeting operation must use one of the
allowed list IDs.

```yaml
    policy:
      trello_allowed_lists:
        - "5f88a1b..."   # To Do
        - "5f88a2c..."   # In Progress
        # empty = deny all list-scoped writes
```

### Enforced operations

| Operation | Enforcement |
|-----------|-------------|
| `trello.cards` | `listId` param must be in allowed list |
| `trello.create_card` | `idList` param must be in allowed list |
| `trello.update_card` with `idList` | target list must be in allowed list |
| `trello.update_card` without `idList` | **denied** — cannot verify which list the card is on |
| `trello.add_comment` | **denied** — uses card ID, not list ID; fetch card first or exclude from role |

### Whitelist model

- **Empty list = deny all** list-scoped operations (safest default)
- Populate with list IDs to allow access
- Operations that don't target a list (`boards`, `board`, `members`, `labels`, `checklists`)
  are **not affected** by this policy
