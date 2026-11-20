// Package svclog is the size-rotated file behind egw's operational (zerolog) log
// when it runs under the SCM, where stderr goes nowhere (EG0065).
package svclog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultMaxBytes is the size at which the log file is rotated.
const DefaultMaxBytes = 10 * 1024 * 1024

// retryDelay is how long a failed rotation or reopen waits before the next try,
// so a stuck file is not retried on every log line.
const retryDelay = time.Minute

// maxRetentionDays bounds the cutoff arithmetic; larger values behave like it.
const maxRetentionDays = 36500

// errNoFile is returned by Write while no file is open.
var errNoFile = errors.New("svclog: no log file open")

// ErrPrune wraps a prune error Switch returns after it switched successfully.
var ErrPrune = errors.New("svclog: prune")

// Test seams.
var (
	removeFile = os.Remove
	renameFile = os.Rename
)

// Writer appends to one log file, rotating it by size. It is safe for
// concurrent use; the zero value is not usable, call New.
type Writer struct {
	mu        sync.Mutex
	f         *os.File
	path      string
	size      int64
	retention int
	retryAt   time.Time // no rotation or reopen attempt before this time

	MaxBytes int64            // rotation size; DefaultMaxBytes from New
	Now      func() time.Time // clock; time.Now from New
}

// New returns a Writer with no file open; Switch opens one.
func New() *Writer {
	return &Writer{MaxBytes: DefaultMaxBytes, Now: time.Now}
}

// Switch makes path the log file and retention (days, 0 keeps all) the prune
// rule, then prunes. For the path already open it only sets the retention. When
// the new path cannot be opened the current file (or none) stays and the error
// is returned. A prune error after a successful switch wraps ErrPrune.
func (w *Writer) Switch(path string, retention int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil && samePath(w.path, path) {
		w.retention = retention
		return pruneErr(Prune(w.path, retention, w.Now()))
	}
	f, size, err := openAppend(path)
	if err != nil {
		return err
	}
	if w.f != nil {
		w.f.Close()
	}
	w.f, w.path, w.size, w.retention, w.retryAt = f, path, size, retention, time.Time{}
	return pruneErr(Prune(path, retention, w.Now()))
}

func pruneErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrPrune, err)
}

// Write appends p, rotating first when p would take the file past MaxBytes.
// A single write larger than MaxBytes goes whole into a fresh file.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if w.path == "" || w.Now().Before(w.retryAt) {
			return 0, errNoFile
		}
		f, size, err := openAppend(w.path)
		if err != nil {
			w.retryAt = w.Now().Add(retryDelay)
			return 0, errNoFile
		}
		w.f, w.size = f, size
	}
	if w.size > 0 && w.size+int64(len(p)) > w.MaxBytes && !w.Now().Before(w.retryAt) {
		w.rotate()
		if w.f == nil {
			return 0, errNoFile
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate renames the full file to <base>_<UTC yyyymmdd-hhmmss>[_N].log and starts
// a new one. The file is closed first: Windows cannot rename an open file. On
// failure the old file is reopened and rotation waits retryDelay. w.mu is held.
func (w *Writer) rotate() {
	w.f.Close()
	w.f = nil
	now := w.Now()
	target := rotatedName(w.path, now)
	rotated := target != "" && renameFile(w.path, target) == nil
	f, size, err := openAppend(w.path)
	if err != nil {
		w.retryAt = now.Add(retryDelay)
		return
	}
	w.f, w.size = f, size
	if !rotated {
		w.retryAt = now.Add(retryDelay)
		return
	}
	// A prune error has no logger to go to here; the next rotation or Switch retries.
	_ = Prune(w.path, w.retention, now)
}

// rotatedName returns the first free rotated name for path at now, "" when
// _1.._99 are all taken too.
func rotatedName(path string, now time.Time) string {
	dir, base := filepath.Dir(path), baseName(path)
	stamp := now.UTC().Format("20060102-150405")
	for i := 0; i < 100; i++ {
		name := base + "_" + stamp + ".log"
		if i > 0 {
			name = fmt.Sprintf("%s_%s_%d.log", base, stamp, i)
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate
		}
	}
	return ""
}

// Close closes the file; a later Write returns an error.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	if w.f != nil {
		err = w.f.Close()
	}
	w.f, w.path = nil, ""
	return err
}

// Prune deletes the rotated files of path (<base>_<yyyymmdd-hhmmss>[_N].log in
// its dir, own base only) whose name date is before UTC midnight of now minus
// days. days <= 0 keeps everything; a file already gone is not an error; other
// remove errors are collected and the walk goes on. Same rule as audit.Prune.
func Prune(path string, days int, now time.Time) error {
	if days <= 0 {
		return nil
	}
	if days > maxRetentionDays {
		days = maxRetentionDays
	}
	u := now.UTC()
	cutoff := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -days)
	dir := filepath.Dir(path)
	re := regexp.MustCompile("^" + regexp.QuoteMeta(baseName(path)) + `_([0-9]{8})-[0-9]{6}(_[0-9]+)?\.log$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		date, err := time.ParseInLocation("20060102", m[1], time.UTC)
		if err != nil || !date.Before(cutoff) {
			continue
		}
		if err := removeFile(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// baseName is the file name without its extension (egw-acme.log -> egw-acme).
func baseName(path string) string {
	name := filepath.Base(path)
	if ext := filepath.Ext(name); ext != "" && ext != name {
		return strings.TrimSuffix(name, ext)
	}
	return name
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func openAppend(path string) (*os.File, int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}
