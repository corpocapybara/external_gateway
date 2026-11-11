# TODO.md migration, 2026-11-11

Source: git blob 52bf02e66ee0009d758957cdf2e5c7e5eb9e14a3 of TODO.md at c090b9d, 11 open items, 0 checked.

| source line | source title (first 60 chars) | todo id | bucket |
|---|---|---|---|
| 10 | No Grafana op: prod logs and metrics go through the browser | T0001 | ops-config |
| 16 | `kibana.search` has no stg/dev `base_url`, and thp's docs ca | T0002 | ops-config |
| 20 | `jira.search` returns a fixed field set; callers can't ask f | T0003 | ops-config |
| 25 | The thp Jira write ops don't cover create, links, versions o | T0004 | ops-config |
| 30 | GitLab writes and MR fields missing, so GitLab work falls ba | T0005 | ops-config |
| 35 | An expired GitLab PAT fails silently until a call returns `i | T0006 | server |
| 39 | No Jenkins test-report op | T0007 | ops-config |
| 43 | `corpjira` can't download attachment content | T0008 | ops-config |
| 46 | No Thunderpick admin-panel connector; agents capture the adm | T0009 | connectors |
| 51 | `postgres.schema` has no `schema` param | T0010 | connectors |
| 54 | `jenkins.build_with_params` passes only `PUBLISH`, so a para | T0011 | ops-config |

Unmapped: 0

Preamble (not imported, verbatim):

> Open gaps in egw, one item each, with the evidence that shows it. Remove an item once it ships.
>
> Browser counts below are Claude in Chrome / built-in browser calls from 2026-10-14 to 2026-11-04, mined by
> `C:\dev\_tools\scripts\session-review\browser_egw.py`. In almost all of them an agent used the user's logged-in tab
> as an API proxy (same-origin `fetch`), which egw is meant to replace. `javascript_tool` cuts output at about 1,000
> chars, so each read becomes several paging calls.
