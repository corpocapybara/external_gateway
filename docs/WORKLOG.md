# Jira time tracking (worklogs / Tempo)

The `jira.worklog_*` ops read and write Tempo worklogs through the Tempo REST API v4. Tempo Cloud writes every worklog
to Jira under its app user ("Timesheets by Tempo - Jira Time Tracking"), so Jira's own worklog author never matches
you and a Jira-only read reports 0 h (found 2026-10-30). Connector `jira-worklog`, code in
`internal/server/jira_worklog.go`.

Two sites are used:
- `tempo` (fixed name): `base_url: https://api.tempo.io`, `auth.type: bearer`, `secret_ref: secret://<ws>/tempo`.
  The token comes from Tempo > Settings > Data Access > API integration and needs worklog view and manage access.
  The ops refuse to run without this site. The universal URL works for every Tempo region (`api.eu.tempo.io` and
  `api.us.tempo.io` only for data in that cluster).
- the op's `site` (the workspace's `jira`, default environment only): your account id (`/myself`), issue keys,
  summaries and parents, the issue scope check, and display names for `worklog_issue`.

Store the token without putting it on a command line: copy it to the clipboard, then
`Get-Clipboard | .\egw-admin.exe -workspace <ws> -name tempo -admin http://127.0.0.1:<port>/admin -token <admin>`.

## Working-hours norm

8 h for every Monday–Friday that is not a Polish public holiday; a holiday on a Saturday or Sunday lowers nothing.
Holidays (`internal/workcal`): 1 and 6 Jan, Easter Sunday and Monday, 1 and 3 May, Zielone Świątki, Boże Ciało,
15 Aug, 1 and 11 Nov, 24 Dec (from 2025), 25 and 26 Dec. A worklog's day is Tempo's `startDate` (the date you
logged it on); "today" is the calendar day in Europe/Warsaw. Weeks run Monday–Sunday.

## Operations

| Operation | Params | Returns |
|-----------|--------|---------|
| `jira.worklog_report` | `period` day/week/month (default week), `date` YYYY-MM-DD (default today) | Days of the period with your worklogs and holidays, totals per issue, and `week` / `month` counters for `date` |
| `jira.worklog_issue` | `key` | Estimate, remaining, time spent (Jira), hours by author, your hours, every worklog (Tempo) |
| `jira.worklog_add` | `key` **or** `activity`, `hours`, `date`, `start` HH:MM (default 09:00), `comment` | The created worklog; a `note` when the day is a weekend or holiday (**write**) |
| `jira.worklog_update` | `key`, `worklog_id`, any of `hours`, `date`, `start`, `comment` | Before and after; fields not given are kept, work attributes too (**write**, own worklogs only) |
| `jira.worklog_delete` | `key`, `worklog_id` | The deleted worklog (**write**, own worklogs only) |

`worklog_id` is the Tempo worklog id (`id` in the report), not a Jira worklog id. Update and delete refuse a worklog
written by someone else or not on `key`. Tempo's own rules apply to writes (closed periods, required work
attributes); its error is passed back. `worklog_issue` lists the worklogs your Tempo token may see, which can be
only your own; `time_tracking.time_spent_hours` is Jira's total for everyone.
| `jira.worklog_activities` | — | The activity map |
| `jira.worklog_activity_set` | `name` (lower-case slug), `key` | Maps an activity to an issue (checked to exist); returns the map (**write**) |
| `jira.worklog_activity_remove` | `name` | Returns the map (**write**) |

Counter fields (`week`, `month`, and `period.norm`): `working_days`, `norm_hours`, `norm_to_date_hours` (working
days up to and including `date`), `logged_hours`, `left_hours` (to reach the norm, never below 0),
`overtime_hours`, `balance_to_date_hours` (logged minus norm to date; negative means behind), `holidays`.

Only issues in the workspace's `jira_projects` are read or written. Time you log on other projects is not counted
in the report, so the counters would understate it.

## Activity map

Non-ticket work (code review, general support, meetings, …) is logged with `activity` instead of `key`; the map says
which issue, usually an epic, takes the time. It starts empty and is changed only through the ops above. It lives in
`worklog-activities.<workspace>.json` next to the deployed `config.yaml` (e.g.
`C:\Program Files\external_gateway\<ws>\`); `deploy/update.ps1` does not touch it.

## Examples

```powershell
$h = @{ Authorization = "Bearer <token>" }
$u = "http://127.0.0.1:8444/v1/workspaces/<ws>/op"
Invoke-RestMethod "$u/jira.worklog_report" -Method Post -Headers $h -ContentType application/json -Body '{"period":"month"}'
Invoke-RestMethod "$u/jira.worklog_activity_set" -Method Post -Headers $h -ContentType application/json -Body '{"name":"code-review","key":"THP-1234"}'
Invoke-RestMethod "$u/jira.worklog_add" -Method Post -Headers $h -ContentType application/json -Body '{"activity":"code-review","hours":1.5,"comment":"MR !42"}'
```
