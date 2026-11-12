# T0006 design: credential expiry and rejection in `/capabilities`

Todo: `todo/T0006.md` (acceptance there is the contract; this note says how).

## Decisions in brief
- Agents learn of an expiring or dead token from `GET /capabilities` only; `gitlab.whoami` is unchanged (§Decision).
- New top-level field `credentials`: one entry per site/env (with a `secret_ref`) of the agent's granted, enabled
  sites whose `tokencheck.DetectKind` is GitLab or GitHub; detection limits as in §Which credentials.
- Each entry: `site`, `env`, `state` (`ok`, `rejected`, `unreachable`, `missing`), `expires_at`, `days_left`,
  and `warning` when the owner must act (§Entry).
- `rejected` only for an upstream 401/403; any other tokencheck "rejected" (404, 429, ...) is `unreachable` (§Entry).
- `missing` = the gateway cannot get a usable secret (not set, empty, unreadable); warning tells the agent to ask
  the owner to check it (§Entry).
- Warn at `days_left <= 7` or expired (owner request in the todo); the tray app keeps its own 14-day owner warning.
- Cache per workspace/site/env of the tokencheck result only: 1 h after `ok`, 5 min otherwise; `days_left` and
  warnings are computed per response. Cleared after a successful `/admin/secrets` write and on `/admin/reload`
  (§Cache and latency).
- Cold checks run in parallel, each capped by a 5 s context (§Cache and latency).
- No secret, identity, token name or scopes reach the agent: the entry is built from `State`, `HTTPStatus` and
  `ExpiresAt` only (§Security).
- Test seams on `Server`: `credNow` and `credTimeout` (§Tests).
- Owner decision 2026-11-12: build the agent-side warning although the tray app already warns the owner.

## Problem (verified)
A GitLab PAT that expires fails with `invalid_token` on every call; agents only find out from a failed call and
switch to the browser (7 such answers on 10-15/16). `tokencheck.checkGitLab` (`internal/tokencheck/tokencheck.go`
~281) already reads `expires_at` from `GET /personal_access_tokens/self`, and `checkGitHub` (~305) reads the
`github-authentication-token-expiration` header, but only the admin secrets status and the tray app use them.
`handleCapabilities` (`internal/server/server.go` ~193) reports operations and features, nothing about credentials.

## Decision
`handleCapabilities` adds `"credentials": [...]` (always present, `[]` when none) built by a new
`(*Server).credentialStatus(workspace string, ws *config.Workspace, ops map[string]*policy.Operation)` in a new
file `internal/server/credential_status.go`.

### Which credentials
From the agent's granted ops (`grantedOperations`, which already drops disabled sites): the distinct sites they
use; for each site every environment with a non-empty `secret_ref` (an op picks its env per call, so every env is
reachable); kept when `tokencheck.DetectKind(site, env)` is `KindGitLab` or `KindGitHub`. Sorted by site, then env.
Limits of `DetectKind` (accepted, stated in AGENTS.md): GitLab only when the base URL contains `gitlab`, GitHub only
for `api.github.com` (GitHub Enterprise is not listed); OAuth, Slack and Trello auth win over the URL. No change to
`DetectKind`.

### Entry
```json
{"site":"gitlab","env":"prod","state":"ok","expires_at":"2026-11-15","days_left":3,
 "warning":"gitlab/prod token expires in 3 days (2026-11-15): ask the owner to renew it; calls will fail after that"}
```
`entryFor(site, env string, res tokencheck.Result, resolveErr error, now time.Time) credentialEntry`, a pure
function:
- `resolveErr != nil`, or tokencheck `unchecked` (empty value) -> `missing`, warning "gateway has no usable stored
  token for <site>/<env> (not set, empty or unreadable): ask the owner to check it". A store outage reads the same:
  the owner must look either way, and the agent's calls on that site fail meanwhile.
- `rejected` with `HTTPStatus` 401 or 403 -> `rejected`, warning "<site>/<env> token rejected by upstream (HTTP n):
  ask the owner to renew it; calls on this site fail".
- `rejected` with any other status, or `unreachable` -> `unreachable`, no warning.
- `ok` -> `ok`; `expires_at` set when the upstream gave one. `days_left` = calendar days from `now`'s local date to
  the date in the first 10 characters of `expires_at` (`time.ParseInLocation("2006-01-02", s[:10], time.Local)`;
  GitLab sends `YYYY-MM-DD`, GitHub `YYYY-MM-DD HH:MM:SS UTC`). Unparseable: `expires_at` kept, no `days_left`,
  no warning. `days_left <= 7` -> warning (text above; `0` says "expires today", `< 0` says "expired on <date>").
  No date (never expires, or `/personal_access_tokens/self` failed or timed out after `/user` passed) -> no warning.

### Cache and latency
`Server.credCache`: `map[string]credCached` keyed `workspace/site/env` under a `sync.Mutex`; `credCached` holds the
`tokencheck.Result`, the resolve error and the check time. Fresh for 1 h after `ok`, 5 min otherwise, judged by
`s.credNow()`. Entries are rebuilt from the cached result on every response, so `days_left` never goes stale.
Misses are checked in parallel (one goroutine each, `context.WithTimeout(s.credTimeout)`, default 5 s; `do()`'s 20 s
client timeout is longer, so the context is the cap). `checkGitLab` makes two sequential calls; if the second runs
out of time the entry is `ok` with no date (accepted). Two concurrent misses for one key may both check
(no singleflight): harmless. `clearCredCache()` runs after a successful Set or Delete in `handleAdmin` `secrets`
(`server.go` ~2154-2200) and after a successful `/admin/reload`. egw-admin.exe and the tray app both write through
`/admin/secrets`; a secret changed directly in Credential Manager shows after the TTL.

### Security
The entry is built from `State`, `HTTPStatus` and `ExpiresAt` only; tokencheck's `Detail` (holds the token name),
`Identity` and `Scopes` never reach it. The secret is resolved with
`secrets.ResolveWithWorkspaceCheck(refWS, name, workspace)` like an op call, so cross-tenant isolation holds. Only
sites of ops the agent is granted are listed.

## Tests (`internal/server/credential_status_test.go`)
Harness: a mutable fake store (`map` + `sync.Mutex`, Set/Delete real) installed with `secrets.SetStore`, a unique
workspace name per test and `secrets.GetResolver().FlushCache()` at start (the resolver's own 30 s cache is keyed
ws/name and outlives `SetStore`). An `httptest` GitLab whose site `base_url` is `<srv>/gitlab/api/v4` (so
`DetectKind` sees `gitlab`), serving `/gitlab/api/v4/user` and `/gitlab/api/v4/personal_access_tokens/self` with
per-path hit counters. `credNow` fixed at a local noon; `credTimeout` 200 ms. Config built per test with roles,
agents, `AdminTokenSHA` (as in `admin_hardening_test.go`), driven through `ServeHTTP`.
1. `TestCredentialsExpiryWarning`: `expires_at` today (0, warning "expires today"), yesterday (-1, "expired on"),
   +7 (warning), +8 (no warning), null (ok, no date, no warning); `/self` 404 after `/user` 200 (ok, no date).
2. `TestCredentialsRejectedMissingUnreachable`: 401 `invalid_token` -> rejected + warning; 429 -> unreachable, no
   warning; secret absent -> missing + warning; closed server -> unreachable; handler slower than `credTimeout` ->
   unreachable.
3. `TestCredentialsScope`: a site only a non-granted op uses, a disabled site and a Jira site are not listed; two
   envs on one site (prod ok, dev rejected) both listed, sorted; an env without `secret_ref` is not listed.
4. `TestCredentialsCache`: a second `/capabilities` makes no hit on either path; at `now` + 61 min an `ok` entry
   is re-checked, at + 6 min a `rejected` one is re-checked and an `ok` one is not; after an `/admin/secrets` set
   (admin token) and after a delete it re-checks (delete -> missing).
5. `TestCredentialsNoLeak`: distinctive token, username, token name and scopes in the replies; none in the body.
6. `TestCredentialsWorkspaces`: one agent granted two workspaces with the same site/env names, different states:
   each `/capabilities?workspace=` shows its own.
7. `TestEntryForGitHubDate`: `entryFor` with a GitHub-style `ExpiresAt` ("2026-11-15 12:00:00 UTC") gives the
   right `days_left` (GitHub's host cannot be faked through `DetectKind`, so this is a unit test).
8. Parallel `/capabilities` calls (cold) under `go test -race` on the package: no race.
No real sleeps beyond the 200 ms timeout case.

## Docs
AGENTS.md "Capabilities endpoint" and the usage text (`server.go` ~1796) mention `credentials` and the limits.

## Out of scope
`gitlab.whoami` output, Atlassian tokens (no expiry in the API), GitHub Enterprise detection, the tray app's warning.
