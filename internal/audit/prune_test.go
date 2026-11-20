package audit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// EG0065: Prune deletes only audit_<date>[_n].log names dated before UTC midnight
// minus the retention days; the boundary day and every other name stay.
func TestPruneAuditFiles(t *testing.T) {
	deleted := []string{"audit_20260821.log", "audit_20260821_3.log"}
	kept := []string{
		"audit_20260822.log", "audit_20261120.log",
		"audit_20261399.log", "audit_2026112.log", "audit_20261120_x.log",
		"feedback.log", "notes.txt", "egw-acme_20200101-000000.log",
	}
	for _, now := range []time.Time{
		time.Date(2026, 11, 20, 0, 0, 1, 0, time.UTC),
		time.Date(2026, 11, 20, 23, 59, 59, 0, time.UTC),
		time.Date(2026, 11, 21, 3, 0, 0, 0, time.FixedZone("plus5", 5*3600)), // 11-20 22:00Z
	} {
		dir := t.TempDir()
		for _, n := range append(append([]string{}, deleted...), kept...) {
			touch(t, dir, n)
		}
		if err := os.Mkdir(filepath.Join(dir, "audit_20200101.log"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := Prune(dir, 90, now); err != nil {
			t.Fatalf("%v: Prune: %v", now, err)
		}
		for _, n := range deleted {
			if exists(dir, n) {
				t.Errorf("%v: %s kept, want deleted", now, n)
			}
		}
		for _, n := range append(kept, "audit_20200101.log") {
			if !exists(dir, n) {
				t.Errorf("%v: %s deleted, want kept", now, n)
			}
		}
	}

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	touch(t, dir, "audit_20000101.log")
	if err := Prune(dir, 0, now); err != nil || !exists(dir, "audit_20000101.log") {
		t.Errorf("retention 0: err=%v, file kept=%v; want nil, true", err, exists(dir, "audit_20000101.log"))
	}
	if err := Prune(filepath.Join(dir, "missing"), 90, now); err != nil {
		t.Errorf("missing dir: %v, want nil", err)
	}

	// Remove failures: a sharing violation is returned, a file already gone is not,
	// and the rest of the dir is still pruned.
	fake := errors.New("sharing violation")
	orig := removeFile
	t.Cleanup(func() { removeFile = orig })
	removeFile = func(p string) error {
		switch filepath.Base(p) {
		case "audit_20000101.log":
			return fake
		case "audit_20000102.log":
			return fs.ErrNotExist
		}
		return os.Remove(p)
	}
	touch(t, dir, "audit_20000102.log")
	touch(t, dir, "audit_20000103.log")
	err := Prune(dir, 90, now)
	if !errors.Is(err, fake) || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Prune error = %v, want only the fake error", err)
	}
	if exists(dir, "audit_20000103.log") {
		t.Error("audit_20000103.log kept after another remove failed, want deleted")
	}
}

// EG0065: the Logger prunes at construction and on the first record of a new UTC
// day, never on a size rotation within the day.
func TestLogPrunesOnDayRollover(t *testing.T) {
	day1 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	clock := day1
	dir := t.TempDir()
	touch(t, dir, "audit_20260801.log")
	l, err := newLogger(dir, 1, 90, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if exists(dir, "audit_20260801.log") {
		t.Error("file older than retention survived construction")
	}
	if l.PruneErr() != nil {
		t.Errorf("PruneErr = %v, want nil", l.PruneErr())
	}

	touch(t, dir, "audit_20260802.log")
	l.maxSize = 1 // every record after the first rotates
	for i := 0; i < 3; i++ {
		if err := l.Log(&Record{Decision: "allow"}); err != nil {
			t.Fatal(err)
		}
	}
	if !exists(dir, "audit_20261120_1.log") {
		t.Fatal("size rotation did not happen; the no-prune check would be vacuous")
	}
	if !exists(dir, "audit_20260802.log") {
		t.Error("pruned within the same day (on a record or a size rotation)")
	}

	clock = day1.Add(24 * time.Hour)
	if err := l.Log(&Record{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	if exists(dir, "audit_20260802.log") {
		t.Error("first record of the next day did not prune")
	}

	fake := errors.New("locked")
	orig := removeFile
	t.Cleanup(func() { removeFile = orig })
	removeFile = func(string) error { return fake }
	touch(t, dir, "audit_20260803.log")
	clock = clock.Add(24 * time.Hour)
	if err := l.Log(&Record{Decision: "allow"}); err != nil {
		t.Fatalf("Log failed on a prune error: %v", err)
	}
	if !errors.Is(l.PruneErr(), fake) {
		t.Errorf("PruneErr = %v, want the remove error", l.PruneErr())
	}
}
