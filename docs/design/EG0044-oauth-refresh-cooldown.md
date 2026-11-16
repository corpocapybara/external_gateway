# EG0044 design: OAuth refresh cooldown after a still-rejected retry (email + http-rest)

## Decisions in brief

- D1 (owner 2026-11-16): the window is a per-env config field `oauth.refresh_cooldown`, a `*time.Duration` decoded
  by yaml.v3 from a Go duration string; nil = 5 min, `0s` = no cooldown, negative = `Config.Validate` error. See D1.
- D2: a new type `oauthCooldown` in `internal/server/oauth_cooldown.go` (own mutex, `until` map, `gen` counter,
  injectable `now`), nil-safe like `credCache`; `Server.oauthCooldown`, built in `New`. See D2.
- D3: key = workspace + NUL + `env.SecretRef`. See D3.
- D4: `refreshOAuthToken` returns `refreshResult{outcome, gen}`; outcomes `refreshRetry` (refreshed or
  skipped-changed) and `refreshCooling`, `refreshFailed` (no retry). `gen` is read right after taking
  `oauthRefreshMu`. Order under the lock: EG0045 stored-token check, cooldown check, token endpoint. See D4.
- D5: cooldown starts in `refreshOAuthToken` on every failure after the cooldown check (resolve, request,
  non-200, decode, empty token, store), and in the caller when the retry is still rejected (`withEmail`: retry error
  wraps `ErrAuthRejected`; http-rest: retry status 401), with the `gen` from the result. See D5.
- D6: `start(key, gen, window)` is dropped when `gen` is not current or `window <= 0`; `clear()` empties the map and
  bumps `gen`, called beside every `s.creds.clear()` in `handleAdmin` (secret set, delete, reload). See D6.
- D7: errors: email cooling = `fmt.Errorf("%w: refresh skipped (cooldown)", err)`; failed keeps
  `"%w: OAuth token refresh failed"`; http-rest passes the upstream 401 through on cooling and failed. See D7.
- D8: docs/HOTMAIL.md documents the field and the outside-write trade-off. See D8.

## Acceptance

Restated from todo EG0044 (the todo is authoritative):

1. Cooldown key workspace + env access-token secret ref; `refreshOAuthToken` returns an outcome (refreshed or
   skipped-changed: retry; cooling or failed: no retry) carrying the generation read when the refresh began;
   order under `oauthRefreshMu`: stored-token check, cooldown check, token endpoint; cooldown starts on a failed
   refresh (inside) and on a still-rejected retry (caller, passing the generation back); own mutex and generation.
2. During the window (env `oauth.refresh_cooldown`, absent 5 min, `0s` none, negative = Validate error naming
   workspace, site, env) a rejected call makes no token-endpoint call; email error wraps `ErrAuthRejected` and
   contains "refresh skipped (cooldown)"; http-rest passes the 401 through; after the window it refreshes again; a
   retry that succeeds starts no cooldown.
3. Secret set, secret delete and reload clear the cooldown and bump the generation; a stale-generation start is
   dropped.
4. Server tests in `internal/server/oauth_cooldown_test.go` (list in the todo).
5. Config tests `TestOAuthRefreshCooldownYAML`, `TestValidateRejectsNegativeRefreshCooldown`.
6. docs/HOTMAIL.md documents the field and the trade-off; `go test ./internal/server/ ./internal/config/` passes.

## Anchors

- `internal/config/config.go`: `OAuthRefreshConfig`, `Config.Validate`
- `internal/server/server.go`: `Server`, `New`, `refreshOAuthToken`, `oauthRefreshMu`, `handleAdmin`,
  `s.creds.clear()`, the http-rest 401 branch (`received 401, attempting OAuth token refresh`)
- `internal/server/email_auth.go`: `withEmail`
- `internal/server/credential_status.go`: `credCache` (generation and clock pattern copied)
- `internal/server/oauth_refresh_test.go`: `apiHarness`, `newAPIUpstream`, `emailFake` (reused by the new tests)
- `internal/server/email_auth_test.go`: `newTokenEndpoint`, `hotmailHarness`
- `docs/HOTMAIL.md`: the `oauth:` config example

## Decisions

**D1 Config field.** `OAuthRefreshConfig` gains `RefreshCooldown *time.Duration \`yaml:"refresh_cooldown"\``.
yaml.v3 decodes a duration string (`"5m"`) into a `time.Duration` (and through a pointer); a bare integer is
nanoseconds (yaml.v3 behaviour, documented as "use a duration string"). A pointer tells absent (nil: default 5 min,
`defaultOAuthRefreshCooldown` in package server) from `0s` (no cooldown, today's behaviour). `Config.Validate` walks
workspaces → sites → environments and returns `workspace %s site %s env %s: oauth.refresh_cooldown %s is negative`.
Per env, not global: the token endpoint and its rate limits belong to the env's OAuth block; the owner asked for a
config field without a scope, and the existing `oauth` block is the only place the other refresh settings live.
The window is read from the env passed to the call when the cooldown starts (`cooldownWindow(env)`), so a reload
that changes it applies from the next start (reload also clears, D6).

**D2 Type.** `oauthCooldown{mu sync.Mutex; until map[string]time.Time; gen uint64; now func() time.Time}` with
`newOAuthCooldown()`, `generation() uint64`, `cooling(key) bool` (until after now; an expired entry is deleted),
`start(key string, gen uint64, window time.Duration)`, `clear()`. All methods are nil-safe (tests build `&Server{}`
without `New`; a nil cooldown never cools and ignores start/clear). Its own mutex, never held while calling out.

**D3 Key.** `workspace + "\x00" + env.SecretRef`: stable across reload (no pointer or env-name identity), and
two envs sharing one access-token secret share one token endpoint state, which is correct (same refresh token).

**D4 Outcome.** `type refreshOutcome int` with `refreshRetry`, `refreshCooling`, `refreshFailed`;
`type refreshResult struct{ outcome refreshOutcome; gen uint64 }`. Under `oauthRefreshMu`: read `gen`; EG0045
stored-token check (changed: `refreshRetry`, no cooldown check, so a fixed token beats the cooldown); then
`cooling(key)`: `refreshCooling`; then the token endpoint. Refreshed and skipped-changed are one outcome because
both callers do the same thing (retry with the stored token); a log line already tells them apart.

**D5 Where it starts.** Inside `refreshOAuthToken`, every `return false` path after the cooldown check becomes
`s.oauthCooldown.start(key, gen, cooldownWindow(env)); return refreshResult{refreshFailed, gen}`: refresh-token
resolve error, request build error, transport error, non-200, decode error, empty `access_token`, failed store of
the access token. (The todo names non-200 and transport; the others are the same "refresh cannot succeed" state and
would otherwise hit the endpoint or the store on every call.) A failed store of a rotated refresh token stays a
logged error after a successful refresh (unchanged). In the callers: `withEmail` on `refreshRetry` runs the call
again and, when that error wraps `email.ErrAuthRejected`, starts the cooldown with `res.gen`; the http-rest branch
does the same when the retry's status is 401. A retry that succeeds, fails another way, or is never sent (the
stored secret fails to resolve) starts nothing.

**D6 Clear and generation.** `clear()` replaces the map and bumps `gen`. `handleAdmin` calls
`s.oauthCooldown.clear()` next to each of the three `s.creds.clear()` calls. `start` drops the entry when
`gen != c.gen`, so a refresh or retry that began before a clear (and finished after it) leaves nothing; and when
`window <= 0`. Because `gen` is read after taking `oauthRefreshMu`, a call that waited for the lock across a clear
uses the new generation and sees the new token through the EG0045 check.

**D7 Errors.** Email: `refreshCooling` → `fmt.Errorf("%w: refresh skipped (cooldown)", err)`, `refreshFailed` →
unchanged `"%w: OAuth token refresh failed"`; both wrap the original `ErrAuthRejected`. http-rest: cooling and
failed fall through to the existing response path with the first 401, exactly as a failed refresh does today. Each
cooling skip logs one info line with the secret ref (no token).

**D8 Docs.** docs/HOTMAIL.md: the `oauth:` example gains a commented `refresh_cooldown: 5m`, and a short paragraph:
default 5 min, `0s` turns it off, negative refuses to load; a credential-store write outside `/admin/secrets` (a
manual `cmdkey`, a direct store edit) takes effect only after the window or an `/admin/reload`.

## Test plan

| Acceptance | Test |
|---|---|
| 1 key, outcome, order | `TestOAuthCooldownPerKey`, `TestOAuthCooldownChangedTokenBeatsCooldown`, `TestOAuthCooldownAfterSkippedRefreshRejected` |
| 2 window, errors | `TestOAuthCooldownEmail`, `TestOAuthCooldownHTTPRest`, `TestOAuthCooldownExpires`, `TestOAuthCooldownFailedRefresh`, `TestOAuthCooldownNoneAfterSuccess`, `TestOAuthCooldownWindow` |
| 3 clear, generation | `TestOAuthCooldownReset` (rows: set, delete, reload), `TestOAuthCooldownClearDuringRefresh` (rows: failing refresh across a clear; successful refresh whose retry is rejected after a clear; upstream/token endpoint block on a channel, no sleeps) |
| 4 | all of the above in `internal/server/oauth_cooldown_test.go`, clock via `s.oauthCooldown.now`, counting token endpoint |
| 5 | `TestOAuthRefreshCooldownYAML`, `TestValidateRejectsNegativeRefreshCooldown` in `internal/config` |
| 6 | `go test ./internal/server/ ./internal/config/` |

Each cooldown test fails on today's code: without the cooldown the second rejected call makes a second token call.

## Not changed

- EG0045's stored-token check and the serialisation under `oauthRefreshMu`.
- The token-check cache (`credCache`) and form-session cookies.
- Response shaping, audit records, `/capabilities` output.
- Config checksum logic (the new field is part of the file it hashes already).
