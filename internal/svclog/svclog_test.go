package svclog

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// No test here calls t.Parallel: they replace package seams.

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func openWriter(t *testing.T, path string, retention int, now func() time.Time, maxBytes int64) *Writer {
	t.Helper()
	w := New()
	w.Now, w.MaxBytes = now, maxBytes
	if err := w.Switch(path, retention); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

// EG0065: rotation renames to <base>_<UTC yyyymmdd-hhmmss>[_N].log and prunes only
// the own base's rotated files older than the retention days.
func TestServiceLogRotatesAndPrunes(t *testing.T) {
	now := time.Date(2026, 11, 21, 3, 0, 0, 0, time.FixedZone("plus5", 5*3600)) // 11-20 22:00Z
	clock := func() time.Time { return now }
	dir := t.TempDir()
	for _, n := range []string{
		"egw-acme_20260821-120000.log", "egw-acme_20260822-000000.log",
		"egw-acme2_20200101-000000.log", "audit_20200101.log",
	} {
		touch(t, dir, n)
	}
	path := filepath.Join(dir, "egw-acme.log")
	w := openWriter(t, path, 90, clock, 10)

	if exists(dir, "egw-acme_20260821-120000.log") {
		t.Error("own rotated file older than retention kept on open")
	}
	for _, n := range []string{"egw-acme_20260822-000000.log", "egw-acme2_20200101-000000.log", "audit_20200101.log"} {
		if !exists(dir, n) {
			t.Errorf("%s deleted, want kept (boundary day or another base)", n)
		}
	}

	for _, line := range []string{"first-line\n", "second-line\n", "third-line\n"} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{
		"egw-acme_20261120-220000.log":   "first-line\n",
		"egw-acme_20261120-220000_1.log": "second-line\n",
		"egw-acme.log":                   "third-line\n",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}

	// A failed rename keeps writing to the same file, data intact, and is not
	// retried within the minute.
	renames := 0
	orig := renameFile
	t.Cleanup(func() { renameFile = orig })
	renameFile = func(string, string) error { renames++; return errors.New("in use") }
	for _, line := range []string{"a-line\n", "b-line\n", "c-line\n"} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write after a failed rotation: %v", err)
		}
	}
	if renames != 1 {
		t.Errorf("rename attempts = %d, want 1 within the retry delay", renames)
	}
	if got, _ := os.ReadFile(path); string(got) != "third-line\na-line\nb-line\nc-line\n" {
		t.Errorf("current file after failed rotation = %q", got)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("late\n")); err == nil {
		t.Error("Write after Close succeeded, want an error")
	}
}

// EG0065: a Writer whose file failed to open drops writes with an error, and a
// later Switch to a good path takes over; retention 0 prunes nothing.
func TestSwitchAfterFailedOpen(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "regular")
	w := New()
	t.Cleanup(func() { w.Close() })
	if err := w.Switch(filepath.Join(dir, "regular", "x.log"), 0); err == nil {
		t.Fatal("Switch under a regular file succeeded")
	}
	if _, err := w.Write([]byte("lost\n")); err == nil {
		t.Error("Write with no file succeeded")
	}
	touch(t, dir, "good_20000101-000000.log")
	good := filepath.Join(dir, "good.log")
	if err := w.Switch(good, 0); err != nil {
		t.Fatal(err)
	}
	if !exists(dir, "good_20000101-000000.log") {
		t.Error("retention 0 pruned a rotated file")
	}
	if _, err := w.Write([]byte("kept\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Switch(good, 90); err != nil {
		t.Fatal(err)
	}
	if exists(dir, "good_20000101-000000.log") {
		t.Error("Switch to the same path with retention 90 did not prune")
	}
	if got, _ := os.ReadFile(good); string(got) != "kept\n" {
		t.Errorf("good.log = %q", got)
	}
}

// EG0065: concurrent writes lose and tear nothing across rotations.
func TestWriterConcurrentWrites(t *testing.T) {
	var mu sync.Mutex
	clock := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { // advances 1 s per call; called under the writer's mutex
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Second)
		return clock
	}
	dir := t.TempDir()
	w := openWriter(t, filepath.Join(dir, "egw-x.log"), 0, now, 4096) // 4000 lines of ~10 B: about 12 rotations
	const goroutines, lines = 20, 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < lines; i++ {
				if _, err := fmt.Fprintf(w, "g%d-%d\n", g, i); err != nil {
					t.Error(err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	w.Close()

	lineRe := regexp.MustCompile(`^g([0-9]+)-([0-9]+)$`)
	seen := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l := sc.Text()
			if !lineRe.MatchString(l) {
				t.Errorf("torn line %q in %s", l, e.Name())
			}
			if seen[l] {
				t.Errorf("duplicate line %q", l)
			}
			seen[l] = true
		}
		f.Close()
	}
	if len(seen) != goroutines*lines {
		t.Errorf("lines = %d, want %d", len(seen), goroutines*lines)
	}
	if len(entries) < 2 || len(entries) > 50 {
		t.Errorf("files = %d, want rotations to have happened (2..50)", len(entries))
	}
	if !strings.HasPrefix(entries[0].Name(), "egw-x") {
		t.Errorf("unexpected file %s", entries[0].Name())
	}
}
