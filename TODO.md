# TODO

Open gaps in egw, one item each, with the evidence that shows it. Remove an item once it ships.

Browser counts below are Claude in Chrome / built-in browser calls from 2026-10-14 to 2026-11-04, mined by
`C:\dev\_tools\scripts\session-review\browser_egw.py`. In almost all of them an agent used the user's logged-in tab
as an API proxy (same-origin `fetch`), which egw is meant to replace. `javascript_tool` cuts output at about 1,000
chars, so each read becomes several paging calls.

- [ ] **No Grafana op: prod logs and metrics go through the browser** (2026-11-04). This is the largest browser cost:
  759 calls, about 641k result chars, 23 sessions on `grafana-prod.<prod-domain>`. The fetches were
  `/api/datasources/proxy/uid/elasticsearch-api/_msearch` (128) and `/api/ds/query` (69, InfluxDB). thp's
  `docs/workspace/external-gateway.md#grafana-logs` already names the target: `grafana.es` (`_msearch`, index
  `logs-thunderpick.*`) and `grafana.influx` (read-only InfluxQL/Flux). The open question is auth: Grafana sits behind
  Teleport SSO, so egw needs a Grafana service-account token and a network route to it (unverified).
- [ ] **`kibana.search` has no stg/dev `base_url`, and thp's docs call `kibana.*` dead** (2026-11-04). Stg and dev
  are `base_url: ""  # TODO` in `config.thp.yaml`. Session a466da94 (10-16) spent 61 browser calls scraping the
  virtualized Discover grid on `kibana-dev.<dev-domain>`. Find out why prod is dead (Teleport plus a native Elastic
  login), then either fix it or remove the ops and point the docs at `grafana.es`.
- [ ] **`jira.search` returns a fixed field set; callers can't ask for `fields`** (2026-11-04). The params are only
  `jql, maxResults, nextPageToken, env`. Session 5e46f9cf (10-16) got
  `{"detail":"unknown parameter: fields","rule":"validation"}`, and the docs send stripped fields (Release Notes
  `customfield_10333`, components) to browser `POST /rest/api/3/search/jql` (40 fetches). Add an allowlisted
  `fields` param and keep response shaping.
- [ ] **The thp Jira write ops don't cover create, links, versions or `update`** (2026-11-04). On
  `ntropydev.atlassian.net` (383 browser calls, 42 sessions), the browser did `POST /rest/api/N/issue` (create; only
  cm has `jira.create`), `/issueLink` (6), `/project/THP/versions` (7), `/editmeta` (4), and the Ready For Test
  `PUT` with `{update:{fixVersions:[{add}]}}`. `jira.edit` takes only `fields`. Proposed ops: `jira.create`,
  `jira.link` (+ delete), `jira.versions`, `jira.editmeta`, and an `update` param on `jira.edit`.
- [ ] **GitLab writes and MR fields missing, so GitLab work falls back to the browser** (2026-11-04). 379 browser
  calls, 38 sessions, 111 `/api/v4/projects/…` fetches. `gitlab.mr_update` takes only `description`, so it can't
  retarget (`target_branch`). No op opens a new (diff) discussion, since `gitlab.mr_note` only replies to an existing
  one. `gitlab.mr` doesn't return `merge_method`, `squash` or `diverged_commits_count`. Group MR lists by
  `author_id` also went through the browser (0ec2bbfd, 10-15).
- [ ] **An expired GitLab PAT fails silently until a call returns `invalid_token`** (2026-11-04). There were 7
  `invalid_token` answers on 10-15/16 (0ec2bbfd, cf2c8d4f), and sessions switched to the browser. Report the
  token's `expires_at` (`GET /personal_access_tokens/self`) in `/capabilities` or `gitlab.whoami`, and warn when it
  is less than 7 days away.
- [ ] **No Jenkins test-report op** (2026-11-04). Session faa62c44 (10-14) read
  `/job/…/MR-N/N/testReport/api/json?tree=failCount,passCount,suites[cases[…]]` in the browser. `jenkins.build`
  only reads `/{job}/{number}/api/json`. Add `jenkins.test_report job* number* tree` (52 Jenkins browser calls in
  total, 128k chars, mostly `consoleText`/`get_page_text`).
- [ ] **`corpjira` can't download attachment content** (2026-11-04). Session 06e38a10 (10-23) fetched
  `/secure/attachment/<id>/<name>.png` as a blob in the browser to look at a screenshot. `corpjira.attachments`
  returns metadata only. Add `corpjira.attachment_content id*` (base64, size-capped, image/text types only).
- [ ] **No Thunderpick admin-panel connector; agents capture the admin session token from the page** (2026-11-04).
  On `admin-panel.<prod-domain>` (91 calls, 4 sessions), 61063612 (10-22) captured request headers and reused
  `thunderpick-token` for `/api/users/admin/wagers/…` and `/api/admin/users/…`. That puts a prod admin credential
  in the transcript, which egw exists to prevent. Add read-only `tpadmin.*` ops (wagers, user) with a service
  credential, or forbid the capture in thp's rules.
- [ ] **`postgres.schema` has no `schema` param** (2026-11-04). Session a466da94 (10-16) got
  `unknown parameter: schema`. The params are `env, db, table`, so tables outside `public` can't be described.

- [ ] **`jenkins.build_with_params` passes only `PUBLISH`, so a parameterized test job can't be started with chosen image tags** (2026-11-04). thp's develop regression `job/AutomatedTests/job/Regression/job/Automated.Tests.Regressions/job/develop` takes 23 Active Choices `*ContainerImageTag` params (automated-tests `src/Thunderpick.ApiTests/.jenkins/JenkinsfileRegression`, stage `Set versions`). Its for (2026-11-04, via /feedback).
