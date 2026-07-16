# Clockify Time Tracking

All Clockify operations use the `api-key` auth type with the `X-Api-Key` header.

## Operations

| Op | Method | Path | Description |
|----|--------|------|-------------|
| `clockify.workspace` | GET | `/workspaces` | List workspaces |
| `clockify.projects` | GET | `/workspaces/{workspace_id}/projects` | List projects (active_only by default) |
| `clockify.user` | GET | `/user` | Current user info |
| `clockify.entries` | GET | `/workspaces/{workspace_id}/user/{user_id}/time-entries` | Time entries (date range) |
| `clockify.today` | GET | `/workspaces/{workspace_id}/user/{user_id}/time-entries` | Today's entries (default start=today) |
| `clockify.active` | GET | `/workspaces/{workspace_id}/user/{user_id}/time-entries` | Currently running timer (`in_progress=true`) |
| `clockify.add` | POST | `/workspaces/{workspace_id}/time-entries` | Create a time entry |
| `clockify.edit_entry` | PUT | `/workspaces/{workspace_id}/time-entries/{id}` | Update a time entry |

All use `site: clockify`. The `workspace_id` and `user_id` are auto-resolved from site config.

## Examples

```powershell
$h = @{Authorization = "Bearer <agent-token>"}

# List workspaces
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.workspace" `
  -Method Post -Body '{"env":"prod"}' -ContentType "application/json" -Headers $h

# List projects
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.projects" `
  -Method Post -Body '{"env":"prod","active_only":true}' -ContentType "application/json" -Headers $h

# Get user info
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.user" `
  -Method Post -Body '{"env":"prod"}' -ContentType "application/json" -Headers $h

# Get today's entries
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.today" `
  -Method Post -Body '{"env":"prod"}' -ContentType "application/json" -Headers $h

# Get entries for a date range
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.entries" `
  -Method Post -Body '{"env":"prod","start_date":"2026-07-16T00:00:00Z","end_date":"2026-07-16T23:59:59Z"}' `
  -ContentType "application/json" -Headers $h

# Check active timer
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.active" `
  -Method Post -Body '{"env":"prod","in_progress":true}' -ContentType "application/json" -Headers $h

# Create a time entry
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.add" `
  -Method Post -Body (@{
    env         = "prod"
    description = "Task description"
    project_id  = "6977a1e57cf2e42b812ba27b"
    start       = (Get-Date).AddMinutes(-30).ToString("yyyy-MM-ddTHH:mm:ssZ")
    end         = (Get-Date).AddMinutes(-25).ToString("yyyy-MM-ddTHH:mm:ssZ")
    billable    = $true
  } | ConvertTo-Json) -ContentType "application/json" -Headers $h

# Edit a time entry (requires the entry ID from clockify.entries)
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/<ws>/op/clockify.edit_entry" `
  -Method Post -Body (@{
    env         = "prod"
    id          = "6a5932fc533f09b32e892046"
    description = "Updated description"
    start       = (Get-Date).AddMinutes(-45).ToString("yyyy-MM-ddTHH:mm:ssZ")
    end         = (Get-Date).AddMinutes(-30).ToString("yyyy-MM-ddTHH:mm:ssZ")
    billable    = $false
  } | ConvertTo-Json) -ContentType "application/json" -Headers $h
```

## Time format

Clockify accepts ISO 8601: `yyyy-MM-ddTHH:mm:ssZ` (UTC). Seconds precision is sufficient; no milliseconds needed.

## Caveats

- `clockify.add` requires at least `description` and a valid `project_id` (or omit project_id to create an entry without a project)
- `clockify.edit_entry` requires the entry `id` — get it from `clockify.entries` or `clockify.today`
- Time entries without project assignment work; omit `project_id` from the body
- The `X-Api-Key` secret is stored as `secret://cm/clockify` in Windows Credential Manager
