# EG0065 design: Audit file retention and a persisted service log under the SCM (review R13)

## Decisions in brief

- D1 (owner 2026-11-20): `audit.retention_days` is `*int` on `AuditConfig`; `Retention()` gives 90 when nil, else the
  value; negative = `Config.Validate` error containing `audit.retention_days`. New `Config.Log` (`LogConfig{File}`,
  yaml `log.file`). Both apply from the startup config only. See D1.
- D2: `audit.Prune(dir, retentionDays, now) error`: anchored regex, `time.Parse("20060102")` in UTC, cutoff = UTC
  midnight of now minus N days, delete when name date < cutoff; non-matching or unparseable names kept; not-exist
  ignored; other remove errors joined and returned after the whole dir is walked. See D2.
- D3: `Logger` gets `retention int`, `now func() time.Time`, `prunedDay string`; built by `newLogger` (no global);
  `Init(dir, maxSizeMB, retentionDays) error` prunes once and returns nothing for prune errors but stores them for
  `PruneErr()`; `Log` prunes when the record's UTC day (from the same clock as the file name) != `prunedDay`;
  server tests calling `Init` pass 0. See D3.
- D4: new package `internal/svclog`: `Writer` (mutex, in-memory size, injectable `MaxBytes`/`Now`), `Open(path,
  retention)`, `Switch(path, retention)`, rotation by rename to `<base>_<UTC yyyymmdd-hhmmss>[_N].log`, prune of own
  base only via the same cutoff rule. A failed rotation keeps the current file. See D4.
- D5 (owner 2026-11-20): default path `<ProgramData>/external_gateway/logs/<svc-name>.log`, from
  `serviceLogPath(interactive, svcName, override)` in cmd/egw; interactive = stderr only. See D5.
- D6: `newLogger(path, stderr)` in cmd/egw builds `zerolog.New(MultiLevelWriter(stderr, w))`; open failure gives a
  stderr-only logger plus a warning naming the path. main builds it before `resolveConfig`; after load it calls
  `w.Switch(cfg.Log.File or default, cfg.Audit.Retention())` and logs an audit-prune warning if any. See D6.
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
- `internal/server/server.go`: `New` (reads `audit.GetLogger()`; unchanged).
- `cmd/egw-desktop/configedit.go`: calls `config.Load` (gets the new validation; unchanged).
- `config.example/config.yaml`, `config.example/config.globex.yaml`: `audit:` block.
- New: `internal/svclog/svclog.go` (`Writer`, `Open`, `Writer.Switch`, `Writer.Write`), `cmd/egw/logging.go`
  (`serviceLogPath`, `newLogger`).

## Decisions

### D1 Config

`AuditConfig` gains `RetentionDays *int \`yaml:"retention_days"\`` and method `Retention() int` (nil -> 90). A pointer is
the only way to tell unset from 0. `Config.Validate` returns `audit.retention_days %d is negative` for a negative value
(placed first, beside the feedback check, so it runs for every config). `Config` gains `Log LogConfig \`yaml:"log"\``
with `File string \`yaml:"file"\``; no validation (an unopenable path falls back at runtime, D6). yaml.v3 is
non-strict, so older configs load unchanged. `/admin/reload` does not re-init audit or the log writer (audit.Init is
guarded today), so both settings apply from the startup config; the docs say so.

### D2 Audit prune

`var auditName = regexp.MustCompile(`^audit_([0-9]{8})(_[0-9]+)?\.log$`)`. `Prune(dir string, days int, now time.Time) error`:
`days <= 0` returns nil. `cutoff := utcMidnight(now).AddDate(0, 0, -days)`. `os.ReadDir(dir)`: not-exist -> nil. For
each non-dir entry matching the regex, `time.ParseInLocation("20060102", m[1], time.UTC)`; parse error -> keep;
`date.Before(cutoff)` -> `os.Remove`; `errors.Is(err, fs.ErrNotExist)` -> ignore; other errors appended; return
`errors.Join(errs...)`. With N=90 and now = 2026-11-20 any time of day, cutoff = 2026-08-22: `audit_20260822.log` kept,
`audit_20260821.log` deleted. Several services pruning one dir race only on Remove, which tolerates not-exist; the
shortest retention wins (documented). Go opens files with FILE_SHARE_DELETE, so a file another service has open for
one append can still be removed; such a file is at least N days old and is not written again.

### D3 Prune timing in the Logger

`Logger` gains `retention int`, `now func() time.Time`, `prunedDay string`, `pruneErr error`. `newLogger(dir, maxSizeMB,
retention, now)` holds today's body of `Init` (MkdirAll, max-size default) and runs the first prune, recording
`prunedDay`. `Init(dir, maxSizeMB, retentionDays)` keeps the global guard and calls `newLogger(..., time.Now)`; prune
errors never fail `Init` (stored, read by `(*Logger).PruneErr()` so main logs a warning). In `Log`, under the mutex, `today :=
l.now().UTC().Format("20060102")` (also used for the file name and the default record timestamp, replacing the direct
`time.Now()` calls); `if today != l.prunedDay` prune with `l.now()`, set `prunedDay`, ignore the error (no logger in
the package; the next day retries). Keyed on the date, so the size rotation path never prunes. The one production
caller of `Init` is `cmd/egw/main.go` `setup`; the `internal/server` tests that call `audit.Init(dir, n)` (grep
`audit.Init(`) gain a third argument `0` (keep everything, no prune in test temp dirs).

### D4 internal/svclog

```go
type Writer struct {
    mu sync.Mutex; f *os.File; path string; size int64; retention int
    MaxBytes int64            // default 10 MB
    Now func() time.Time      // default time.Now
}
func Open(path string, retention int) (*Writer, error)  // MkdirAll(dir), open append, size from Stat, prune
func (w *Writer) Switch(path string, retention int) error // same path: set retention, prune; else open new, close old
func (w *Writer) Write(p []byte) (int, error)
func (w *Writer) Close() error
func Prune(path string, days int, now time.Time) error   // own base only
```
`Write`: lock; if `size > 0 && size+len(p) > MaxBytes` rotate; write; `size += n`. Rotate: close, target
`<base>_<now.UTC() 20060102-150405>.log` in the same dir; if it exists try `_1`, `_2`, … (first free, up to 100);
`os.Rename`; on rename failure reopen the same file and keep its size (logging continues, the next write retries);
reopen fresh file, size 0; prune. Own-base regex: `^` + `regexp.QuoteMeta(base)` + `_([0-9]{8})-[0-9]{6}(_[0-9]+)?\.log$`,
so `egw-acme` never matches `egw-acme2_…` and neither matches the audit regex (hyphen in the time part) nor the
reverse. The prune boundary rule is D2's (UTC date of the name vs UTC midnight minus N days). `Switch` to the same
path (compared after `filepath.Clean`, case-insensitive) only sets the retention and prunes once, with no reopen;
on failure to open a new path it keeps the old file and returns the error. Only `cmd/egw` uses the package.

### D5 Default path

`serviceLogPath(interactive bool, svcName, override string) string` in `cmd/egw/logging.go`: interactive -> `""`;
override non-empty -> override; else `filepath.Join(programData(), "external_gateway", "logs", svcName+".log")` where
`programData()` is `os.Getenv("ProgramData")`, falling back to `C:\ProgramData`. Each service has its own svc-name,
so default files never collide; two configs naming the same `log.file` is the operator's choice (documented).

### D6 Wiring in main

`newLogger(path string, stderr io.Writer, retention int) (zerolog.Logger, *svclog.Writer)`: `path == ""` -> stderr-only
logger, nil writer. Else `svclog.Open(path, retention)`; on error a stderr-only logger and one `Warn` line
`service log file unavailable` with `path` and the error; else `zerolog.New(zerolog.MultiLevelWriter(stderr, w))`.
Timestamp, caller and `-v` level are applied as today. `main`: `interactive := service.Interactive()`, `logger, w :=
newLogger(serviceLogPath(interactive, *svcName, ""), os.Stderr, 90)` before `-install`/`-remove` and `resolveConfig`;
after `resolveConfig`, when `w != nil`, `w.Switch(serviceLogPath(false, *svcName, cfg.Log.File), cfg.Audit.Retention())`
(a failure logs a warning and keeps the default file). Every logger copy (program, setup, server.New) shares `w`, so
switching in place needs no rebuild. `setup` passes `cfg.Audit.Retention()` to `audit.Init` and logs
`audit prune` warnings from `PruneErr()`. zerolog's multi-writer calls every writer even after one errors (writer.go
`multiLevelWriter.Write`), so an invalid stderr under the SCM does not block the file. `Fatal` writes synchronously
before `os.Exit`, so it reaches the file.

### D7 Docs

`config.example/config.yaml`: under `audit:` a commented `retention_days: 90` with "0 keeps every file; services that
share one dir prune it with the shortest retention; applies from startup config, not /admin/reload"; a commented
`log:` / `file:` block naming the default path and that it is used only under the SCM. `config.globex.yaml` gets the
`retention_days` line.

## Test plan

1. `internal/config` `TestAuditRetentionDays`: Load temp configs with unset/0/30 -> Retention() 90/0/30; -1 -> error
   containing `audit.retention_days`; `log: {file: X}` -> `cfg.Log.File == X`.
2. `internal/audit` `TestPruneAuditFiles`: table at now 2026-11-20 00:00:01Z and 23:59:59Z, N=90; files 20260821
   (deleted), 20260821_3 (deleted), 20260822 (kept), today (kept), odd names (kept); N=0 deletes nothing; missing dir nil.
3. `internal/audit` `TestLogPrunesOnDayRollover`: newLogger with fake clock and tiny max size; plant an old file; Log
   twice and force a size rotation on day 1 -> file still there; advance clock to day 2, Log -> deleted.
4. `cmd/egw` `TestServiceLogPath`: four cases incl. `ProgramData` unset (t.Setenv).
5. `cmd/egw` `TestNewLoggerTeesAndFallsBack`: failing writer as stderr, temp path -> line in file; path under a
   regular file -> nil writer, warning in a buffer stderr.
6. `internal/svclog` `TestServiceLogRotatesAndPrunes` (time.Local = a fixed +05:00 zone, restored after; MaxBytes small;
   same-second double rotation -> `_1`; acme/acme2 own-base prune; boundary day) and `TestWriterConcurrentWrites`
   (20 goroutines x 200 lines, every line in the files parses as whole).
7. Docs: read through.
8. Manual e2e as in acceptance 8.

## Not changed

- Audit record format, file naming and size rotation (`max_size_mb`).
- `/admin/reload` (does not re-init audit or the log writer).
- egw-desktop, egwctl, egw-admin logging.
- Event Log integration (owner chose the file).
