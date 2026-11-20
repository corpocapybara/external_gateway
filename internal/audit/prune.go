package audit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// auditName matches the files Logger.Log writes: audit_<yyyymmdd>.log and its
// size-rotated audit_<yyyymmdd>_<n>.log.
var auditName = regexp.MustCompile(`^audit_([0-9]{8})(_[0-9]+)?\.log$`)

// removeFile is os.Remove; tests replace it to inject failures.
var removeFile = os.Remove

// maxRetentionDays bounds the cutoff arithmetic; larger values behave like it.
const maxRetentionDays = 36500

// RetentionCutoff returns the first UTC day that is kept: UTC midnight of now
// minus days. A file whose name date is before it has expired.
func RetentionCutoff(now time.Time, days int) time.Time {
	if days > maxRetentionDays {
		days = maxRetentionDays
	}
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -days)
}

// Prune deletes the audit files in dir whose name date is before
// RetentionCutoff(now, days) (EG0065). days <= 0 keeps everything. Names that do
// not match the audit pattern, or carry an impossible date, are kept. A missing
// dir or a file removed meanwhile (another service sharing the dir) is not an
// error; any other remove error is collected and the walk goes on.
func Prune(dir string, days int, now time.Time) error {
	if days <= 0 {
		return nil
	}
	cutoff := RetentionCutoff(now, days)
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
		m := auditName.FindStringSubmatch(e.Name())
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
