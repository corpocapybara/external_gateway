# OneDrive / Microsoft Graph Integration

OneDrive access through the gateway using a **bearer OAuth 2.0 access token** for Microsoft
Graph. Agents call allow-listed `onedrive.*` operations; the gateway injects the token from
the SYSTEM Credential Manager.

## Auth model

The `onedrive` site uses `bearer` auth against `https://graph.microsoft.com/v1.0`. The
gateway sends `Authorization: Bearer <token>` on every request.

Required OAuth scope:
- `Files.Read` or `Files.ReadWrite` — read/write OneDrive files
- `Files.Read.All` / `Files.ReadWrite.All` — access files shared with the user (if needed)

Getting a Delegated permission token (user context) for Microsoft Graph requires a
registered app in Azure AD.

## Getting a token

### Option 1: Device Authorization Grant with refresh token (recommended for auto-refresh)

Requires an **App Registration** in Azure AD (https://portal.azure.com → App registrations):

1. Create a new app registration (e.g. "external-gateway")
2. Under **Authentication** → **Mobile and desktop applications** → add
   `https://login.microsoftonline.com/common/oauth2/nativeclient` as redirect URI
3. Under **API Permissions**, add Microsoft Graph **Delegated permissions**:
   - `Files.Read` (or `Files.ReadWrite`)
4. Note the **Application (client) ID** and create a **Client secret**

Use the Device Authorization Grant flow (no redirect server needed):

```powershell
# Step 1: Get device code (use your app's Client ID)
$body = @{
  client_id = "your-client-id"
  scope     = "Files.Read offline_access"
}
$deviceCode = Invoke-RestMethod -Uri "https://login.microsoftonline.com/common/oauth2/v2.0/devicecode" `
  -Method Post -Body $body

# Step 2: Open the verification_uri in a browser and enter the user_code
Write-Host "Open $($deviceCode.verification_uri) and enter code: $($deviceCode.user_code)"
Start-Process $deviceCode.verification_uri

# Step 3: Poll for the token (includes refresh_token because of offline_access scope)
$tokenBody = @{
  client_id   = "your-client-id"
  grant_type  = "urn:ietf:params:oauth:grant-type:device_code"
  device_code = $deviceCode.device_code
}
do {
  Start-Sleep -Seconds 5
  try {
    $tokenResponse = Invoke-RestMethod -Uri "https://login.microsoftonline.com/common/oauth2/v2.0/token" `
      -Method Post -Body $tokenBody
    break
  } catch { }
} while ($true)

# Store all three secrets
$tokenResponse.access_token  | .\egw-admin.exe -workspace <ws> -name onedrive
$tokenResponse.refresh_token | .\egw-admin.exe -workspace <ws> -name onedrive-refresh
# Store the client secret:
.\egw-admin.exe -workspace <ws> -name onedrive-client-secret -value "<your-client-secret>"
```

### Option 2: Microsoft Graph Explorer (access token only, no auto-refresh)

1. Open https://developer.microsoft.com/en-us/graph/graph-explorer
2. Sign in with your Microsoft account
3. Click the gear icon → **Select permissions** → ensure `Files.Read`
4. Make any request (e.g. `GET /me/drive/root/children`)
5. Click the **Access token** tab in the response pane
6. Copy the token value

### Option 3: Azure CLI (access token only, no auto-refresh)

```powershell
az login --allow-no-subscriptions
az account get-access-token --resource https://graph.microsoft.com
# Copy the accessToken from the JSON output
```

## Auto-refresh

The gateway can automatically refresh expired Microsoft Graph access tokens. On a 401
response, the gateway uses the refresh token to obtain a new access token, stores it in
Credential Manager, and retries the request once.

### Config

```yaml
      onedrive:
        environments:
          prod:
            base_url: https://graph.microsoft.com/v1.0
            auth:
              type: bearer
            secret_ref: secret://<ws>/onedrive
            oauth:
              token_url: https://login.microsoftonline.com/common/oauth2/v2.0/token
              client_id: "your-client-id"
              client_secret_ref: secret://<ws>/onedrive-client-secret
              refresh_secret_ref: secret://<ws>/onedrive-refresh
```

### Secrets

| Secret ref | Value |
|------------|-------|
| `secret://<ws>/onedrive` | Microsoft Graph access token (initially, then managed by gateway) |
| `secret://<ws>/onedrive-refresh` | Microsoft Graph refresh token (long-lived, from `offline_access` scope) |
| `secret://<ws>/onedrive-client-secret` | Azure AD app client secret |

```powershell
.\egw-admin.exe -workspace <ws> -name onedrive -value "<initial-access-token>"
.\egw-admin.exe -workspace <ws> -name onedrive-refresh -value "<refresh-token>"
.\egw-admin.exe -workspace <ws> -name onedrive-client-secret -value "<client-secret>"
```

> The gateway automatically overwrites `secret://<ws>/onedrive` with a fresh access token
> on each refresh. The refresh token and client secret are read-only after setup.

## Operations

## Operations

| Op | Method | Path | Description |
|----|--------|------|-------------|
| `onedrive.list_files` | GET | `/me/drive/root/children` | List files/folders in root (paginated, 20 default) |
| `onedrive.read` | GET | `/me/drive/items/{itemId}` | Get file/folder metadata + download URL |
| `onedrive.search` | GET | `/me/drive/root/search(q='{q}')` | Search files by name/content |
| `onedrive.create_folder` | POST | `/me/drive/root/children` | Create folder (conflictBehavior: rename) |
| `onedrive.upload` | PUT | `/me/drive/root:/{name}:/content` | Upload file content (simple PUT, up to 4MB) |

## Usage examples

```powershell
$token = "<agent-token>"
$base = "http://127.0.0.1:<port>/v1/workspaces/<ws>/op"

# List files in root
Invoke-RestMethod -Uri "$base/onedrive.list_files" -Method Post `
  -Body '{"top":10}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Read file metadata
Invoke-RestMethod -Uri "$base/onedrive.read" -Method Post `
  -Body '{"itemId":"ABC123..."}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Search for files
Invoke-RestMethod -Uri "$base/onedrive.search" -Method Post `
  -Body '{"q":"budget","top":10}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Create a folder
Invoke-RestMethod -Uri "$base/onedrive.create_folder" -Method Post `
  -Body '{"name":"Reports"}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Upload a file (content is a string — the /content endpoint accepts raw bytes in body)
Invoke-RestMethod -Uri "$base/onedrive.upload" -Method Post `
  -Body '{"name":"notes.txt"}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}
```

## Caveats

- **Token expires** — Microsoft Graph access tokens last 1 hour. Configure `oauth` on the site environment for automatic refresh (see [Auto-refresh](#auto-refresh)); without it you must re-provision before expiry.
- **`onedrive.upload` uses simple PUT** — file content is the raw request body. The current
  operation template generates a JSON body from `body_template`, which is incompatible with
  raw binary upload. For real file uploads, the agent should use `onedrive.read` to get
  the upload URL, then upload directly (the token is exposed as a bearer header). This is
  a known limitation.
- **Large files** — simple PUT is limited to 4MB. For larger files, use the upload session
  API (`POST /me/drive/root:/{name}:/createUploadSession`) — not yet implemented.
- **No download commands** — `onedrive.read` returns `@microsoft.graph.downloadUrl` in the
  response, which the agent can use to download the file directly (no gateway auth needed
  for that URL — it's a temporary pre-signed URL).
- **Rate limits** — Microsoft Graph has per-app + per-user throttling (~10,000 req per
  10 minutes per app per tenant for reads). The gateway adds no throttling.
