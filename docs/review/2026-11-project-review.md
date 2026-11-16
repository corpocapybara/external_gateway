# Project review (rubric v1): external_gateway

```
repo: apps/external_gateway   variant: full   rubric: v1
findings: 2 blocking, 14 note   todos: EG0052, EG0053, EG0054, EG0055, EG0056, EG0057, EG0048
suite: 29.0 s (full, cap 15 min)   docker: --cpus 0.5 --memory 512m
affected-tests: no   speed-tags: no   metrics: partial
```

| id | tag | area | todo |
|----|-----|------|------|
| R1 | note | 1 Structure | EG0053 |
| R2 | note | 1 Structure | EG0053 |
| R3 | note | 2 Tests | EG0054 |
| R4 | note | 2 Tests | EG0054 |
| R5 | note | 2 Tests | EG0054 |
| R6 | note | 2 Tests | EG0054 |
| R7 | note | 3 Bugs and drift | EG0055 |
| R8 | note | 3 Bugs and drift | EG0055 |
| R9 | note | 3 Bugs and drift | EG0055 |
| R10 | note | 3 Bugs and drift | EG0055 |
| R11 | note | 4 Technology | EG0056 |
| R12 | note | 5 Performance (tests) | EG0054 |
| R13 | note | 6 Metrics | EG0057 |
| R14 | note | 6 Metrics | EG0057 |
| R15 | blocking | 7 Security | EG0052 (new) |
| R16 | blocking | 7 Security | EG0048 (existing) |

Reviewed at master `c8f32fe` (2026-11-16), in the worktree `.worktrees/EG0035`. Measurement scripts live in the
untracked `.scratch/review/` of that worktree: `sample.py` (process-tree sampler, psutil, 0.25 s interval),
`slow.py` (go test -json summary), `loadgen/` (fake upstream plus load client), `load/ladder.sh` (Docker ladder),
`secrets_scan.py`, `names_check.py`, `hashcheck.py`. Workspaces are named by placeholder only (`acme`, `<ws>`).

## 1. Structure

**Module map** (from `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./...`, module-internal edges only; no cycles):
- `cmd/egw`: the service (kardianos/service); wires config, policy and server. `cmd/egw-admin`, `cmd/egwctl`: CLIs with
  no internal imports. `cmd/egw-desktop`: Windows tray app (`ExternalGateway.exe`), imports config, policy,
  tokencheck. `cmd/tools/*`: debug tools (imports secrets), `gen-token`, `leakcheck` (pre-commit names guard).
- `internal/server`: the hub, HTTP routes, auth, op execution, admin API; imports audit, config, connectors and its
  email/formsession/httprest/postgres packages, policy, redact, secrets, tokencheck, workcal (12 internal imports).
- `internal/policy` -> config; `internal/audit` -> secrets; `internal/redact` -> secrets; `internal/tokencheck` ->
  config, email, postgres; `internal/connectors/httprest` -> connectors, secrets; `localservice` -> connectors.
  `config`, `scheduler`, `workcal`, `formsession`, `email`, `postgres` import nothing internal.
- Non-Go parts: `deploy/*.ps1` (provision, setup, update), `tools/slack-provision/`, `config.example/`, `docs/`,
  `todo/`, `handovers/`.

**Extensibility.** A new http-rest operation is YAML only (`config.example/operations*.yaml` plus the live ops file).
A new connector touches: the new package, `internal/server/server.go` (the if/else dispatch at
`internal/server/server.go:579-594` plus a new `executeX` method), `internal/policy/connectors.go:7` (`knownConnectors`),
`.ai/CONNECTORS.md`, the README file tree and `config.example`.

**Co-change** (`git log --name-only -n 200`, todo/ and handovers/ excluded, script `.scratch/review/co.py`): the
strongest pair changed together 3 times (README.md with `.ai/PITFALLS.md`); the rest are 2 each and are README/docs
pairs (one concern: README acts as an index) or `cmd/tools/leakcheck/install.go` with `leakcheck.go` (one part split
in two files). No code tangle shows. Buckets: every `internal/*` and `cmd/*` directory is owned exactly once
(`todo/_config.yaml:4-10`).

**Duplication.** `git grep -h -o -E '^func [a-zA-Z_]+\(' -- '*.go' ':!*_test.go' | sort | uniq -c`: only
`noRedirect` (`internal/connectors/formsession/connector.go:33`, `internal/connectors/httprest/connector.go:30`) and
`contains` (`internal/connectors/email/connector.go:503`, `cmd/tools/leakcheck/leakcheck.go:142`) repeat; both
are a few lines long. Nothing is copied from `C:/dev/shared` (`git ls-files` has no file from there).

**Oversized files** (`wc -l`, tracked .go, 25,381 lines total): `internal/server/server.go` 2658,
`internal/server/jira_worklog.go` 1072, `cmd/egw-desktop/configedit.go` 787, `internal/policy/registry.go` 714,
`internal/server/form_session_test.go` 701, `internal/server/feedback_todo_test.go` 695,
`internal/server/jira_worklog_test.go` 568, `internal/server/feedback_todo.go` 529,
`cmd/tools/leakcheck/leakcheck_test.go` 527, `internal/connectors/email/connector.go` 522. Entry points and layout
are clear from AGENTS.md "Key files" and the README "File Structure" section (README.md:286-325).

- **R1 (note)** Connector-specific logic lives in `server.go`, not behind the `Connector` interface: the dispatch is
  an if/else chain with http-rest special cases (`internal/server/server.go:579-594`: `SlackFileContent`,
  `JiraAttachmentContent`, form-session), and the connector list is kept a second time in
  `internal/policy/connectors.go:7`. `server.go` is 2658 lines (dispatch, MCP, Jira, Slack, admin). Todo EG0053.
- **R2 (note)** Bucket boundaries miss some tracked paths: `docs/design/`, `docs/archive/`, `AGENTS.md`,
  `opencode.json` and `go.mod` are owned by no bucket (`todo/_config.yaml:4-10`: `docs` owns `.ai/`, README,
  SECURITY, CLAUDE.md; `ops-config` owns only "connector docs in docs/*.md"). Todo EG0053.

## 2. Tests

**Timed run.** `python -I -X utf8 .scratch/review/sample.py run test-sample.json test.json -- go test -json -count=1 ./...`
(the full suite, well under the 15-minute cap): wall **28.97 s**, exit 0; 254 top-level tests pass, 4 skip, 0 fail
(`slow.py`). Packages by elapsed: `cmd/tools/leakcheck` 16.0 s, `internal/server` 11.4 s,
`internal/connectors/localservice` 4.3 s, the rest under 2.2 s each.

The 10 slowest tests by `Elapsed`:

| # | test | s | verdict | value |
|---|------|---|---------|-------|
| 1 | leakcheck `TestHookChainEndToEnd` | 4.49 | opt-in | Runs this checkout's real installed hook chain; catches a broken install that temp-repo tests miss, but depends on the local `.git/hooks` and skips elsewhere. |
| 2 | localservice `TestCheckGlobalProtectMatchesPANGPAdapter` | 3.34 | opt-in | Cross-checks Go's VPN-adapter detection against PowerShell `Get-NetAdapter`; machine-dependent, cost is the PowerShell start. |
| 3 | server `TestFeedbackTodoNotAdoptedThenAdopted` | 3.04 | speed up | Only test of the not-adopted to adopted switch of the feedback todo writer against real git; could share one fixture repo. |
| 4 | leakcheck `TestPreCommitBlocksAddedLine` | 1.99 | keep | Core guarantee of the names guard: an added line with a term is refused. |
| 5 | leakcheck `TestPreCommitIgnoresUnchangedAndRemovedLines` | 1.33 | keep | Guards against false positives that would block every commit. |
| 6 | server `TestFeedbackTodoBuiltInWriter` | 1.27 | keep | Built-in writer keeps the user's staged and dirty files intact. |
| 7 | leakcheck `TestPreCommitPaths` | 1.19 | keep | Renamed and deleted paths are judged correctly. |
| 8 | leakcheck `TestWorktreeUsesMainCheckout` | 1.18 | keep | A worktree reads the terms file of the main checkout (the untracked file is absent in worktrees). |
| 9 | server `TestFeedbackTodoBuiltInWriterCommitFailureRestores` | 1.12 | keep | The data-loss path: a failed commit restores the index. |
| 10 | leakcheck `TestInstall` | 1.12 | keep | Install into a path with a space (the hook shell quoting). |

**Flaky tests:** none. Checked: `git log --oneline -n 539 | grep -i -E 'flak|intermittent|race|timing'` (only a
todo creation and a "race" test name, no flake report), `grep -l -i flak todo/*.md .ai/PITFALLS.md` (only this
todo), and both suite runs in this review (normal and `-p 1 -cpu 1`) passed with 0 failures. No rerun loop.

- **R3 (note)** No documented affected-subset command. `grep -n -i -E 'go test|affected' .ai/*.md AGENTS.md README.md`
  finds no test command at all; the only guidance is the flow-note prose "go test the changed packages and their
  importers (found by grep)" in `todo/_config.yaml:7-9`. Todo EG0054.
- **R4 (note)** No speed tags. `git grep -n -E 't\.Skip|testing\.Short|//go:build' -- '*_test.go'`: no
  `testing.Short()`, no slow/e2e build tags; the only gating is `//go:build windows` (3 egw-desktop files,
  `globalprotect_windows_test.go`) and environment skips. Todo EG0054.
- **R5 (note)** Four `cmd/egw-desktop` tests read the live, untracked `config.<ws>.yaml`/`operations.<ws>.yaml` at the
  checkout root (`cmd/egw-desktop/configedit_test.go:59`, `cmd/egw-desktop/yamledit_test.go:91`): they skip in a
  worktree (this run: `TestAddServiceWithCatalogCapabilities`, `TestRegenerateAgentUpdatesConfigAndKeys`,
  `TestDisableAndEnableSite`, `TestPatchedRealConfigsUnchangedRoundTrip` skipped) and run against private live data
  in the main checkout. Todo EG0054.
- **R6 (note)** Critical paths with no test: the admin API refusing every call when `admin_token_sha256` is empty
  (`internal/server/server.go:2476-2481`; `git grep -n 'AdminTokenSHA: *""\|authenticateAdmin' -- '*_test.go'`: 0
  hits); `cmd/egw` (the loopback-only listen at `cmd/egw/main.go:197`, service wiring) has no test file
  (`go list -f '{{.ImportPath}} {{len .TestGoFiles}}'`: `cmd/egw 0`); `secrets.ResolveWithWorkspaceCheck`
  (`internal/secrets/resolver.go:229`) is tested only through the form-session path
  (`internal/server/form_session_test.go:448`). Covered: bearer auth on help/capabilities/feedback
  (`internal/server/access_test.go:49,101`, `internal/server/feedback_test.go:110`), grant denial
  (`internal/server/unknown_op_test.go:41`), taint redaction (`internal/secrets/taint_test.go:8`). Todo EG0054.

## 3. Bugs, issues and gaps

**Error handling on main paths:** no ignored error that changes a result. Checked `internal/server/server.go` and
`internal/connectors/httprest/connector.go`: `_, _ = w.Write(buf)` at `internal/server/server.go:122` (response already
committed), unchecked `json.NewEncoder(w).Encode` on responses (for example `internal/server/server.go:300`), an ignored
`json.Marshal` of a built map (`internal/server/server.go:1293`) and unchecked deferred `Close` on read paths. The op
dispatch has no `else`, but `internal/policy/connectors.go:7` refuses unknown connectors at load.

**TODO/FIXME/HACK:** `git grep -n -E '\b(FIXME|HACK|XXX)\b' -- '*.go'`: 0. `\bTODO\b`: 18 hits, all the `/feedback`
TODO-file feature (`internal/server/feedback.go`, `feedback_todo.go` and their tests), none a code marker.

**Drift spot-checks (8):** match: secrets cache 30 s (`internal/secrets/resolver.go:53`); AGENTS.md key files all
exist; `.ai/CONNECTORS.md` dispatch description matches `internal/server/server.go:579-594`; README's
"Clockify scheduler: Stub, not wired" (README.md:371) matches `internal/server/server.go:312`. Drift: R7, R8.

- **R7 (note)** AGENTS.md lists three connectors in its architecture diagram (AGENTS.md:112: http-rest, postgres,
  local-service); the code dispatches seven (`internal/server/server.go:579-594` adds form-session, mcp, email,
  jira-worklog). Its admin table lists 2 endpoints; the server serves 4 (`/admin/secrets`, `/admin/secrets/status`,
  `/admin/reload`, `/admin/diag`; index at `internal/server/server.go:2209`). Todo EG0055.
- **R8 (note)** `server.listen_addr`, `tls_cert` and `tls_key` are documented (README.md:68,
  `config.example/config.yaml:2,6-7`) but ignored: the gateway always listens on plain HTTP at
  `127.0.0.1:<port>` (`cmd/egw/main.go:197`); `ListenAddr` (`internal/config/config.go:209`) is never read and
  `git grep -n -E 'ServeTLS|ListenAndServeTLS' -- '*.go'` finds nothing. Todo EG0055.
- **R9 (note)** Unfinished features: `internal/scheduler` (312 lines) is imported by nothing
  (`git grep -l 'internal/scheduler"' -- '*.go'`: 0 files) and `/capabilities` reports it `stub-not-wired`
  (`internal/server/server.go:312`); KeePass `Set`/`Delete` return "not implemented"
  (`internal/secrets/keepass_store.go:71,75`, documented). Todo EG0055.
- **R10 (note)** Every upstream call logs its URL and the raw request body at info level, unredacted
  (`internal/server/server.go:866-874`), plus a second line (`internal/server/server.go:931`): 3 stderr lines per
  request (measured: 60,005 lines for 20,001 requests in `egw-native.log`, 18.9 MB). Bodies carry agent-supplied
  content (Jira comments, messages); credentials go in headers, not the body or URL (`internal/server/server.go:981-984`).
  Todo EG0055.

## 4. Technology

**Fit:** Go, YAML config, Windows Credential Manager and kardianos/service suit a credential-hiding proxy on a Windows
workstation. Windows-only code is isolated (`git grep` for `//go:build windows`, `x/sys/windows`, `wincred`: all of
`cmd/egw-desktop`, `internal/connectors/localservice/adapters_windows.go`, `internal/secrets/wincred_store.go`), and
the service itself cross-compiles for Linux (`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/egw/`: exit 0,
15.9 MB). No alternative has a stated gain. Toolchain `go1.26.5` (`go version`), `go.mod` says `go 1.25.7`.

- **R11 (note)** `go list -u -m all`: 23 of 35 modules have a newer version; 4 of 10 direct requirements are behind:
  `danieljoos/wincred v1.1.2 [v1.2.3]`, `emersion/go-sasl` (2020 pseudo-version, 2024 available),
  `rs/zerolog v1.31.0 [v1.35.1]`, `golang.org/x/sys v0.41.0 [v0.50.0]`; indirect `golang.org/x/crypto v0.48.0
  [v0.59.0]`, `golang.org/x/net v0.49.0 [v0.62.0]`. Vulnerabilities: UNVERIFIED - needs: `govulncheck ./...`
  (not installed on this machine; fetching and running it with `go run ...@latest` was refused in this session).
  Todo EG0056.

## 5. Performance and resources

### App
| state | figure | measured by |
|-------|--------|-------------|
| idle, 20 s, native Windows build | CPU 0.03 s total since start (about 0 %), peak RSS 57.1 MB, 8 threads, disk read/write 0 | `sample.py pid idle.json <pid> 20` |
| idle, the 3 live services | working set 20.9 / 25.4 / 30.6 MB, 17 / 19 / 27 threads, all listening on 127.0.0.1 | `Get-CimInstance Win32_Process`, `Get-NetTCPConnection -State Listen` |
| typical load, native: 20,000 op calls, 8 clients, fake local upstream (4 KB JSON) | 1,849 req/s; p50 3.7 ms, p95 5.8 ms, p99 9.1 ms, max 634 ms; all 200; CPU 30.1 s in 10.8 s (about 2.8 cores); peak RSS 66.0 MB; 24 threads; writes 23.7 MB (audit 5.9 MB, about 300 B/request; stderr log 18.9 MB) | `loadgen.exe -n 20000 -c 8` with `sample.py pid load-native.json <pid> 15` |

Background processes and threads: one process per workspace, 8 threads at idle, no timers seen (0.03 CPU-s over 20 s).

**Docker cap ladder** (`sh .scratch/review/load/ladder.sh`: a `FROM scratch` image with the Linux build, the
gateway capped, an uncapped load sidecar sharing its network namespace; 60,000 op calls, 8 clients; figures from
`docker stats --no-stream` and the load client). The container leaves out the Windows-only parts (Credential Manager,
VPN-adapter checks, the service manager); an http-rest op without a secret ran the full auth, policy, connector and
audit path.

| step | idle CPU / mem | load CPU / mem | throughput, latency | step wall |
|------|----------------|----------------|---------------------|-----------|
| `--cpus 4 --memory 4g` | 0.00 % / 2.1 MiB | 265 % / 12.1 MiB | 4,938 req/s, p95 3.2 ms, p99 4.2 ms, all 200 | 19.9 s |
| `--cpus 2 --memory 2g` | 0.00 % / 2.1 MiB | 168 % / 10.0 MiB | 3,742 req/s, p95 4.3 ms, p99 6.1 ms, all 200 | 23.3 s |
| `--cpus 1 --memory 1g` | 0.00 % / 2.1 MiB | 103 % / 9.1 MiB | 2,684 req/s, p95 4.0 ms, p99 47.4 ms, all 200 | 30.9 s |
| `--cpus 0.5 --memory 512m` | 0.00 % / 2.1 MiB | 54 % / 10.3 MiB | 1,186 req/s, p95 72.5 ms, p99 78.9 ms, max 500 ms, all 200 | 58.1 s |

Block I/O after each step: 17.9 MB written (audit). Lowest working step: 0.5 CPU / 512 MB, not OOM-killed
(`docker inspect -f '{{.State.OOMKilled}}'`: false).

**Degradation case (measured):** at a quarter of a core per step-down the gateway degrades gracefully: throughput
falls with the CPU cap, p95 rises from 3.2 ms to 72.5 ms at 0.5 CPU (CFS throttling), no request fails or times out
and memory stays near 10 MiB. Memory pressure and a slow disk were not measured (memory never came near any cap).

### Test suite
| state | figure | measured by |
|-------|--------|-------------|
| idle | N/A: the suite leaves no background process (the sampled tree ended with the run) | `sample.py run` |
| full run | wall 28.97 s; CPU 20.8 s (0.72 cores avg); peak RSS 989 MB summed over the tree (go, compile, link, test binaries); 146 threads peak; 49 processes; disk read 1,637 MB, write 35.6 MB | `sample.py run test-sample.json test.json -- go test -json -count=1 ./...` |
| degradation: one CPU, one package at a time | wall 86.6 s (3.0x), all 254 pass, 0 fail; CPU 13.4 s; peak RSS 249 MB; read 1,050 MB. The build cache was warm on this second run, so its CPU is not comparable with the first | `sample.py run ... -- go test -json -count=1 -p 1 -cpu 1 ./...` |
| Docker | cannot run as is: `cmd/egw-desktop` and `globalprotect_windows_test.go` are Windows-only, tests spawn PowerShell and read this checkout's git hooks. A Linux run of `./internal/...` would need a Go image: UNVERIFIED - needs: `docker run --rm -v <repo>:/src -w /src golang:1.26 go test ./internal/...` | `git grep -n '//go:build windows' -- '*_test.go'` |

- **R12 (note)** The suite's floor is process spawning, not CPU: under one CPU it ran 3x slower while using only 0.15
  cores on average (`test-p1-sample.json`: `avg_cores 0.15`), and two packages that start git and PowerShell
  (`cmd/tools/leakcheck` 16.0 s, `internal/connectors/localservice` 4.3 s) take 42 % of the summed package time (48.7 s, `slow.py` over `test.json`). Tagging
  them as a slow tier (R4) is the lever. Todo EG0054.

## 6. Metrics

What is recorded: one JSON line per request in `audit_<date>.log` under `audit.dir` (`internal/audit/logger.go:90`),
with decision, upstream status and `latency_ms` (`internal/audit/logger.go:37`); the operational log is zerolog JSON
to stderr (`cmd/egw/main.go:62`). Where it goes: `C:\ProgramData\external_gateway\audit` (created by
`deploy/provision.ps1:204`); stderr is not redirected anywhere (`git grep -n -i -E 'stderr|RedirectStandard' --
cmd/egw deploy`: only `cmd/egw/main.go:62`). What reads it: nothing in the repo (`git grep -n -i 'latency_ms'`:
only the struct tag). Live audit volume on this machine: UNVERIFIED - needs:
`Get-ChildItem C:\ProgramData\external_gateway\audit | Measure-Object Length -Sum` (outside this session's folders).

- **R13 (note)** No runtime metrics and no retention: `git grep -n -i -E 'expvar|pprof|prometheus|/metrics' --
  '*.go'`: 0 hits; audit files are rotated by size (`max_size_mb`, `internal/audit/logger.go:95-98`) but never deleted
  (`git grep -n -E 'os\.Remove|RemoveAll' -- internal/audit`: 0); the service's stderr log is lost under the SCM.
  Missing to answer area 5 without measuring by hand: per-op request count and latency percentiles, process CPU, RSS,
  goroutines and threads, audit size per day, and a reader that summarises them. Todo EG0057.
- **R14 (note)** The audit `agent_id` is the token's SHA-256, not the configured agent id: `authenticate` returns the
  hash (`internal/server/server.go:2458-2461`) and the load run's audit lines carry `agent_id` equal to
  `sha256("reviewtoken0035")` (`printf %s reviewtoken0035 | sha256sum`: `0e1c726d8980...`). Rotating a token splits
  an agent's history. Todo EG0057.

## 7. Security

**Secrets in history** (`python -I -X utf8 .scratch/review/secrets_scan.py`: `git log --all -E -G <pattern>` and
`git grep -I -l -E <pattern>` per kind, prints only commit ids and paths): GitLab PAT 0, GitHub PAT 0, Slack token 0,
Atlassian token 0, AWS key 0, private key 0, Google OAuth token 0, literal Bearer value 0, quoted password/secret
assignment 0 commits. Token hash: 1 commit (`f248062`, `config.example/config.globex.yaml`), a documented placeholder
(`config.example/config.globex.yaml:99-100`) that matches none of the live config hashes (`hashcheck.py`:
`overlap_live=0`). Positive control `glpat-xxx` (the AGENTS.md placeholder): 3 commits, so the search works.
Total secret hits: 0.

**Real names** (`python -I -X utf8 .scratch/review/names_check.py history|tree`, pattern read from `.ai/LOCAL.md`
at run time): 196 commits on all refs add or remove a line with a real workspace name or Jira key; the tracked tree
has 4 hits, of which 2 are the documented email copy-recipient field false positives and 2 are leaks (R15).

- **R15 (blocking)** A real workspace name is in two tracked files of a public repository
  (`gh repo view --json visibility`: `PUBLIC`): `.ai/OPERATIONS.md:442` and
  `internal/server/jenkins_build_params_test.go:14`. Both landed in `1dcfcdd`/`b3570b1` at 2026-11-16 09:17, before the
  leakcheck hook was installed (`.git/hooks/pre-commit.local` mtime 10:02); the hook checks added lines only, so the
  existing hits stay. Todo EG0052.
- **R16 (blocking)** Real names in git history: 196 commits (`names_check.py history`), on a public repository.
  Matches the existing todo EG0048 (history rewrite).

**Exposure list** (live: `Get-NetTCPConnection -State Listen` for the `egw` and `ExternalGateway` processes):

| listener | endpoint | auth |
|----------|----------|------|
| 3 gateway services, each `127.0.0.1:<port>`, plain HTTP | `GET /healthz`, bare `GET /help` | none (status and banner only) |
| | `GET /admin/` (index) | none: returns the endpoint list only (`internal/server/server.go:2206-2211`) |
| | `GET /whoami`, `POST /feedback` | agent Bearer token (SHA-256 lookup, `internal/server/server.go:2451-2461`) |
| | `GET /help?workspace=`, `GET /capabilities?workspace=` | agent Bearer token plus a grant in the workspace |
| | `POST /v1/workspaces/<ws>/op/<id>` | agent Bearer token, grant, op policy and constraints |
| | `/admin/secrets`, `/admin/secrets/status`, `/admin/reload`, `/admin/diag` | admin Bearer token, constant-time compare (`internal/server/server.go:2489`); refused when no admin hash is set (`internal/server/server.go:2476-2481`) |
| tray app `ExternalGateway.exe`, `127.0.0.1:<ephemeral>` | `/ping` | none |
| | `/` and the API | per-run session secret in `?s=` or `X-Session` (`cmd/egw-desktop/server.go:28,65`) |

File shares: none (`git grep -n -i -E 'SmbShare|net share'`: 0). Outbound: each workspace site's configured
`base_url` (Jira, GitLab, Slack, Kibana, Postgres, mail and others), called as LocalSystem; credentials go in headers
(`internal/server/server.go:981-984`), never the URL.

checked: areas 1-7 in full; timed suite once (`go test -json -count=1 ./...`, 28.97 s) plus one degradation run
(`-p 1 -cpu 1`); app idle and load natively and the 4-step Docker ladder; secrets pattern search (10 kinds plus a
control) and real-names count over all refs; live listeners. Names check of this report and the filed todos:
`python -I -X utf8 .scratch/review/names_check.py paths docs/review/ todo/EG0052.md todo/EG0053.md todo/EG0054.md
todo/EG0055.md todo/EG0056.md todo/EG0057.md` (pattern read from `.ai/LOCAL.md` section 'Real names in tracked
files' at run time) returned 0 hits. Skipped: vulnerability scan (govulncheck not installed, see R11); memory-pressure
and slow-disk degradation (the app never neared a memory cap); the test suite in Docker (Windows-only tests); the live
audit volume (outside this session's folders); a full duplication analysis beyond repeated function names.
