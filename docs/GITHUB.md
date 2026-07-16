# GitHub Personal Access Token

## Scopes needed

For the workspace operations (`github.*`), you need a **fine-grained PAT** or a
**classic PAT** with these scopes:

- `repo` (full control of private repos) — needed for PR, issues, contents, commits
- `read:org` — read org membership (optional)

## Getting a classic PAT

1. Open https://github.com/settings/tokens
2. Click **Generate new token** → **Generate new token (classic)**
3. Give it a name (e.g. "external-gateway")
4. Set expiration (max 1 year, or "No expiration" for classic)
5. Select scopes: `repo` (full control), `read:org`
6. Click **Generate token**
7. **Copy the token immediately** — you won't see it again

## Getting a fine-grained PAT

1. Open https://github.com/settings/tokens?type=beta
2. Click **Generate new token**
3. Set **Repository access** → **All repositories** (or select specific ones)
4. Under **Permissions**:
   - Contents: **Read** (for `github.contents`)
   - Issues: **Read and write** (for `github.issues`, `github.create_issue`)
   - Pull requests: **Read and write** (for `github.pr`, `github.create_pr`)
   - Commit statuses: **Read** (for `github.commits`)
5. Click **Generate token** and copy it

## Store in the gateway

```powershell
.\egw-admin.exe -workspace <ws> -name github -value "<token>" -admin http://127.0.0.1:<port>/admin
```
