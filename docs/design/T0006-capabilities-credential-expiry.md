# T0006 design: credential expiry and rejection in `/capabilities`

Todo: `todo/T0006.md` (acceptance there is the contract; this note says how).

## Decisions in brief
- Agents learn of an expiring or dead token from `GET /capabilities` only; `gitlab.whoami` is unchanged (§Decision).
- New top-level field `credentials`: one entry per site/env the agent's granted ops use whose kind reports an
  expiry (GitLab, GitHub); other kinds are not listed (§Which credentials).
- Each entry: `site`, `env`, `state` (`ok`, `rejected`, `unreachable`, `missing`), `expires_at`, `days_left`,
  and `warning` when it needs the owner (§Entry).
- Warn at 7 days or fewer (owner request in the todo); the tray app keeps its own 14-day owner warning (§Entry).
- Results are cached per workspace/site/env: 1 h after `ok`, 5 min otherwise; a cold check has a 5 s timeout and
  the entries run in parallel (§Cache and latency).
- No secret, identity, token name or scopes reach the agent (§Security).
- Owner decision 2026-11-12: build the agent-side warning although the tray app already warns the owner.

## Problem (verified)
A GitLab PAT that expires fails with `invalid_token` on every call; agents only find out from a failed call and
switch to the browser (7 such answers on 10-15/16). `tokencheck.checkGitLab` (`internal/tokencheck/tokencheck.go`
~281) already reads `expires_at` from `GET /personal_access_tokens/self`, and `checkGitHub` (~305) reads the
`github-authentication-token-expiration` header, but only the admin secrets status and the tray app use them.
`handleCapabilities` (`internal/server/server.go` ~193) reports operations and features, nothing about credentials.

## Decision
`handleCapabilities` adds `"credentials": [...]` (always present, `[]` when none) built by a new
`credentialStatus(workspace, ws, ops)` in a new file `internal/server/credential_status.go`.

### Which credentials
From the agent's granted ops (`grantedOperations`, which already drops disabled sites): the distinct sites they
use; for each site every environment with a `secret_ref`; kept when `tokencheck.DetectKind(site, env)` is
`KindGitLab` or `KindGitHub` (`expiryKinds`, one map in the new file). Sorted by site, then env.

### Entry
```json
{"site":"gitlab","env":"prod","state":"ok","expires_at":"2026-11-15","days_left":3,
 "warning":"gitlab/prod token expires in 3 days (2026-11-15): ask the owner to renew it; calls will fail after that"}
```
- `state`: tokencheck `ok`, `rejected`, `unreachable` as is; `missing` when the secret cannot be resolved (not
  stored or empty); tokencheck `unchecked` (no value) also maps to `missing`.
- `expires_at` / `days_left`: only when `ok` and the upstream gave a date. `days_left` counts whole days from the
  gateway's local date to `expires_at` (GitLab date `YYYY-MM-DD`; GitHub's header is parsed by its date part).
  Unparseable date: `expires_at` kept, `days_left` and the expiry warning omitted.
- `warning` when: `rejected` ("token rejected by upstream: ask the owner to renew it; calls on this site fail"),
  `missing` ("no token stored: ask the owner to set it"), `ok` with `days_left <= 7` (text above; `days_left < 0`
  says "expired on"). No warning for `unreachable` (network, not the token) or a later expiry.

### Cache and latency
A `credCache` on `Server`: `map[string]credEntry` keyed `ws/site/env` under a mutex, entry = result + time.
Fresh for 1 h after `ok`, 5 min after anything else. Misses are checked in parallel (one goroutine each,
`context.WithTimeout` 5 s), so a cold `/capabilities` waits at most ~5 s plus the upstream's two calls; warm calls
add nothing. Two concurrent misses for one key may both check (no singleflight): harmless, rare.
Config reload: keys do not include the secret, so a renewed token shows after the TTL; a set or delete through
`/admin/secrets` (`handleAdmin`, `server.go` ~2121) clears the whole cache so a renewal shows at once.

### Security
The entry carries only site, env, state, date, days and a fixed warning text; tokencheck's `Detail`, `Identity`
and `Scopes` are dropped. The secret is resolved with `secrets.ResolveWithWorkspaceCheck(wsName, name, workspace)`
like an op call, so cross-tenant isolation holds. Only sites of ops the agent is granted are listed, so it learns
nothing about other sites.

## Tests (`internal/server/credential_status_test.go`)
Fake secret store (`secrets.SetStore`, as in `jenkins_crumb_test.go`) plus an `httptest` GitLab that serves
`/user` and `/personal_access_tokens/self`, driven through `ServeHTTP` on `/capabilities`:
1. expires in 3 days -> `state ok`, `days_left 3`, warning; expires in 30 days -> no warning.
2. 401 `invalid_token` -> `state rejected`, warning; secret absent -> `missing`, warning.
3. A site of a non-granted op and a disabled site are not listed; a non-expiry kind (Jira) is not listed.
4. Cache: a second `/capabilities` within the TTL makes no upstream call (request counter); after a
   `/admin/secrets` set it checks again.
5. The response has no token, username or scopes (the fake values are absent from the body).
No real clock sleeps: the cache takes a `now func() time.Time` the test sets.

## Out of scope
`gitlab.whoami` output, Atlassian tokens (no expiry in the API), the tray app's warning.
