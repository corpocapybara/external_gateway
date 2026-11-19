# EG0043 design: Back off form-session logins after a failed login

## Decisions in brief

- D1 (owner 2026-11-20): the backoff window is a fixed 60 s, a package constant `loginBackoff` in
  `internal/connectors/formsession/sessions.go`; no config field, no growth. See D1.
- D2 (owner 2026-11-20): `formsession.ErrLoginRefused` (text `form-session login refused`) is wrapped by
  `Connector.Login` for a 401, a 403, a 3xx to the login page and any other non-5xx answer with no non-empty
  `connect.sid` cookie (a 200, a 3xx elsewhere, a 404, a 429); never for a 5xx or a transport error. Existing
  error texts keep their wording. See D2.
- D3: the backoff lives in `sessionEntry` (`refused error`, `until time.Time`), read and written under the entry's
  existing lock, so concurrent callers after a refusal see it and make no login. See D3.
- D4: `Cookie` order under `e.mu`: cached cookie, then backoff (`now() < until` returns the stored error with the
  suffix `; login not retried for <N>s`, N = remaining whole seconds rounded up, wrapped with `%w`), then login.
  A login error wrapping `ErrLoginRefused` stores it with `until = now()+loginBackoff`; any other error or a success
  clears it. See D4.
- D5: `Sessions.now func() time.Time` (nil = `time.Now`) with a test-only `SetNow`, like `SetWaitHook`; the func
  is copied under `s.mu`, called after `e.mu.Lock()` for the check and again after `login()` returns for `until`.
  See D5.
- D6: reset needs no new code: `Clear()` replaces the entry map and is already called on `/admin/reload`,
  `/admin/secrets` set and delete; a refused login in flight across `Clear()` writes into its orphaned entry. See D6.
- D7: the server is unchanged except tests: `formSessionCookie` and the re-login path already return the `Cookie`
  error, and a secret-resolve error does not wrap `ErrLoginRefused`. See D7.

## Acceptance

Restated from `todo/EG0043.md`:
1. `formsession.Login` wraps `ErrLoginRefused` for a 401, a 403, a redirect to the login page and a 200 with no
   session cookie, not for a 5xx or a transport error. Test: `TestLoginWrapsRefusal`.
2. Only an error wrapping `ErrLoginRefused` starts a fixed 60 s backoff (owner 2026-11-20); during it `Cookie`
   returns the stored error with the suffix `; login not retried for <N>s` and does not call login; success
   clears it; other keys unaffected; `now` field with a test-only setter. Tests: `TestFormSessionBackoffAfterRefusal`,
   `TestFormSessionNoBackoffOnOtherError`, `TestFormSessionBackoffExpires`, `TestFormSessionBackoffPerKey`,
   `TestFormSessionBackoffConcurrent`.
3. `Clear()` during an in-flight refused login leaves no backoff. Test: `TestFormSessionClearDuringRefusedLogin`.
4. Server: three sequential calls on a 401 login make one login; re-login refused also starts it. Tests:
   `TestFormSessionLoginBackoff`, `TestFormSessionReloginRefusedBackoff`.
5. Reload, secret set and delete reset it. Tests: `TestFormSessionBackoffResetByReload`,
   `TestFormSessionBackoffResetBySecretWrite`.
6. `go test ./internal/connectors/formsession/ ./internal/server/` passes.

## Anchors

- `internal/connectors/formsession/connector.go`: `Connector.Login`, `NeedsLogin`, `SessionCookieName`
- `internal/connectors/formsession/sessions.go`: `Sessions`, `sessionEntry`, `Sessions.Cookie`, `Sessions.Clear`,
  `Sessions.SetWaitHook`
- `internal/server/server.go`: `Server.formSessionCookie`, the form-session re-login block in
  `executeHttpConnector`, `handleAdmin` (`s.formSessions.Clear()` beside `s.oauthCooldown.clear()`)
- `internal/server/form_session_test.go`: `newFSUpstream`, `newFormSessionServer`, `fsCall`, `fsWantCounts`,
  `TestFormSessionLoginFails`, `TestFormSessionDroppedOnReload`, `TestFormSessionConcurrentCalls`
- `internal/server/admin_hardening_test.go`: `adminDo`

## Decisions

**D1 Window.** Owner 2026-11-20 chose a fixed 60 s over a per-env field (EG0044's shape) and an exponential
30 s to 5 min. `const loginBackoff = 60 * time.Second`. A rotated password is fixed by `/admin/secrets`, which
resets the backoff (D6), so a longer or configurable window buys little.

**D2 Refusal classes.** `var ErrLoginRefused = errors.New("form-session login refused")`. In `Login`:
401/403 -> `fmt.Errorf("%w (status %d)", ErrLoginRefused, code)`; 3xx with `NeedsLogin` -> `ErrLoginRefused`
itself; no `connect.sid` cookie on any other non-error answer -> `fmt.Errorf("%w: no session cookie",
ErrLoginRefused)` (owner 2026-11-20: a 200 with no cookie is a refusal). This is today's final fall-through, so a
3xx elsewhere, a 404 or a 429 with no cookie (or an empty one) is a refusal too: none of them can succeed sooner on
a retry, and a 429 is the site asking for exactly this backoff. 5xx keeps `form-session login failed (status %d)` and transport errors keep
`form-session login: %w`, neither wrapping the sentinel. `TestFormSessionLoginFails` texts stay true
(`login refused (status 401)`, `form-session login refused`, `no session cookie`, `login failed (status 500)`).

**D3 State.** `sessionEntry` gains `refused error` and `until time.Time`, guarded by `e.mu` like `cookie`. Holding
the login under `e.mu` already serialises callers per key, so the first refusal is visible to every waiter.

**D4 Cookie flow.** Under `e.mu`: `cookie != ""` -> return it. `refused != nil && now.Before(until)` -> return
`fmt.Errorf("%w; login not retried for %ds", refused, secs)` with `secs = ceil(until-now)` in whole seconds
(so `errors.Is(err, ErrLoginRefused)` holds). Otherwise call login; on `errors.Is(err, ErrLoginRefused)` set
`refused, until = err, now+loginBackoff`; on any other error or a success set `refused = nil`. An expired backoff
just logs in again (no separate expiry sweep).

**D5 Clock.** `Sessions.now func() time.Time`; `Cookie` copies the func with the hook under `s.mu` (`time.Now`
when nil) but calls it only after `e.mu.Lock()` (a waiter queued behind a slow login compares the current time) and
again after `login()` returns, for `until` (the window is not shortened by the login's duration). `SetNow(f)` is exported for tests only (same note as `SetWaitHook`; the server tests may use it).

**D6 Reset.** No new call sites: `Clear()` swaps the map, dropping every entry with its backoff. An in-flight login
holds its old entry; its refusal lands there and the next call gets a fresh entry and logs in. A caller that
already queued on the old entry's lock before `Clear()` still sees that refusal; that is acceptable (it was
in flight before the reset). `Invalidate` clears only the cookie and leaves `refused`/`until` alone (they are
empty whenever a cookie is cached).

**D7 Server.** No production change. A secret-resolve error is returned from the login closure unwrapped, so it
never backs off. The re-login path calls `formSessionCookie` again after `Invalidate`, so a refused re-login starts
the backoff and the next call returns the backoff error.

## Test plan

| Acceptance | Test |
|---|---|
| 1 | `TestLoginWrapsRefusal` (new `internal/connectors/formsession/connector_test.go`): httptest rows 401, 403, 302 to `/login`, 200 no cookie, 200 with an empty `connect.sid`, 302 to `/` no cookie, 404, 429 -> `errors.Is` true; 500 and a closed listener (transport) -> false |
| 2 | `sessions_test.go`: `TestFormSessionBackoffAfterRefusal` (exact error text `...; login not retried for 60s`, then 30 s later `30s`, login count 1), `TestFormSessionNoBackoffOnOtherError` (rows: transport error and 5xx from a real `Connector.Login` against httptest, `errors.New("secret resolve failed")`; second call logs in), `TestFormSessionBackoffExpires` (+59.5 s still backs off with `1s`; exactly +60 s logs in; a row where the clock advances 10 s inside the refusing login func, so `until` = login end + 60 s; white-box `entries[k].refused == nil` after a success after expiry and after a non-refusal error after expiry), `TestFormSessionBackoffPerKey`, `TestFormSessionBackoffConcurrent` (fresh key, refusing login func gated like `TestFormSessionConcurrentCalls`: the wait hook counts 3 arrivals before the login returns; 1 login, all 3 errors `errors.Is(err, ErrLoginRefused)`) |
| 3 | `TestFormSessionClearDuringRefusedLogin`: login func blocks on a channel (the wait hook runs before `e.mu`, so the gate is in the login func), `Clear()`, release with a refusal; the blocked caller gets the refusal, the next call logs in |
| 4 | `form_session_test.go`: `TestFormSessionLoginBackoff` (login always 401, three calls, `fsWantCounts(1,0)`, calls 2 and 3 contain `login not retried for`), `TestFormSessionReloginRefusedBackoff` (`onLogin` ok on n==1 and 401 after, `onData` always 401; next call returns the backoff error, logins stay 2) |
| 5 | `TestFormSessionBackoffResetByReload`, `TestFormSessionBackoffResetBySecretWrite` (rows set, delete): refused call, admin call, next call logs in again (login count 2) |
| 6 | `go test ./internal/connectors/formsession/ ./internal/server/` |

The existing case "A failed login is not cached" in `TestFormSessionInvalidateKeepsNewerCookie` uses a non-sentinel
error and stays valid.

## Not changed

- No config field, no `/capabilities` or `/help` change, no audit field, no new log line (the refusal text reaches
  the agent and the existing upstream log; logging is a separate todo if wanted).
- `NeedsLogin` (data-request signal) and the re-login retry logic.
- `.ai/CONNECTORS.md` Sessions line gains one sentence on the backoff (doc only).
