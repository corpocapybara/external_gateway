# EG0065 design: Audit file retention and a persisted service log under the SCM (review R13)

## Decisions in brief

- D1 (owner 2026-11-20): `AuditConfig.RetentionDays *int` (yaml `audit.retention_days`), `Retention()` = 90 when
  nil; negative = `Validate` error naming `audit.retention_days`. `Config.Log.File` (yaml `log.file`). Startup
  config only. See D1.
- D2: `audit.Prune(dir, days, now) error`: anchored regex, UTC date parse, cutoff = UTC midnight of now minus days
  (days clamped to 36500); older names removed via seam `removeFile`; not-exist ignored, other errors joined. See D2.
- D3: `Logger` gets clock, retention, `prunedDay`, `pruneErr`; `newLogger(dir, maxSizeMB, retention, now)`;
  `Init(dir, maxSizeMB, retentionDays)` keeps the global guard, prunes once, keeps errors for `PruneErr()`; `Log`
  prunes when its UTC day != `prunedDay`. Server tests pass 0. See D3.
- D4: `internal/svclog`: `New()` gives a `Writer` with no file; `Switch(path, retention)` opens (MkdirAll), or only
  re-prunes for the same path; `Write` without a file returns an error; rotation = close, rename to
  `<base>_<UTC yyyymmdd-hhmmss>[_N].log`, reopen, prune own base; a failed rotation reopens the old file and waits
  a minute. Seams `removeFile`, `renameFile`. See D4.
- D5 (owner 2026-11-20): `serviceLogPath(interactive, svcName, override)`: default
  `<ProgramData>/external_gateway/logs/<svc-name>.log`; interactive gives "". See D5.
- D6: `newLogger(path, stderr) (zerolog.Logger, *svclog.Writer)` tees stderr and the writer; an open failure warns on
  the logger and keeps the writer. main builds it first with retention 0 (no prune before config), then
  `w.Switch(serviceLogPath(false, svc, cfg.Log.File), cfg.Audit.Retention())`; `log.Fatal` after that becomes
  `logger.Fatal`. See D6.
- D7: docs in config.example/config.yaml and config.globex.yaml. See D7.

## Acceptance

Restated from todo EG0065 (the todo is authoritative):
1. Config `audit.retention_days` (*int, 90 unset, 0 keeps all, negative = Load error) and `log.file`; TestAuditRetentionDays.
2. `audit.Prune` deletes only anchored audit names older than the UTC cutoff, keeps the boundary day and odd names,
   tolerates missing/removed files, joins other errors; TestPruneAuditFiles.
3. Prune at Init and on the first record of a new UTC day, not on size rotation; TestLogPrunesOnDayRollover.
4. `serviceLogPath`; TestServiceLogPath.
5. Wiring: default file opened before config load, tee with stderr, switch after load, fallback to stderr with a
   warning; TestNewLoggerTeesAndFallsBack.
6. `internal/svclog` rotation and own-base prune; TestServiceLogRotatesAndPrunes, TestWriterConcurrentWrites.
7. Docs in config.example.
8. Manual e2e after deploy.

## Anchors

- `internal/config/config.go`: `Config`, `AuditConfig`, `Config.Validate`, `Load`.
- `internal/audit/logger.go`: `Logger`, `Init`, `GetLogger`, `Logger.Log`.
- `cmd/egw/main.go`: `main`, `resolveConfig`, `setup` (the `audit.Init` call), `program`.
- `internal/server/server.go`: `New` (reads `audit.GetLogger()`; unchanged); the `/admin/reload` handler (calls
  `config.Load`; unchanged).
- `cmd/egw-desktop/configedit.go`: calls `config.Load` (gets the new validation; unchanged).
- `config.example/config.yaml`, `config.example/config.globex.yaml`: `audit:` block.
- zerolog `writer.go` `multiLevelWriter.Write` (module github.com/rs/zerolog v1.35.1): calls every writer even after
  one fails.
- New: `internal/svclog/svclog.go` (`Writer`, `New`, `Writer.Switch`, `Writer.Write`, `Writer.Close`, `Prune`),
  `cmd/egw/logging.go` (`serviceLogPath`, `newLogger`).

## Decisions

### D1 Config

`AuditConfig` gains `RetentionDays *int` (yaml `retention_days`) and method `Retention() int` (nil -> 90). A pointer
is the only way to tell unset from 0. `Config.Validate` returns `audit.retention_days %d is negative` for a negative
value (placed first, beside the feedback check, so it runs for every config). `Config` gains `Log LogConfig` (yaml
`log`) with `File string` (yaml `file`); no validation (an unopenable path falls back at runtime, D6). yaml.v3 is
non-strict, so older configs load unchanged. `/admin/reload` does not re-init audit or the log writer (audit.Init is
guarded), so both settings apply from the startup config; but reload runs `config.Load`, so a negative
`retention_days` in a reloaded file is rejected like any other invalid config. The docs say both.

### D2 Audit prune

`var auditName = regexp.MustCompile("^audit_([0-9]{8})(_[0-9]+)?[.]log$")`, `var removeFile = os.Remove` (test seam).
`Prune(dir string, days int, now time.Time) error`: `days <= 0` returns nil; `days > 36500` is treated as 36500 (no
AddDate overflow). `cutoff := utcMidnight(now.UTC()).AddDate(0, 0, -days)` (UTC: no DST). `os.ReadDir(dir)`:
not-exist -> nil, other error returned. For each non-dir entry matching the regex,
`time.ParseInLocation("20060102", m[1], time.UTC)`; parse error -> keep; `date.Before(cutoff)` -> `removeFile`;
`errors.Is(err, fs.ErrNotExist)` -> ignore; other errors appended; the walk continues; return `errors.Join(errs...)`.
With days=90 and now = 2026-11-20 any time of day UTC, cutoff = 2026-08-22: `audit_20260822.log` kept,
`audit_20260821.log` deleted. Several services pruning one dir race only on remove, which tolerates not-exist; the
shortest retention wins (documented). Go opens files with FILE_SHARE_READ|FILE_SHARE_WRITE only, so removing a
file another service holds open for an append fails with a sharing violation; that error is returned (D3 keeps
it) and the next day's prune retries. Such a file is at least `days` old and is not written again.

### D3 Prune timing in the Logger

`Logger` gains `retention int`, `now func() time.Time`, `prunedDay string`, `pruneErr error`. `newLogger(dir,
maxSizeMB, retention, now)` holds today's body of `Init` (MkdirAll, max-size default), runs the first prune, stores
its error in `pruneErr` and sets `prunedDay`. `Init(dir, maxSizeMB, retentionDays)` keeps the global guard (a second
call returns nil without pruning) and calls `newLogger(..., time.Now)`; prune errors never fail `Init`;
`(*Logger).PruneErr()` returns them, read by main's `setup` through `audit.GetLogger()` and logged as a warning. In
`Log`, under the mutex, `t := l.now()`; `today := t.UTC().Format("20060102")` drives the file name and the default
record timestamp (`t.UTC().Format(time.RFC3339)`), replacing both direct `time.Now()` calls; when `today !=
l.prunedDay` it prunes with `t`, sets `prunedDay`, stores the error in `pruneErr` (no logger in the package; the
next day retries). Keyed on the date, so the size-rotation path never prunes. The first record of a day pays one
`ReadDir` of about 100 entries under the mutex: accepted (once a day). The one production caller of `Init` is
`cmd/egw/main.go` `setup`; the `internal/server` tests calling `audit.Init(dir, n)` (grep `audit.Init(`, 9 sites in
7 files) gain a third argument `0`.

### D4 internal/svclog

```go
type Writer struct {
    mu sync.Mutex; f *os.File; path string; size int64; retention int
    retryAt time.Time         // no rotation or reopen attempt before this time
    MaxBytes int64            // default 10 MB
    Now func() time.Time      // default time.Now
}
var removeFile, renameFile = os.Remove, os.Rename // test seams
func New() *Writer
func (w *Writer) Switch(path string, retention int) error
func (w *Writer) Write(p []byte) (int, error)
func (w *Writer) Close() error
func Prune(path string, days int, now time.Time) error   // own base only
```

`Switch`: lock. Same path as the open file (`strings.EqualFold` of `filepath.Clean`): set retention, prune, return.
Else `MkdirAll(dir)`, open append, size from Stat; on error keep the old file (or none) and return the error; on
success close the old file, set path, retention, size, prune. `Write`: lock; `f == nil`: when `path != ""` and
`Now() >= retryAt` try one reopen of `path` (failure sets `retryAt = Now()+1 min`), else return `errNoFile`. When
`size > 0 && size+len(p) > MaxBytes && Now() >= retryAt`, rotate. A single write larger than MaxBytes goes whole
into a fresh file. Then write and `size += n`. Rotate: close `f` (Windows cannot rename an open file); target
`<base>_<Now().UTC() 20060102-150405>.log` in the same dir, then `_1` … `_99` while the target exists; none free or
`renameFile` fails: rotation failure. Reopen `path` (new file, size 0 after a rename; the old file and its size
after a failure, with `retryAt = Now()+1 min` so a failed rotation is not retried on every write); reopen failure
leaves `f == nil` (next `Write` retries as above). After a successful rotation, prune. `Close` closes and sets
`f = nil, path = ""`, so a later `Write` returns `errNoFile`. Own-base regex:
`"^" + regexp.QuoteMeta(base) + "_([0-9]{8})-[0-9]{6}(_[0-9]+)?[.]log$"`, so `egw-acme` never matches `egw-acme2_…`,
and neither matches the audit regex (a hyphen follows the date) nor the reverse. Prune uses D2's cutoff rule,
`removeFile`, the not-exist tolerance and the joined errors. Prune errors from rotation are ignored (no logger
inside the writer); `Switch` returns them. Only `cmd/egw` uses the package; main never calls `Close` (the process
exits).

### D5 Default path

`serviceLogPath(interactive bool, svcName, override string) string` in `cmd/egw/logging.go`: interactive -> `""`
(so `log.file` is ignored for console, `-install` and `-remove` runs); override non-empty -> override; else
`filepath.Join(programData(), "external_gateway", "logs", svcName+".log")` where `programData()` is
`os.Getenv("ProgramData")`, falling back to `C:\ProgramData`. Each service has its own svc-name, so default files
never collide; two configs naming the same `log.file` is the operator's choice (documented).

### D6 Wiring in main

`newLogger(path string, stderr io.Writer) (zerolog.Logger, *svclog.Writer)`: `path == ""` -> stderr-only logger, nil
writer. Else `w := svclog.New()`, `logger := zerolog.New(zerolog.MultiLevelWriter(stderr, w))` with timestamp and
caller as today; `err := w.Switch(path, 0)` (retention 0: nothing is pruned before the config is read); on error one
`Warn` line `service log file unavailable` with field `path` and the error (it reaches stderr; the writer drops it
with `errNoFile`). `main`: `logger, w := newLogger(serviceLogPath(service.Interactive(), *svcName, ""), os.Stderr)`,
then the `-v` level, before `-install`/`-remove` and `resolveConfig`. After `resolveConfig`, when `w != nil`:
`w.Switch(serviceLogPath(false, *svcName, cfg.Log.File), cfg.Audit.Retention())`; an error logs the same warning
and leaves the writer as it was (the default file, or none: then a good `log.file` still opened here, since
`Switch` opens whenever the path differs or no file is open). Every logger copy (program, setup, server.New)
shares `w`, so the in-place switch needs no rebuild. `setup` passes `cfg.Audit.Retention()` to `audit.Init` and
logs `audit prune` warnings from `audit.GetLogger().PruneErr()`. The `log.Fatal` calls for `service.New` and
`svc.Run` in the run path become `logger.Fatal` so they reach the file; the `-install`/`-remove` ones stay
(interactive). zerolog's `multiLevelWriter.Write` calls every writer even after one fails, so an invalid stderr
under the SCM does not block the file; writes are unbuffered, so `Fatal` reaches the file before `os.Exit`.

### D7 Docs

`config.example/config.yaml`: under `audit:` a commented `retention_days: 90` with "0 keeps every file; services that
share one dir prune it with the shortest retention; applies from the startup config, not /admin/reload (a reload
still validates it)"; a commented `log:` / `file:` block naming the default path, used only under the SCM, the
same retention, and that two configs must not share one file. `config.globex.yaml` gets the `retention_days` line.

## Test plan

No test in the touched packages calls `t.Parallel` alongside these; every test that opens a writer closes it in
`t.Cleanup` (an open handle makes `t.TempDir` cleanup fail on Windows). JSON log lines are decoded before
comparing paths (zerolog escapes backslashes).

1. `internal/config` `TestAuditRetentionDays`: Load temp configs with unset/0/30 -> `Retention()` 90/0/30; -1 ->
   error containing `audit.retention_days`; `log: {file: X}` -> `cfg.Log.File == X`.
2. `internal/audit` `TestPruneAuditFiles`: days=90 at now 2026-11-20T00:00:01Z, 2026-11-20T23:59:59Z and
   2026-11-21T03:00+05:00 (= 11-20 22:00Z): `audit_20260821.log`, `audit_20260821_3.log` deleted;
   `audit_20260822.log`, `audit_20261120.log` kept; `audit_20261399.log`, `audit_2026112.log`,
   `audit_20261120_x.log`, `feedback.log`, `notes.txt`, `egw-acme_20200101-000000.log` and a directory
   `audit_20200101.log` kept; days=0 deletes nothing; missing dir -> nil; with `removeFile` stubbed to fail one name
   with a fake error and another with `fs.ErrNotExist`: the other old files are still deleted, the returned error
   wraps the fake one only.
3. `internal/audit` `TestLogPrunesOnDayRollover`: plant `audit_<day-100>.log` before `newLogger` (fake clock, days=90)
   -> deleted at construction, `PruneErr()` nil; plant a second old file; set `l.maxSize` small (same package),
   Log three times on day 1 -> `audit_<day1>_1.log` exists (rotation happened) and the old file survives; advance the
   clock one day, Log -> deleted; with `removeFile` failing, `PruneErr()` returns the error.
4. `cmd/egw` `TestServiceLogPath`: interactive -> ""; override -> override; default from svc-name under `ProgramData`;
   `ProgramData` unset (t.Setenv "") -> `C:\ProgramData\...`.
5. `cmd/egw` `TestNewLoggerTeesAndFallsBack`: a stderr writer that always errors, temp path -> the logged line is in
   the file; a path `filepath.Join(regularFile, "x.log")` -> stderr buffer holds a warning whose decoded `path` field
   equals that path and the writer's next write returns an error; then `w.Switch(goodPath, 0)` -> lines reach the
   good file (a failed default does not block `log.file`); `w.Switch(path, 0)` before any config prunes nothing (a
   planted old rotated file survives).
6. `internal/svclog` `TestServiceLogRotatesAndPrunes`: fake `Now` returning `time.Date(2026,11,21,3,0,0,0,
   time.FixedZone("x", 5*3600))`, small MaxBytes: rotation gives `egw-acme_20261120-220000.log`; a second rotation in
   the same second gives `_1`; days=90: `egw-acme_20260821-…` deleted, `egw-acme_20260822-…` kept,
   `egw-acme2_20200101-000000.log` and `audit_20200101.log` kept; `renameFile` stubbed to fail -> writes continue into
   the same file with all data intact and no second rename attempt within the minute; `Write` after `Close`
   returns an error, no panic. `TestWriterConcurrentWrites`: days=0, a fake `Now` advancing 1 s per call,
   MaxBytes sized for under 50 rotations; 20 goroutines x 200 unique `g<id>-<seq>` lines; read every file in the
   dir: 4000 lines, no duplicates, each matches the full line regex.
7. Docs: read through.
8. Manual e2e as in acceptance 8.

## Not changed

- Audit record format, file naming and size rotation (`max_size_mb`).
- `/admin/reload` (does not re-init audit or the log writer).
- egw-desktop, egwctl, egw-admin logging.
- Event Log integration (owner chose the file).
