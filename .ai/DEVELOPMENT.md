# Development Guide

## Building

```powershell
# Go 1.23+ required, on PATH in PowerShell and Git Bash: call `go` / `gofmt`, never the full go.exe path.
go build -o egw.exe       ./cmd/egw/
go build -o egw-admin.exe  ./cmd/egw-admin/
go build -o egwctl.exe     ./cmd/egwctl/
```

Builds produce statically-linked `.exe` files in the project root. Go modules are in `go.mod` (module: `github.com/external_gateway`).

## Deploying

`deploy\update.ps1` rebuilds `egw.exe` (`-GoExe` overrides the Go path; a build error stops it before any service is touched), copies it + configs + operations to `C:\Program Files\external_gateway\` and restarts every workspace's service. **Must run as Administrator.**

The `egw.exe` copy retries for up to 30 s while the file is still locked (the SCM can report a service Stopped before its process lets go of the image). If anything between stopping the services and starting them fails (a stop timeout, the lock outlasting the retry, a config copy), the script starts again the gateways that were running before the deploy and then exits non-zero. They come back on the old `egw.exe`, or with a partly copied config set when a config copy failed; rerun the deploy once the cause is fixed. Helpers and their tests: `deploy/deploylib.ps1`, `go test ./deploy/`.

The script does NOT patch configs — all config values are the source of truth in the repo.

Service binary path:
- `C:\Program Files\external_gateway\egw.exe --config "C:\Program Files\external_gateway\acme\config.yaml" --ops "C:\Program Files\external_gateway\acme\operations.yaml" --port <port> --svc-name egw-acme`

## Running the desktop app (run-app check)

How an agent starts and drives `ExternalGateway.exe` (`cmd/egw-desktop`) to test a change a user sees in its UI
(core rule "UI work is tested in the real app"). A browser harness or a stubbed backend does not count.

**Build** in the repo root (README.md "Build"):

```powershell
go build -ldflags -H=windowsgui -o ExternalGateway.exe ./cmd/egw-desktop/
```

The exe must sit in the repo root: `--build-dir` defaults to the exe's folder, and `config.<ws>.yaml`,
`operations.<ws>.yaml`, `agent-keys.txt` and `deploy\` are read from there (`main.go` flags). A running instance
locks `ExternalGateway.exe`, so the build fails until it is quit; or build to another name in the repo root, e.g.
`ExternalGateway-test.exe` (`*.exe` is gitignored).

**Start** it the way its user does. One instance per data dir (`win.go` `acquireSingleInstance`): a second start
with the same `--data-dir` only reopens the running instance's window (`main.go` `signalRunningInstance`), or exits
silently with `--tray`, so a fresh build does not run while the owner's instance is up.
- Owner instance running: ask the owner before quitting it. Once quit, rebuild `ExternalGateway.exe` and start it
  with no flags (default data dir `%APPDATA%\ExternalGateway`) in the background, keeping its pid:
  `$p = Start-Process .\ExternalGateway.exe -PassThru; $p.Id` (the exe is `-H=windowsgui`, so `-PassThru` is how to
  get the pid).
- Only if the owner keeps theirs: start the test build with
  `Start-Process .\ExternalGateway-test.exe -ArgumentList '--data-dir',"`"$env:TEMP\egw-run-app`"",'--tray' -PassThru`
  (a fresh data dir gets a random port; a reused one keeps the port in its `instance.json`), and say in the test record that it ran with a scratch data dir (default
  settings, fresh state).

**Drive** it. The UI is a local web page served by the app itself, `http://127.0.0.1:<port>/?s=<session>`
(`main.go` `uiURL`); the page sends the session as `X-Session`. Port and session are in `<data-dir>\instance.json`
(written at start in `main.go`): `Get-Content "$env:APPDATA\ExternalGateway\instance.json" | ConvertFrom-Json`.
Open that URL with Claude in Chrome or the built-in browser pane, or use computer-use on the Edge `--app` window it
opens (`win.go` `openWindow`; not opened with `--tray`). The tray menu, Windows notifications and the tray icon colour
need computer-use (`tray.go` `runTray`).

**Stop** only what you started, by pid: `Stop-Process -Id <pid>`, never by image name (that would kill the owner's
instance too). Quit is only in the tray menu. `Stop-Process` skips the tray cleanup, so a stale tray icon may linger
until hovered (cosmetic). Delete a scratch data dir afterwards, and restart the owner instance you quit (no flags)
when done.

**Backend and data are real.** The app reads `config.<ws>.yaml`, `operations.<ws>.yaml` and `agent-keys.txt` from
`--build-dir`, the deployed configs under `--install-dir` (default `C:\Program Files\external_gateway`) and the
running `egw-<ws>` services.
- Edits write the real (gitignored) source configs in the repo root: check the diff the app shows before applying.
  The only undo is the backups folder, `<data-dir>\backups\<yyyyMMdd-HHmmss>\` (`configedit.go`).
- Publish, deploy and service start/stop/restart run through an elevated helper (`elevate.go` `runElevated`,
  ShellExecute `runas`): unless the app itself runs elevated, they raise a UAC prompt an agent cannot confirm, so
  ask the owner to do those steps.
- Record the run as `test.kind=e2e` with `runner` and `steps` (`.ai/shared/rules/todo.md` "Test runs").

## Running tests

**Affected subset.** For a changed package, test it and every package whose `Deps`, `TestImports` or
`XTestImports` contain it (set `$p` to the changed package's import path):

```powershell
$p = 'github.com/external_gateway/internal/config'; go test -count=1 (go list -f '{{.ImportPath}} {{join .Deps " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./... | Where-Object { " $_ " -match " $([regex]::Escape($p)) " } | ForEach-Object { ($_ -split ' ')[0] })
```

It misses a package whose test imports a helper package that in turn imports the changed one (test imports'
own deps are not listed); add such a package by hand.

**Speed tiers.** `go test -short ./...` is the fast tier. The slow tier is the tests that spawn git or PowerShell
(leakcheck's repo tests, `internal/server`'s `gitRepo` feedback tests, localservice's PANGP adapter check): each
package's `skipSlow` test helper skips them with a "slow tier" message under `-short`. The pre-merge run is
without `-short`.

## Testing operations

No unit tests exist. Testing is done manually via `Invoke-RestMethod` against the running gateway.

Quick test pattern:
```powershell
$token = "WpnrHLaY…REDACTED-see-agent-keys.txt"
$body = @{env = "prod"; param = "value"} | ConvertTo-Json -Compress
$r = Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/acme/op/gitlab.branches" `
  -Method Post -Body $body -ContentType "application/json" `
  -Headers @{"Authorization" = "Bearer $token"}
```

Dry-run (validates params + grants, doesn't execute upstream):
```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:<port>/v1/workspaces/acme/op/gitlab.projects?dry_run=true" `
  -Method Post -Body "{}" -ContentType "application/json" -Headers @{"Authorization" = "Bearer $token"}
```

Useful debug endpoints:
- `GET /healthz` — service alive check
- `GET /whoami` — agent identity and grants (needs auth)
- `GET /help` — public banner; `?workspace=<ws>` needs a Bearer token with a grant there (granted ops only)
- `GET /capabilities?workspace=<ws>` — granted ops only + config checksum + feature status (Bearer token)
- `POST /feedback` `{"type","text"}` — append to `feedback.log` (Bearer token required; agent id is recorded). With `feedback.todo_repo`
  set in `config.<x>.yaml` (hot-reloadable; `todo_python` absolute and `todo_bucket` then required), `"type":"todo"`
  (text whitespace-collapsed, capped at 400 chars; empty text -> 400) is also filed in that repo R:
  - **Adopted** (`todo/_config.yaml` committed in R's HEAD, checked per request with `git rev-parse --verify -q HEAD`
    and `git ls-tree HEAD -- todo/_config.yaml`; the working tree does not count): a `draft` todo, P2, size S, bucket
    `todo_bucket`, title = text cut at 120 chars, body = text + `Filed via /feedback by <agent> on <date>.`. Through
    the ToDo tool (`todo_python -I -X utf8 todo_tool new ...` then `commit`) when `todo_tool` runs, else written by
    the gateway as the todo rule's "Without the tool" section says (`todo/<ID>.md`, its PRIORITY.md line, one `[hand]`
    commit of those two paths, R's hooks run). Every git/python child gets identity `egw-feedback`,
    `safe.directory=R` via `GIT_CONFIG_*` and PATH prefixed with `todo_python`'s directory (the services run as
    LocalSystem). Responds `{"status":"added","todo":"<ID>"}`, or `"duplicate"` when a `todo/<id_prefix>NNNN.md` body starts with
    the same text. Any failure (check, tool, commit) is a 500 after `feedback.log` was written; nothing is half-written.
  - **Not adopted**: appends `- [ ] <text> (date, via /feedback by <agent>).` to the legacy to-do list file
    `todo_path` (default: that file in R; created with a `# TODO` header if missing; parent dir must exist; line
    endings kept). Responds `{"status":"added"}` or `"duplicate"` (same item text already there).
  Other types and an unset `todo_repo` respond `"recorded"`.
- `POST /admin/reload` — hot-reload config + ops from disk (needs admin auth)
- `GET /admin/metrics` — per-op counts and latency since start plus process CPU/RSS/goroutines/threads (needs admin
  auth; counters live on `Server`, so a reload keeps them; dry runs are not counted)
- `POST /admin/secrets` — set a secret (needs admin auth)

## Health checks

```powershell
# Is the service running?
Get-Service egw-acme, egw-globex

# Is the gateway responding?
Invoke-RestMethod http://127.0.0.1:<port>/healthz
```

## Audit logs

Stored at `C:\ProgramData\external_gateway\audit\audit_YYYYMMDD.log` — append-only JSONL. `agent_id` is the
calling agent's config `agents[].id` (EG0064; files written before it hold the token's SHA-256 there):
```json
{"ts":"2026-07-09T10:06:40Z","request_id":"...","agent_id":"...","workspace":"acme","operation":"gitlab.projects","params":{...},"decision":"allow","upstream_status":200,"latency_ms":1411}
```

## Adding a new operation

1. Add an entry in `operations.<workspace>.yaml` under `operations:`
2. If it uses a new site or environment, add to `config.<workspace>.yaml`
3. If a new connector type is needed, implement `connectors.Connector` interface and wire it into `server.go`
4. Add the operation ID to the relevant role in `config.<workspace>.yaml`
5. Build, deploy, restart

## Project layout conventions

- `cmd/` — entry points (one per binary)
- `internal/` — all library code (not importable externally by Go convention)
- `config.example/` — template configs for new deployments
- `deploy/` — PowerShell provisioning and update scripts
- Operations YAML files live in the project root alongside config YAML files
- Agent tokens are in `agent-keys.txt` (git-ignored, generated by the provision script)

## Logging

Zerolog writes to stderr. When running as a Windows service, stderr may not be captured — check `C:\ProgramData\external_gateway\audit\` for audit logs instead. Application logs include "upstream request" and "upstream response" entries with URL, method, status, and body previews.
