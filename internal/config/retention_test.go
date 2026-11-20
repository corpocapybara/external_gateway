package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EG0065: audit.retention_days is optional (unset = 90, 0 keeps all, negative refused)
// and log.file overrides the service log path.
func TestAuditRetentionDays(t *testing.T) {
	load := func(extra string) (*Config, error) {
		t.Helper()
		f := filepath.Join(t.TempDir(), "config.yaml")
		y := "agents:\n  - id: bot\n    token_sha256: sha\naudit:\n  dir: x\n" + extra
		if err := os.WriteFile(f, []byte(y), 0644); err != nil {
			t.Fatal(err)
		}
		return Load(f)
	}
	for _, tc := range []struct {
		extra string
		want  int
	}{
		{"", 90},
		{"  retention_days: 0\n", 0},
		{"  retention_days: 30\n", 30},
	} {
		cfg, err := load(tc.extra)
		if err != nil {
			t.Fatalf("%q: %v", tc.extra, err)
		}
		if got := cfg.Audit.Retention(); got != tc.want {
			t.Errorf("%q: Retention() = %d, want %d", tc.extra, got, tc.want)
		}
	}
	if _, err := load("  retention_days: -1\n"); err == nil || !strings.Contains(err.Error(), "audit.retention_days") {
		t.Errorf("negative retention: err = %v, want one naming audit.retention_days", err)
	}
	cfg, err := load("log:\n  file: C:/logs/egw.log\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.File != "C:/logs/egw.log" {
		t.Errorf("log.file = %q", cfg.Log.File)
	}
	if cfg, _ := load(""); cfg.Log.File != "" {
		t.Errorf("unset log.file = %q, want empty", cfg.Log.File)
	}
}
