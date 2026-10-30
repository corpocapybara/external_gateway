# Jira time tracking (worklogs / Tempo)

The `jira.worklog_*` ops read and write Jira Cloud worklogs, which is the data Tempo Timesheets shows. No Tempo API
token is needed: they reuse the workspace's `jira` site (default environment, its basic auth secret). Connector
`jira-worklog`, code in `internal/server/jira_worklog.go`.

## Working-hours norm

8 h for every Monday–Friday that is not a Polish public holiday; a holiday on a Saturday or Sunday lowers nothing.
Holidays (`internal/workcal`): 1 and 6 Jan, Easter Sunday and Monday, 1 and 3 May, Zielone Świątki, Boże Ciało,
15 Aug, 1 and 11 Nov, 24 Dec (from 2025), 25 and 26 Dec. Dates are calendar days in Europe/Warsaw. Weeks run
Monday–Sunday.

## Operations

| Operation | Params | Returns |
|-----------|--------|---------|
| `jira.worklog_report` | `period` day/week/month (default week), `date` YYYY-MM-DD (default today) | Days of the period with your worklogs and holidays, totals per issue, and `week` / `month` counters for `date` |
| `jira.worklog_issue` | `key` | Estimate, remaining, time spent, hours by author, your hours, every worklog |
| `jira.worklog_add` | `key` **or** `activity`, `hours`, `date`, `start` HH:MM (default 09:00), `comment` | The created worklog; a `note` when the day is a weekend or holiday (**write**) |
| `jira.worklog_update` | `key`, `worklog_id`, any of `hours`, `date`, `start`, `comment` | Before and after; fields not given are kept (**write**, own worklogs only) |
| `jira.worklog_delete` | `key`, `worklog_id` | The deleted worklog (**write**, own worklogs only) |
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
