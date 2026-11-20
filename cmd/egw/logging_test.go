package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EG0065: the service log path is empty for interactive runs, log.file when
// set, else <ProgramData>/external_gateway/logs/<svc-name>.log.
func TestServiceLogPath(t *testing.T) {
	t.Setenv("ProgramData", `D:\PD`)
	if got := serviceLogPath(true, "egw-acme", `E:\x.log`); got != "" {
		t.Errorf("interactive = %q, want empty", got)
	}
	if got := serviceLogPath(false, "egw-acme", `E:\x.log`); got != `E:\x.log` {
		t.Errorf("override = %q", got)
	}
	if got, want := serviceLogPath(false, "egw-acme", ""), filepath.Join(`D:\PD`, "external_gateway", "logs", "egw-acme.log"); got != want {
		t.Errorf("default = %q, want %q", got, want)
	}
	t.Setenv("ProgramData", "")
	if got, want := serviceLogPath(false, "egw-acme", ""), filepath.Join(`C:\ProgramData`, "external_gateway", "logs", "egw-acme.log"); got != want {
		t.Errorf("ProgramData unset = %q, want %q", got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("invalid handle") }

// EG0065: the logger tees stderr and the file (a failing stderr does not stop
// the file), and an unopenable file leaves a working stderr logger with a
// warning naming the path.
func TestNewLoggerTeesAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egw-acme.log")
	touch := filepath.Join(dir, "egw-acme_20000101-000000.log")
	if err := os.WriteFile(touch, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger, w := newLogger(path, failingWriter{})
	t.Cleanup(func() { w.Close() })
	logger.Info().Msg("starting gateway")
	got, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(got), "starting gateway") {
		t.Errorf("file = %q, %v; want the line despite a failing stderr", got, err)
	}
	if _, err := os.Stat(touch); err != nil {
		t.Error("the pre-config open pruned a rotated file")
	}

	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(regular, "x.log")
	var stderr bytes.Buffer
	logger2, w2 := newLogger(bad, &stderr)
	t.Cleanup(func() { w2.Close() })
	var warn struct{ Level, Message, Path string }
	if err := json.Unmarshal(bytes.SplitN(stderr.Bytes(), []byte("\n"), 2)[0], &warn); err != nil {
		t.Fatalf("stderr %q: %v", stderr.String(), err)
	}
	if warn.Level != "warn" || warn.Message != "service log file unavailable" || warn.Path != bad {
		t.Errorf("warning = %+v, want warn / service log file unavailable / %s", warn, bad)
	}
	if _, err := w2.Write([]byte("x\n")); err == nil {
		t.Error("write with no file open succeeded")
	}

	// A failed default does not block log.file opened after config load.
	good := filepath.Join(dir, "configured.log")
	if err := w2.Switch(good, 90); err != nil {
		t.Fatal(err)
	}
	logger2.Info().Msg("after switch")
	if got, _ := os.ReadFile(good); !strings.Contains(string(got), "after switch") {
		t.Errorf("configured.log = %q, want the line", got)
	}

	if l, w := newLogger("", &stderr); w != nil {
		t.Error("empty path gave a writer")
	} else {
		stderr.Reset()
		l.Info().Msg("console")
		if !strings.Contains(stderr.String(), "console") {
			t.Errorf("stderr-only logger wrote %q", stderr.String())
		}
	}
}
