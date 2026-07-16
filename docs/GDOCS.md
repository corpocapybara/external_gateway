# Google Docs Integration

Google Docs access through the gateway using a **bearer OAuth 2.0 access token** (not a
service account). Agents call allow-listed `gdocs.*` operations; the gateway injects the
token from the SYSTEM Credential Manager.

## Auth model

The `gdocs` site uses `bearer` auth against `https://docs.googleapis.com`. The gateway
sends `Authorization: Bearer <token>` on every request.

The same token must also scope the **Google Drive API** (`https://www.googleapis.com/drive/v3`)
because document listing/search goes through Drive's files endpoint.

Required OAuth scopes:
- `https://www.googleapis.com/auth/documents.readonly` or `documents` (read/write)
- `https://www.googleapis.com/auth/drive.metadata.readonly` or `drive.metadata` (search/list)

## Getting a token

### Option 1: Full OAuth flow with refresh token (recommended for auto-refresh)

Use the Google OAuth 2.0 Playground with your own client credentials to get both an
access token and a refresh token.

#### 1a. Create OAuth credentials in Google Cloud Console

1. Open https://console.cloud.google.com/apis/credentials
2. Select or create a project
3. Click **Create Credentials** → **OAuth client ID**
4. If not yet configured, go to **OAuth consent screen** and set it up:
   - **User Type:** External
   - **App name:** anything (e.g. "external-gateway")
   - **Scopes:** add `../auth/documents` and `../auth/drive.metadata.readonly`
   - **Test users:** add your Google account
5. Back in **Credentials**, create an **OAuth client ID**:
   - **Application type:** Desktop app
   - Note the **Client ID** and **Client secret** shown in the dialog

#### 1b. Exchange for tokens via OAuth Playground

1. Open https://developers.google.com/oauthplayground
2. Click the gear icon → check **Use your own OAuth credentials**
3. Enter your **Client ID** and **Client secret** from step 1a
4. Under **Select & authorize APIs**, search for:
   - `https://www.googleapis.com/auth/documents` — Docs read/write
   - `https://www.googleapis.com/auth/drive.metadata.readonly` — list/search
5. Click **Authorize APIs**, log in, grant consent
6. Click **Exchange authorization code for tokens**
7. Copy the **Access token**, **Refresh token**

Then store all three:

```powershell
.\egw-admin.exe -workspace <ws> -name gdocs -value "<access-token>"
.\egw-admin.exe -workspace <ws> -name gdocs-refresh -value "<refresh-token>"
.\egw-admin.exe -workspace <ws> -name gdocs-client-secret -value "<client-secret>"
```

### Option 2: gcloud CLI (access token only, no auto-refresh)

```powershell
gcloud auth print-access-token
```

### Option 3: OAuth Playground (quick, no own credentials)

Same as Option 1 but skip step 2 (uses Google's test client). Tokens expire in ~1 hour
and cannot be refreshed without your own client credentials.

## Auto-refresh

The gateway can automatically refresh expired access tokens when you provide OAuth
configuration. On a 401 response from Google, the gateway uses the refresh token to
obtain a new access token, stores it in Credential Manager, and retries the request
once — all transparently to the agent.

### Config

```yaml
      gdocs:
        environments:
          prod:
            base_url: https://docs.googleapis.com
            auth:
              type: bearer
            secret_ref: secret://<ws>/gdocs
            oauth:
              token_url: https://oauth2.googleapis.com/token
              client_id: "your-client-id.apps.googleusercontent.com"
              client_secret_ref: secret://<ws>/gdocs-client-secret
              refresh_secret_ref: secret://<ws>/gdocs-refresh
```

### Secrets

| Secret ref | Value |
|------------|-------|
| `secret://<ws>/gdocs` | Google OAuth 2.0 access token (initially, then managed by gateway) |
| `secret://<ws>/gdocs-refresh` | Google OAuth 2.0 refresh token (long-lived) |
| `secret://<ws>/gdocs-client-secret` | Google Cloud OAuth client secret |

```powershell
.\egw-admin.exe -workspace <ws> -name gdocs -value "<initial-access-token>"
.\egw-admin.exe -workspace <ws> -name gdocs-refresh -value "<refresh-token>"
.\egw-admin.exe -workspace <ws> -name gdocs-client-secret -value "<client-secret>"
```

> The gateway automatically overwrites `secret://<ws>/gdocs` with a fresh access token
> on each refresh. The refresh token and client secret are read-only after setup.

## Operations

## Operations

| Op | Method | Path | Description |
|----|--------|------|-------------|
| `gdocs.list` | GET | `/v1/documents` | List recent documents (paginated, 20 default) |
| `gdocs.read` | GET | `/v1/documents/{documentId}` | Full document content + structure |
| `gdocs.search` | GET | `/drive/v3/files?q=...` | Search documents by name/metadata (uses Drive API) |
| `gdocs.create` | POST | `/v1/documents` | Create a blank document with given title |

## Usage examples

```powershell
$token = "<agent-token>"
$base = "http://127.0.0.1:<port>/v1/workspaces/<ws>/op"

# List recent documents
Invoke-RestMethod -Uri "$base/gdocs.list" -Method Post `
  -Body '{"pageSize":5}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Read a document
Invoke-RestMethod -Uri "$base/gdocs.read" -Method Post `
  -Body '{"documentId":"1abc123..."}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Search by name
Invoke-RestMethod -Uri "$base/gdocs.search" -Method Post `
  -Body '{"q":"name contains \"quarterly\"","pageSize":10}' `
  -ContentType "application/json" -Headers @{"Authorization" = "Bearer $token"}

# Create a new document
Invoke-RestMethod -Uri "$base/gdocs.create" -Method Post `
  -Body '{"title":"My New Doc"}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}
```

## Caveats

- **Token expires** — standard Google OAuth tokens last 1 hour. Configure `oauth` on the site environment for automatic refresh (see [Auto-refresh](#auto-refresh)); without it you must re-provision before expiry.
- **Drive vs Docs API** — `gdocs.list` and `gdocs.search` use the Drive API (different
  `base_url`), wired via explicit `/drive/v3/...` paths. The Docs API only supports
  read-by-id and create.
- **No update support** — `gdocs.update` is not implemented. The Docs API batchUpdate
  requires structured `requests` JSON (insert, delete, replaceAllText), which is complex
  to parameterize through the generic operation template. Add it if your agent can
  construct the request body.
- **Rate limits** — Google APIs have per-project quotas (~60 req/user/s for Docs,
  ~1000 req/100s for Drive). The gateway adds no throttling.
