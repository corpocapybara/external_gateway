# EG0066 design: Runtime metrics: per-op request count and latency, process CPU and RSS (review R13)

## Decisions in brief

- D1 (owner 2026-11-20): metrics are exposed only as admin-token `GET /admin/metrics` JSON; no log line, no
  agent-token endpoint. See D1.
- D2: new package `internal/metrics`: `Collector` (one mutex, map keyed by workspace+op, `deniedUnattributed`
  counter, `startedAt`, injectable `now`), nil-safe methods `Unattributed`, `Denied`, `Rejected`, `Executed`,
  `Snapshot` (a nil collector gives an empty snapshot). See D2.
- D3: latency histogram: fixed upper bounds 0,1,2,5,10,20,50,100,200,500,1000,2000,5000,10000,30000,60000,120000 ms
  plus overflow; a sample goes to the first bound >= it; percentile p = min(bound of the bucket holding rank
  r = (n*p+99)/100 integer, at least 1, max); overflow reports max; empty = 0; negative = 0. See D3.
- D4: process stats in `process_windows.go` (GetProcessTimes, lazy kernel32 `K32GetProcessMemoryInfo`, Toolhelp32
  Process32 `Threads` of the own pid) and `process_other.go` (those fields nil, omitted). See D4.
- D5: wiring: `Server.metrics` built in `New` (survives reload, not in `runtimeState`). Counting only in
  `handleWorkspaceOp` (auth failure when not dry run) and `executeOp`: pre-resolution denials call `Unattributed`;
  after resolution local closures `deny` and `reject` wrap `writeDeny` / the 400 and 413 `http.Error`; one
  `Executed` call next to `LogAllow`. `writeDeny`, `writeUnknownOp`, `handleDryRun` are unchanged. See D5.
- D6: `handleAdmin` `case "metrics"`: `authenticateAdmin` (401) first, then GET (405), then the snapshot JSON; the
  index text names `GET /admin/metrics`. See D6.
- D7: JSON shape as acceptance A2, `latency_ms.count` = samples; `ops` sorted by workspace then op. See D7.
- D8: AGENTS.md Admin API row and `.ai/DEVELOPMENT.md` endpoint list. See D8.
- D9: server tests use `newCredHarness` through `ServeHTTP` with a local no-auth `httptest` upstream. See D9.

## Acceptance

Restated from todo EG0066 (the todo is authoritative): A1 endpoint and auth order; A2 JSON shape; A3 counting rule
(where counted, dry run never, unattributed set, denied / rejected / executed exactly one, errors rule,
requests = denied + rejected + latency_ms.count); A4 `TestAdminMetricsCountsOps` cases; A5 reload survival and
bounded entries; A6 histogram buckets and percentile rule; A7 concurrency with mid-run invariant, exact totals, nil collector and `-race`; A8 process
stats per platform; A9 AGENTS.md row and the e2e run after deploy.

## Anchors

- `internal/server/server.go`: `Server`, `New`, `runtimeState`, `handleWorkspaceOp`, `executeOp`, `writeDeny`,
  `writeUnknownOp`, `handleDryRun`, `handleAdmin` (index text, `case "reload"`), `authenticateAdmin`,
  `s.auditLog.LogAllow`, `policy.ConstraintDenied` branch after `dispatchConnector`
- `internal/server/dispatch.go`: `dispatchConnector` (only caller: `executeOp`)
- `internal/audit/logger.go`: `Logger.LogAllow` (`latencyMS`)
- `internal/server/credential_status_test.go`: func `newCredHarness`, type `credHarness` (config and ops on disk,
  `adminTok`, `credAgentTok`, `audit.Init` in its dir)
- `internal/config/config.go`: `Load` (role operations must exist in the workspace: the reload test drops the op
  from the role too)
- `.ai/DEVELOPMENT.md`: the admin endpoint list (`POST /admin/reload` line)
- `internal/connectors/localservice/adapters_windows.go`, `adapters_other.go` (platform split pattern)
- `golang.org/x/sys/windows` v0.50.0: `GetProcessTimes`, `GetCurrentProcess`, `CreateToolhelp32Snapshot`,
  `Process32First`, `Process32Next`, `ProcessEntry32`, `NewLazySystemDLL`
- `AGENTS.md`: "Admin API" table

## Decisions

**D1 Exposure (owner 2026-11-20).** Admin-token `GET /admin/metrics` JSON. Admin endpoints fail closed when
`admin_token_sha256` is unset (`authenticateAdmin`). A periodic service-log line (needs EG0065) and an agent-token
endpoint were offered and not chosen.

**D2 Collector.** `internal/metrics/metrics.go`:

```go
type Collector struct {
    mu         sync.Mutex
    ops        map[opKey]*opStats   // opKey{workspace, op}
    unattributed uint64
    startedAt  time.Time
    now        func() time.Time     // tests
}
type opStats struct { requests, denied, rejected, errors uint64; lat histogram }
func New() *Collector
func (c *Collector) Unattributed()
func (c *Collector) Denied(workspace, op string)
func (c *Collector) Rejected(workspace, op string)
func (c *Collector) Executed(workspace, op string, latencyMS int64, failed bool)
func (c *Collector) Snapshot() Snapshot
```

Each per-op method adds 1 to `requests` and to its own field under `mu`, creating the entry on first use; `Executed`
adds a latency sample and, when `failed`, 1 to `errors`. One mutex, not atomics or `sync.Map`: a record is a few
increments next to an upstream call of milliseconds, and one lock makes `Snapshot` a consistent cut (a snapshot
never shows `requests` without its outcome). All methods are nil-receiver safe, like `credCache` (`Snapshot` on nil returns an empty snapshot), so a `Server`
literal built without `New` in old tests (env_select, audit_agent_id, jira_link_precheck, admin_hardening) does
not panic. `Snapshot` copies the entries and histograms under `mu`; process stats are read after unlocking. Entries are created only after the workspace and op resolve
(D5), so the map is bounded by the workspaces x ops ever loaded; unknown ids go to `unattributed`. Grant-denied
requests of a valid agent create an entry; that is within the same bound and intended.

**D3 Histogram.** `internal/metrics/histogram.go`: `bounds = []int64{0,1,2,5,10,20,50,100,200,500,1000,2000,5000,
10000,30000,60000,120000}`, `counts [len(bounds)+1]uint64` (last = overflow), `n uint64`, `max int64`. `add(v)`:
`v < 0` becomes 0; the bucket is the first `i` with `v <= bounds[i]`, else overflow; `max = max(max, v)`.
`percentile(p)` for p in percent (50, 95, 99): `n == 0` gives 0; rank `r = (n*p + 99) / 100` in integer math, so
no float rounding (at least 1); walk the counts until the running sum reaches
`r`; the overflow bucket gives `max`, another gives `min(bounds[i], max)`. Integer ms throughout, because the
audit `latency_ms` is `time.Since(start).Milliseconds()`; a sub-millisecond op records 0. Fixed memory per entry
(18 counters); a reported value is at most one bucket above the exact one and never above `max`.

**D4 Process stats.** `Snapshot.Process` is `Process{CPUSeconds *float64; RSSBytes *uint64; Goroutines int;
Threads *uint32}` with `omitempty` on the pointers. `process_windows.go` (`//go:build windows`):
`windows.GetProcessTimes(windows.CurrentProcess(), ...)`, user + kernel `Filetime` in 100 ns units / 1e7;
`K32GetProcessMemoryInfo(windows.CurrentProcess(), &pmc, pmc.cb)` from
`windows.NewLazySystemDLL("kernel32.dll")` with a local `processMemoryCounters` struct laid out as
PROCESS_MEMORY_COUNTERS (`cb uint32`, `PageFaultCount uint32`, then eight `uintptr`; `cb` = `unsafe.Sizeof`),
`WorkingSetSize`, r1 == 0 = failure; threads from a `TH32CS_SNAPPROCESS` snapshot (`defer windows.CloseHandle`),
`ProcessEntry32.Size` set before `Process32First`, the entry whose `ProcessID == windows.GetCurrentProcessId()`,
its `Threads`. A failing call leaves only that field nil (no error to the caller; the endpoint still answers).
`process_other.go` (`//go:build !windows`) fills `Goroutines` only. Goroutines: `runtime.NumGoroutine()`.

**D5 Server wiring.** `Server.metrics *metrics.Collector`, set in `New`; `runtimeState` and the reload case are
untouched, so counters survive `/admin/reload` and a reload-removed op keeps its entry.
- `handleWorkspaceOp`: the authentication branch calls `s.metrics.Unattributed()` unless
  `r.URL.Query().Get("dry_run") == "true"`. The 405 and invalid-path 400 are not counted. Dry runs go to
  `handleDryRun`, which counts nothing.
- `executeOp`: the agent-nil, workspace-nil and unknown-op returns call `s.metrics.Unattributed()` before writing.
  After `agentHasOp` is reached the op is resolved; from there `deny := func(rule, detail string)
  { s.metrics.Denied(workspace, opID); s.writeDeny(w, r, agentID, workspace, opID, rule, detail) }` replaces every
  `s.writeDeny` call (grant, disabled, validation, constraint, scope, env validation, tunnel, post-dispatch
  `ConstraintDenied`), and the 413 and invalid-JSON 400 call `s.metrics.Rejected(workspace, opID)`. Next to
  `LogAllow`: `s.metrics.Executed(workspace, opID, latency, err != nil || upstreamStatus >= 400)`, with the same
  `latency` variable `LogAllow` gets, so the sample equals the audit `latency_ms`. The non-JSON / empty upstream
  body paths after it are executed too. A non-413 body read error is ignored today and falls through; unchanged.
  The agent-nil return is reachable only through a reload race: covered by review, no deterministic test.
- `writeDeny`, `writeUnknownOp`, `handleDryRun` stay unchanged, so dry runs never count.
Every return of `executeOp` after resolution is then exactly one of deny, reject, executed, giving
`requests = denied + rejected + latency samples`. A request in flight during a reload is attributed by its
workspace and op strings, so no stale registry pointer is kept.

**D6 Admin route.** `case "metrics":` `authenticateAdmin` false gives 401 "unauthorized"; method not GET gives 405
"GET required"; else `Content-Type: application/json` and the encoded snapshot. Auth first matches `secrets` and
`diag` (the method is not revealed to an unauthenticated caller). The index string gains `GET /admin/metrics`.

**D7 JSON.**

```json
{"started_at":"2026-11-20T19:00:00Z","uptime_seconds":12.5,"denied_unattributed":3,
 "process":{"cpu_seconds":0.42,"rss_bytes":31457280,"goroutines":9,"threads":14},
 "ops":[{"workspace":"acme","op":"jira.issue","requests":5,"denied":1,"rejected":0,"errors":1,
         "latency_ms":{"count":4,"p50":200,"p95":431,"p99":431,"max":431}}]}
```

`started_at` is the collector creation time (server start) in RFC 3339 UTC; `uptime_seconds` = now - startedAt.
`ops` sorted by workspace, then op; `latency_ms.count` is the number of samples (so `requests = denied + rejected +
count` is visible to the caller); an entry with no samples has all latency fields 0. p95 and p99 above are
capped by max (D3).

**D8 Docs.** AGENTS.md "Admin API" gets `| /admin/metrics | GET | Per-op request, deny, reject, error counts and
latency percentiles since start, process CPU, RSS, goroutines, threads |`; `.ai/DEVELOPMENT.md` lists
`GET /admin/metrics` beside `POST /admin/reload`.

**D9 Server test harness.** `newEnvFixture` calls `executeOp` directly and skips `authenticate`, the `dry_run`
query, `MaxBytesReader` and `/admin`, so the server tests use `newCredHarness` (built with `New`) and
`ServeHTTP`. Sites point at a local `httptest` upstream with no auth whose status and body the test switches
(200 JSON, 200 non-JSON, 204 empty, 500); a closed server gives the connector error. The ops file has one extra
op that is not in the role (grant deny) and one op with a required param (validation deny). The 413 body is
streamed (`io.MultiReader` of a small JSON prefix and an `io.LimitReader` of repeated bytes), not allocated.
The allowed call's audit line is read from the harness dir to compare `latency_ms` with the metric's max.

## Test plan

- A1: `TestAdminMetricsAuth` (internal/server/metrics_test.go): no token, wrong token, admin_token_sha256 unset,
  POST with no token -> 401; admin token with POST -> 405; admin token GET -> 200 JSON; `/admin/` index contains `GET /admin/metrics`.
- A2: `TestSnapshotSorted` (internal/metrics/metrics_test.go): entries recorded out of order come back sorted, with
  every field; the JSON decode of the endpoint in `TestAdminMetricsCountsOps` checks the field names.
- A3, A4: `TestAdminMetricsCountsOps` (internal/server/metrics_test.go) through `ServeHTTP` with a fake upstream:
  allowed op (count 1, max = audit latency_ms), 200 non-JSON, 204 empty (errors 0), grant deny, validation deny, invalid-JSON 400, a body of maxOpBodyBytes+1 (413, rejected), connector
  error, upstream 500, unknown op x3 and unknown workspace (unattributed, no entry), bad token (unattributed +1),
  bad token with dry_run=true (no change), dry run of an allowed and a grant-denied op (no change);
  exact counts and `requests == denied + rejected + latency_ms.count` per entry. Harness: D9.
- A5: `TestAdminMetricsSurvivesReload`: count an op, rewrite config (role) and ops without it, POST /admin/reload
  (200), the entry and counts are unchanged, a call to the removed op adds 1 to denied_unattributed only.
- A6: `TestHistogramPercentiles` (internal/metrics/histogram_test.go): empty -> 0s; one 7 ms sample -> p50=p99=7,
  max 7; a 5 ms sample (bound) -> 5; a 0 ms sample -> 0; a 200000 ms sample -> 200000 (overflow = max); -3 -> 0; 100
  samples 1..100 -> p50=50, p95=100, p99=100, max 100; {3,3,3} -> p50 3 (capped by max).
- A7: `TestRecordConcurrent`: 8 goroutines x 1000 `Executed`/`Denied`/`Rejected` while a goroutine snapshots;
  every mid-run snapshot has `requests == denied + rejected + count`; exact final totals. `TestNilCollector`: a nil
  collector takes every call and returns an empty snapshot. `go test -race ./internal/metrics/ ./internal/server/`.
- A8: `TestProcessStats` (internal/metrics/process_windows_test.go): rss_bytes > 0, threads >= 1, goroutines >= 1,
  cpu_seconds >= 0 and not decreasing over two calls, threads non-nil; `GOOS=linux go vet ./internal/metrics/ ./internal/server/`.
- A9: e2e after the elevated `deploy/update.ps1`: one live op call, then `GET /admin/metrics` shows it.

## Not changed

Audit log format and `LogAllow`/`LogDeny`; `handleDryRun`; `/capabilities`, `/help`, `/healthz`; config schema (no
new field); the desktop app and egwctl (no metrics view); EG0065 (service log, audit retention).
