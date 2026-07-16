# Authentik SSO Integration (Globex Workspace)

Authentik instance: `https://authentik.example.com/`

## Auth

The Authentik site uses `bearer` auth with a token stored in Windows Credential Manager:

| Secret | Purpose |
|--------|---------|
| `secret://globex/authentik-token` | Authentik API token (Bearer auth, admin-level for full API access) |

### Getting an API token

1. Log in to `https://authentik.example.com/` as an admin
2. Go to **Admin interface → Tokens → API Tokens**
3. Create a new token with appropriate expiry
4. Copy the token value (shown only once)
5. Set it in the gateway:

```powershell
.\egw-admin.exe -workspace globex -name authentik-token -value "ak-xxx..."
```

## Operations

### User operations

| Operation | Method | Endpoint | Description |
|-----------|--------|----------|-------------|
| `authentik.whoami` | GET | `/api/v3/core/users/me` | Current user identity + group memberships |
| `authentik.users` | GET | `/api/v3/core/users` | List/search users |
| `authentik.user` | GET | `/api/v3/core/users/{id}` | Get single user details + groups |
| `authentik.user.update` | PUT | `/api/v3/core/users/{id}` | Update username, name, email, active status |

### Group operations (for role/tenant/club hierarchy)

| Operation | Method | Endpoint | Description |
|-----------|--------|----------|-------------|
| `authentik.groups` | GET | `/api/v3/core/groups` | List/search groups |
| `authentik.group` | GET | `/api/v3/core/groups/{id}` | Get group details + members |
| `authentik.group.create` | POST | `/api/v3/core/groups` | Create a new group |
| `authentik.group.update` | PUT | `/api/v3/core/groups/{id}` | Update group name or parent |
| `authentik.group.delete` | DELETE | `/api/v3/core/groups/{id}` | Delete a group |
| `authentik.group.add_user` | POST | `/api/v3/core/groups/{id}/add_user` | Add user to group (body: `{"pk": user_pk}`) |
| `authentik.group.remove_user` | POST | `/api/v3/core/groups/{id}/remove_user` | Remove user from group |

### Token operations

| Operation | Method | Endpoint | Description |
|-----------|--------|----------|-------------|
| `authentik.tokens` | GET | `/api/v3/core/tokens` | List API tokens |
| `authentik.token.create` | POST | `/api/v3/core/tokens` | Create a new API token |
| `authentik.token.delete` | DELETE | `/api/v3/core/tokens/{identifier}` | Revoke an API token |

### Blueprint operations (IaC)

| Operation | Method | Endpoint | Description |
|-----------|--------|----------|-------------|
| `authentik.blueprints` | GET | `/api/v3/core/blueprints` | List installed blueprints + status |
| `authentik.blueprint.apply` | POST | `/api/v3/core/blueprints/{id}/apply` | Re-apply a blueprint |

### Provider and Application operations (OIDC setup)

| Operation | Method | Endpoint | Description |
|-----------|--------|----------|-------------|
| `authentik.providers` | GET | `/api/v3/providers/oauth2` | List OAuth2/OIDC providers |
| `authentik.applications` | GET | `/api/v3/core/applications` | List applications with provider info |

## SRSP coverage

These operations cover the Authentik API needs of `SRSP-wire-md-to-authentik.md`:

| SRSP task | Covered by |
|-----------|-----------|
| INF-AUTH-01 (create OAuth2 provider + app) | `authentik.providers`, `authentik.applications` (list/verify; create is UI/blueprint) |
| INF-AUTH-02 (define tenant/role/club groups) | `authentik.group.create`, `authentik.group.update`, `authentik.group.delete` |
| INF-AUTH-03 (scope mappings) | UI/blueprint (Python expressions, not REST API) |
| INF-AUTH-04 (service account) | `authentik.token.create` |
| INF-AUTH-05 (admin RBAC) | UI (Authentik internal RBAC) |
| Epic B (UpdateRole → group membership) | `authentik.group.add_user`, `authentik.group.remove_user` |

## Usage examples

```powershell
$token = "<agent-token>"
$base = "http://127.0.0.1:8443/v1/workspaces/globex/op"

# Who am I?
Invoke-RestMethod -Uri "$base/authentik.whoami" -Method Post `
  -Body "{}" -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Search users
Invoke-RestMethod -Uri "$base/authentik.users" -Method Post `
  -Body '{"search":"krzysztof"}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Create a group for a tenant
Invoke-RestMethod -Uri "$base/authentik.group.create" -Method Post `
  -Body '{"name":"tenants/umbrella/admins"}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Add user to a role group (Epic B: UpdateRole)
Invoke-RestMethod -Uri "$base/authentik.group.add_user" -Method Post `
  -Body '{"id":"tenants/umbrella/admins","pk":42}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Remove user from a group
Invoke-RestMethod -Uri "$base/authentik.group.remove_user" -Method Post `
  -Body '{"id":"tenants/umbrella/users","pk":42}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# Create a service account token
Invoke-RestMethod -Uri "$base/authentik.token.create" -Method Post `
  -Body '{"identifier":"example-backend-m2m","user":1,"description":"Example backend client_credentials"}' `
  -ContentType "application/json" -Headers @{"Authorization" = "Bearer $token"}

# Revoke a token
Invoke-RestMethod -Uri "$base/authentik.token.delete" -Method Post `
  -Body '{"identifier":"example-backend-m2m"}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# List OIDC providers
Invoke-RestMethod -Uri "$base/authentik.providers" -Method Post `
  -Body '{}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}

# List applications
Invoke-RestMethod -Uri "$base/authentik.applications" -Method Post `
  -Body '{}' -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}
```
