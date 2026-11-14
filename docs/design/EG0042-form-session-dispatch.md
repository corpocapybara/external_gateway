# EG0042 design: Wire the form-session connector into op dispatch

Status: DESIGNED (2026-11-14). Round 1, two lanes: code+scope PASS, tests+edges FAIL (4 blocking), all fixed; round 2 verifier PASS.

## Decisions in brief

- D1: form-session ops run through `executeHttpConnector` (owner 2026-11-14): only the plain http-rest dispatch branch also takes `form-session`; it sets the session cookie in place of an auth header, reuses path/query/body building, proxy_host, insecure_skip_verify, no-redirect and `shapeResponse`, and skips the OAuth retry (§D1).
- D2: `internal/connectors/formsession` keeps only `Login` and a `Sessions` cache; `Connector.Execute`, its cookie fields and `cap_test.go` go (owner 2026-11-14; the cap is httprest's) (§D2).
- D3: credentials: `user` (login email) and `secret_ref` (password) of the site environment, resolved with `secrets.ResolveWithWorkspaceCheck` on a session-cache miss only; `auth.type` is ignored for form-session ops; the cookie is tainted (§D3).
- D4: `Sessions` keys by workspace, site and resolved env; one mutex per key serialises login; a failed login is not cached; `Invalidate(key, stale)` drops only the stale cookie; `Clear()` runs at all three `s.creds.clear()` sites in `handleAdmin`; nil `*Sessions` is safe (§D4).
- D5: login = POST `<proxied base>/login`, form `email`, `password`, no redirects, insecure per env; success = a `connect.sid` cookie and neither 401/403/5xx nor a redirect to `/login` (§D5).
- D6: re-login signal = 401, or 3xx whose Location path ends in `/login`; on it the request re-logs in once and retries once; a second signal or a failed re-login returns an error (§D6).
- D7: `checkEnvSelection` adds `form-session` (§D7).
- D8: every new error returns status 0 with an error, so `executeOp` audits allow with upstream_status 0 and answers `status:error` (§D8).
- D9: tests in `internal/server/form_session_test.go` build the server with `New` from config and ops files on disk; the concurrency test is gated by a `Sessions` wait hook, not by timing (§Test plan).
- D10: docs and a commented `codimd` site (`base_url`, `user`, `secret_ref`, no `auth`) in `config.example/config.yaml` (§Docs).

## Acceptance (restated, from todo/EG0042.md)

1. Round trip through the handler, allow_fields, upstream status (404/503 unfiltered), audit status.
2. Secret via ResolveWithWorkspaceCheck; pw-EG42-secret / sid-EG42 never in response or audit.
3. Login failures: 200 without cookie, 401, cookie plus failure signal (401, 302 to /login), 5xx.
4. Session isolation per workspace/site/env; cross-tenant secret_ref refused with 0 logins.
5. Re-login once on 401 or redirect to /login; no loop; failing re-login errors.
6. 4 concurrent first calls: exactly 1 login; `-race` clean.
7. Sessions dropped on /admin/reload and secret writes.
8. checkEnvSelection covers form-session (403 validation, 0 logins).
9. base_url empty/- errors with 0 logins; proxy_host and insecure_skip_verify honoured; cap is httprest's; formsession.Execute and cap_test.go removed.
10. Docs (CONNECTORS, PITFALLS, README) and a commented codimd site in config.example/config.yaml.
11. Regression command passes; wall times recorded.

## Anchors

- internal/server/server.go: func (s *Server) executeOp (dispatch chain, audit LogAllow, status:error response)
- internal/server/server.go: func (s *Server) executeHttpConnector (baseURL/proxy, auth chain, OAuth 401 retry, shapeResponse)
- internal/server/server.go: func checkEnvSelection
- internal/server/server.go: func (s *Server) handleAdmin (the three `s.creds.clear()` calls: secret delete, secret set, reload)
- internal/server/server.go: type Server; func New
- internal/server/credential_status.go: func (c *credCache) clear (nil-safe pattern)
- internal/connectors/httprest/connector.go: func noRedirect; func NewConnector
- internal/connectors/formsession/connector.go: func (c *Connector) Login; func (c *Connector) Execute
- internal/secrets/resolver.go: func SetStore; func (r *Resolver) FlushCache; func (r *Resolver) Resolve (taints the secret)
- internal/server/jira_link_precheck_test.go: func auditRecords
- internal/server/admin_hardening_test.go: func sha (config/ops files on disk, `New`, admin token)
- internal/audit/logger.go: func Init (process-global, first call wins); func (l *Logger) Dir

## Decisions

**D1 Through http-rest.** In `executeOp` only the last http-rest branch, `op.Connector == "http-rest"`, becomes `op.Connector == "http-rest" || op.Connector == "form-session"`; the Slack-file and Jira-attachment branches stay http-rest only. In `executeHttpConnector`, after the base URL (with proxy_host) is computed and checked, a new first branch of the auth chain handles `op.Connector == "form-session"` (D3, D4) and sets `headers["Cookie"] = "connect.sid=" + cookie`, overwriting any `Cookie` from `op.Headers` (no site sets one today besides Jenkins, which is not form-session); the existing `env.Auth.Type` branches become `else if`. Right after the first `s.httpClient.Execute`, before the OAuth block, D6 runs for form-session ops; the OAuth block's guard gains `op.Connector == "http-rest"`, so a form-session env never triggers an OAuth refresh whatever its `auth`. Everything else (path template, query, body_template, headers, insecure client, size cap, logging, `shapeResponse`) is unchanged. Rejected: an own executor duplicating ~150 lines.

**D2 formsession package.** `Connector` keeps a normal and an insecure `http.Client` (30 s timeout, `CheckRedirect` returning `http.ErrUseLastResponse`; httprest's `noRedirect` is unexported, so formsession has its own three-line copy), `Name`, and `Login(ctx, baseURL, email, password string, insecure bool) (string, error)`. New `sessions.go`: `type Sessions`, `NewSessions`, `Cookie`, `Invalidate`, `Clear`, `SetWaitHook`. `Execute`, `sessionCookie`, `baseURL`, `UnmarshalJSON`, `DefaultMaxBodyBytes` and `cap_test.go` are removed (grep shows no users outside the package).

**D3 Credentials.** Empty `env.User` or `env.SecretRef` returns `form-session environment <env> needs user and secret_ref` before any login. The password is resolved inside the login function passed to `Sessions.Cookie`, so only on a cache miss: `secrets.ResolveWithWorkspaceCheck(parseSecretRef(env.SecretRef), workspace)`; a refusal returns its error, with no login and nothing cached. A cached cookie skips the check; that is safe because a config change (the only way the cross-tenant answer changes) goes through `/admin/reload`, which clears the sessions, and a password change goes through `/admin/secrets`, which does too. `Resolve` already taints the password (resolver.go); each cookie value is passed to `secrets.GetTaintRegistry().Taint`, so `writeRedactedJSON` and log previews redact it.

**D4 Sessions.** Key = workspace + "\x00" + site + "\x00" + envName, envName from `resolveEnv`. `Cookie(key, login func() (string, error)) (string, error)`: take the map lock, get or create the entry, release; call the wait hook if set; take the entry lock; return the cached cookie, else call login, store on success, return. `Invalidate(key, stale)` clears the entry only if it still holds `stale`, so a newer cookie from a concurrent re-login survives. `Clear()` replaces the map; a login in flight across a Clear stores into the orphaned entry and is lost (one extra login later, never a stale cookie served from the new map). Nil receiver: `Clear` and `Invalidate` are no-ops and `Cookie` returns `form-session sessions not initialised`, so the ~17 test files that build a literal `&Server{}` and never run a form-session op are unaffected. `SetWaitHook(f func())` is a test hook (documented as such), nil in production. `Server` gets `formLogin *formsession.Connector` and `formSessions *formsession.Sessions`, set in `New`; `handleAdmin` calls `s.formSessions.Clear()` next to each of its three `s.creds.clear()` calls (secret delete, secret set, reload).

**D5 Login.** URL = `strings.TrimRight(baseURL,"/") + "/login"`, where `baseURL` is the one `executeHttpConnector` computed (proxy_host/proxy_port applied), and `insecure = env.InsecureSkipVerify`, so both settings hold for the login as for the data request. POST, `Content-Type: application/x-www-form-urlencoded`, body `email=<user>&password=<pw>`. Body drained up to 1 MiB and discarded. Outcomes, checked in this order (errors never contain the password or cookie; a `*url.Error` is unwrapped as in httprest): 401/403 `form-session login refused (status N)`; 5xx `form-session login failed (status N)`; 3xx with `NeedsLogin` true `form-session login refused`; no `connect.sid` `form-session login: no session cookie`; else success, which includes a 302 to `/` with the cookie (the usual Express answer). CodiMD's exact responses are UNVERIFIED (owner: no live instance).

**D6 Re-login.** `formsession.NeedsLogin(status int, location string) bool`: true for 401, and for 3xx whose Location, parsed with `url.Parse` (absolute or relative, query ignored), has a path ending in `/login`. A 403 or a 3xx elsewhere is not a signal and passes through like any upstream status. On true: `Invalidate(key, cookie)`, `Cookie` again (logs in), rebuild `headers["Cookie"]`, retry with the same `urlStr`, `body` and other headers once. A second signal returns `form-session: upstream still asks for login after re-login`. Counts: one expiry = 2 logins, 2 data requests; always signalling = 2 logins, 2 data requests, error; failing re-login = 2 login attempts, 1 data request, error.

**D7 Env selection.** `checkEnvSelection`'s case list gets `"form-session"`; `checkTunnelAccess` and the disabled-site check are generic and unchanged. The empty-base_url check runs after the proxy rewrite (existing order, unchanged), so it fires only without proxy_host.

**D8 Errors and audit.** Every error D3-D6 add returns `(0, nil, err)`. `executeOp` then audits `LogAllow` with upstream_status 0 (the record omits the field, `omitempty`) and writes `status:"error"` with the error text through `writeRedactedJSON`. A data response, 2xx or not, returns its real status, which the audit record and `upstream_status` carry.

## Test plan

All in `internal/server/form_session_test.go` unless named otherwise. Helper `newFormSessionServer(t, opts)`:
- Upstream: `httptest.NewServer` (or `NewTLSServer`) whose handler counts logins and data requests (atomic), records the cookie, path, query and login password it got, and answers per test. Default login answer: 302 `Location: /` with `Set-Cookie: connect.sid=sid-EG42`.
- Secrets: a `memSecretStore` (mutex-guarded map whose `Set`/`Delete` write, unlike `fakeSecretStore`) holding `<ws>/codimd` = `pw-EG42-secret`; `secrets.SetStore` + `GetResolver().FlushCache()`, restore `&secrets.WinCredStore{}` and flush in `t.Cleanup`.
- Config and ops written to `t.TempDir()` as in admin_hardening_test.go: `admin_token_sha256: sha(adminTok)`, workspace with site `codimd` (env `prod`: `base_url` = upstream URL, `user`, `secret_ref: secret://<ws>/codimd`), a role with the op, an agent with `token_sha256: sha(agentTok)` granted the role. Op `codimd.note`: `connector: form-session`, `site: codimd`, `method: GET`, `path: /api/notes/{noteId}`, params `noteId` (required) and `view` (string, goes to the query), `response.allow_fields: [id, title]`.
- `audit.Init(t.TempDir(), 1)` then `os.MkdirAll(audit.GetLogger().Dir(), 0o700)` (Init is process-global, first call wins; jira_link_precheck_test.go does the same); `s := New(cfg, reg, opsPath, zerolog.Nop())`.
- Calls go through `s.ServeHTTP` with `Authorization: Bearer <agentTok>` on POST `/v1/workspaces/<ws>/op/codimd.note`.
- `assertNoLeak(t, bodies...)`: neither secret value in any response body nor in any `audit.GetLogger().Dir()/audit_*.log` (strings.Contains); run at the end of every test below.

Tests:
1. TestFormSessionOpRoundTrip (acc 1, 2): subtests 200 (cookie `sid-EG42`, path `/api/notes/n1`, query `view=x` at upstream; response has `id`, `title`, not `secret_field`; `upstream_status` 200; audit record via `auditRecords` has upstream_status 200), 404 and 503 with a body (that status, unfiltered body, audit status).
2. TestFormSessionLoginFails (acc 3, 2), 5 subtests: 200 without cookie; 401; cookie plus 401; cookie plus 302 to `/login`; 500. Each: `status:"error"`, an error text, data requests 0, no leak.
3. TestFormSessionSessionsIsolated (acc 4): (a) two sites of one workspace on two upstreams, each sees only its own cookie (`sid-A`, `sid-B`), 1 login each; (b) a second workspace with the same site name on a third upstream logs in on its own, 1 login per upstream; (c) a `secret_ref` naming another workspace: error, login count 0.
4. TestFormSessionRelogin (acc 5): (a) 401 once then 200, and 302 to `/login` once then 200: 2 logins, 2 data requests, success; (b) always 401: 2 logins, 2 data requests, error; (c) re-login answered 401: 2 login attempts, 1 data request, error; (d) data 403 and data 302 to `/other`: passed through with that status, 1 login, 1 data request.
5. TestFormSessionConcurrentCalls (acc 6): `s.formSessions.SetWaitHook` sends on an `arrived` channel; the upstream login handler waits until it has received 4 arrivals (fails the test after 5 s instead of hanging), then answers. 4 goroutines behind a start barrier (closed channel), no sleeps. Asserts all 4 succeed and login count 1. Without the per-entry lock all 4 would log in, so the test fails; the implementer proves this once by removing the lock, running the test, and reverting, and records it.
6. TestFormSessionDroppedOnAdmin (acc 7): subtests reload (POST `/admin/reload`), secret set (POST `/admin/secrets` writing a new password `pw-EG42-rotated`; the next login carries it) and secret delete (DELETE `/admin/secrets` of an unrelated secret). Each: login count 1 before, 2 after the next call. Admin calls use `Authorization: Bearer <adminTok>` as admin_hardening_test.go does.
7. TestFormSessionEnvSelection (acc 8): site with envs `a` and `b`, no `default_env`, call without `env`: 403 `rule:"validation"`, login count 0.
8. TestFormSessionEnvFields (acc 9, D3): base_url `""` and `"-"` (no proxy_host): error naming the env, login count 0; empty `user`, empty `secret_ref`: the D3 error, login count 0; proxy_host: `base_url: http://codimd.invalid`, `proxy_host: 127.0.0.1`, `proxy_port` = upstream port: login and data both reach the upstream; insecure_skip_verify on `httptest.NewTLSServer`: true succeeds, false fails at login with data requests 0.
9. `internal/connectors/formsession/sessions_test.go`: TestFormSessionInvalidateKeepsNewerCookie (unit) and TestFormSessionNeedsLogin (table: 401; 302 `/login`, `http://h/login`, `/x/login`, `/login?next=/n`: true; 302 `/`, 403, 200, 302 `/loginx`: false).

Race (acc 6): `go test -race -run FormSession ./internal/server/ ./internal/connectors/formsession/` (both test files match `FormSession`); its wall time recorded separately from the regression run.
Regression (acc 11): `go test ./internal/server/ ./internal/connectors/formsession/ ./internal/policy/ ./internal/config/ ./cmd/egw-desktop/`, wall time before and after recorded in the tested note.

## Docs

Acc 10's files, plus `.ai/CONNECTORS.md` ~64, whose "also used by `formsession`" becomes false when `DefaultMaxBodyBytes` goes. `config.example/config.yaml` gets a commented-out site:

```yaml
# codimd:                      # form-session: logs in with user + password, keeps connect.sid
#   environments:
#     prod:
#       base_url: https://codimd.example.com
#       user: bot@example.com  # login email
#       secret_ref: secret://<ws>/codimd   # login password
```

No `auth` block: form-session ignores it (D3).

## Not changed

config schema (no new fields); the 3 CodiMD ops in config.example/operations.yaml; httprest; tunnel and disabled-site checks; OAuth refresh for http-rest ops; the order of the base_url and proxy checks.
