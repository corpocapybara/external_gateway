# EG0042 design: Wire the form-session connector into op dispatch

Status: DRAFT, not yet reviewed by the design critics (2026-11-14).

## Decisions in brief

- D1: form-session ops run through `executeHttpConnector` (owner 2026-11-14): the dispatch chain sends `http-rest` and `form-session` there; it adds the session cookie in place of an auth header and reuses path/query/body building, proxy_host, insecure_skip_verify, no-redirect and `shapeResponse` (§D1).
- D2: `internal/connectors/formsession` keeps only `Login` and a `Sessions` cache; `Connector.Execute`, its cookie fields and `cap_test.go` go (owner 2026-11-14; the cap is httprest's) (§D2).
- D3: credentials: `user` (login email) and `secret_ref` (password) of the site environment, resolved with `secrets.ResolveWithWorkspaceCheck`; `auth.type` is ignored for form-session ops; password and cookie are tainted (§D3).
- D4: `Sessions` keys by workspace, site and resolved env; one mutex per key serialises login (exactly one login for concurrent first calls); a failed login is not cached; `Invalidate(key, stale)` drops only the stale cookie; `Clear()` (nil-safe) runs wherever `s.creds.clear()` runs in `handleAdmin` (§D4).
- D5: login = POST `<base>/login`, form `email`, `password`, no redirects; success = a `connect.sid` cookie and neither 401/403/5xx nor a redirect to `/login` (§D5).
- D6: re-login signal = 401, or 3xx whose Location path ends in `/login`; on it the request re-logs in once and retries once; a second signal or a failed re-login returns an error (§D6).
- D7: `checkEnvSelection` adds `form-session` (§D7).
- D8: tests in `internal/server/form_session_test.go` on httptest servers, `&Server{}` built with both new fields; no sleeps (§Test plan).

## Acceptance (restated, from todo/EG0042.md)

1. Round trip through the handler, allow_fields, upstream status (404/503 unfiltered), audit status.
2. Secret via ResolveWithWorkspaceCheck; pw-EG42-secret / sid-EG42 never in response or audit.
3. Login failures: 200 without cookie, 401, cookie plus failure signal, 5xx.
4. Session isolation per workspace/site/env; cross-tenant secret_ref refused with 0 logins.
5. Re-login once on 401 or redirect to /login; no loop; failing re-login errors.
6. 4 concurrent first calls: exactly 1 login; `-race` clean.
7. Sessions dropped on /admin/reload and secret writes.
8. checkEnvSelection covers form-session (403 validation, 0 logins).
9. base_url empty/- errors with 0 logins; proxy_host and insecure_skip_verify honoured; cap is httprest's; formsession.Execute and cap_test.go removed.
10. Docs (CONNECTORS, PITFALLS, README) and a commented codimd site in config.example/config.yaml.
11. Regression command passes; wall times recorded.

## Anchors

- internal/server/server.go: func (s *Server) executeOp (dispatch chain)
- internal/server/server.go: func (s *Server) executeHttpConnector (baseURL/proxy, auth chain, OAuth 401 retry, shapeResponse)
- internal/server/server.go: func checkEnvSelection
- internal/server/server.go: func (s *Server) handleAdmin (the `s.creds.clear()` calls)
- internal/server/server.go: type Server; func New
- internal/server/credential_status.go: func (c *credCache) clear (nil-safe pattern)
- internal/connectors/httprest/connector.go: func noRedirect; func NewConnector
- internal/connectors/formsession/connector.go: func (c *Connector) Login; func (c *Connector) Execute
- internal/secrets/resolver.go: func SetStore; func (r *Resolver) FlushCache
- internal/server/jira_link_precheck_test.go: func auditRecords
- internal/server/jenkins_crumb_test.go: type fakeSecretStore

## Decisions

**D1 Through http-rest.** In `executeOp` the branch `op.Connector == "http-rest"` becomes `op.Connector == "http-rest" || op.Connector == "form-session"`. In `executeHttpConnector`, after the base URL (with proxy_host) is computed and checked, a new first branch of the auth chain handles `op.Connector == "form-session"` (D3, D4) and sets `headers["Cookie"] = "connect.sid=" + cookie`; the existing `env.Auth.Type` branches become `else if`. After the response, next to the OAuth retry, D6 runs. Everything else (path template, query, body_template, headers, insecure client, size cap, logging, `shapeResponse`) is unchanged. Rejected: an own executor duplicating ~150 lines.

**D2 formsession package.** `Connector` keeps a normal and an insecure `http.Client` (30 s timeout, `CheckRedirect` returning `http.ErrUseLastResponse`), `Name`, and `Login(ctx, baseURL, email, password string, insecure bool) (string, error)`. New `sessions.go`: `type Sessions`, `NewSessions`, `Cookie`, `Invalidate`, `Clear`. `Execute`, `sessionCookie`, `baseURL`, `UnmarshalJSON`, `DefaultMaxBodyBytes` and `cap_test.go` are removed (grep shows no other users).

**D3 Credentials.** Empty `env.User` or `env.SecretRef` returns `form-session environment <env> needs user and secret_ref` before any login. The password comes from `secrets.ResolveWithWorkspaceCheck(parseSecretRef(env.SecretRef), workspace)`; a refusal returns its error (no login). Password and every cookie value are passed to `secrets.GetTaintRegistry().Taint`, so `writeRedactedJSON` and log previews redact them.

**D4 Sessions.** Key = workspace + "\x00" + site + "\x00" + envName. `Cookie(key, login func() (string, error))`: take the map lock, get or create the entry, release; take the entry lock; return the cached cookie, else call login, store on success, return. `Invalidate(key, stale)` clears the entry only if it still holds `stale`, so a newer cookie from a concurrent re-login survives. `Clear()` replaces the map (nil receiver: no-op). `Server` gets `formLogin *formsession.Connector` and `formSessions *formsession.Sessions`, set in `New`; `handleAdmin` calls `s.formSessions.Clear()` next to each `s.creds.clear()`.

**D5 Login.** POST `strings.TrimRight(base,"/") + "/login"`, `Content-Type: application/x-www-form-urlencoded`, body `email=<user>&password=<pw>`. Body drained up to 1 MiB and discarded. Errors (never containing the password or cookie; a `*url.Error` is unwrapped as in httprest): 401/403 `form-session login refused (status N)`; redirect to /login `form-session login refused`; 5xx `form-session login failed (status N)`; no `connect.sid` `form-session login: no session cookie`. CodiMD's exact responses are UNVERIFIED (owner: no live instance).

**D6 Re-login.** `formsession.NeedsLogin(status int, location string) bool`. On true: `Invalidate(key, cookie)`, `Cookie` again (logs in), retry the same request once. A second signal returns `form-session: upstream still asks for login after re-login`. Counts: one expiry = 2 logins, 2 data requests.

**D7 Env selection.** `checkEnvSelection`'s case list gets `"form-session"`; `checkTunnelAccess` and the disabled-site check are generic and unchanged.

## Test plan

`internal/server/form_session_test.go`, helper `newFormSessionServer(t, upstreams...)` (httptest upstream counting logins and data requests, `secrets.SetStore(fakeSecretStore{...})` + `FlushCache`, restore `&secrets.WinCredStore{}` in `t.Cleanup`, `audit.Init(t.TempDir(),1)`, `&Server{...}` with both new fields):
1. TestFormSessionOpRoundTrip (subtests 200, 404, 503) — acc 1, 2.
2. TestFormSessionLoginFails (4 subtests) — acc 3, 2.
3. TestFormSessionSessionsIsolated (a, b, c) — acc 4.
4. TestFormSessionRelogin (401 once, 302 once, always, re-login fails) — acc 5.
5. TestFormSessionConcurrentCalls (4 goroutines, start barrier) + `go test -race -run FormSession ./internal/server/ ./internal/connectors/formsession/` — acc 6.
6. TestFormSessionDroppedOnReload (reload and secrets-write subtests) — acc 7.
7. TestFormSessionEnvSelection — acc 8.
8. TestFormSessionEnvFields (base_url empty and "-", proxy_host, insecure_skip_verify on httptest TLS) — acc 9.
9. formsession: TestSessionsInvalidateKeepsNewerCookie (extra, unit).
Acc 10: grep from the acceptance. Acc 11: `go test ./internal/server/ ./internal/connectors/formsession/ ./internal/policy/ ./internal/config/ ./cmd/egw-desktop/`, wall time before/after.

## Not changed

config schema (no new fields); the 3 CodiMD ops in config.example/operations.yaml; httprest; tunnel and disabled-site checks; OAuth refresh.
