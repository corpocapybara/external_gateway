package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadIgnoresRetiredServerKeys: listen_addr, tls_cert and tls_key were
// removed (never read, EG0055); deployed configs that still carry them load.
func TestLoadIgnoresRetiredServerKeys(t *testing.T) {
	y := "server:\n  listen_addr: \"127.0.0.1\"\n  port: 8555\n  tls_cert: 'C:\\certs\\a.crt'\n  tls_key: 'C:\\certs\\a.key'\n" +
		"agents:\n  - id: bot\n    token_sha256: sha\n"
	f := filepath.Join(t.TempDir(), "config.x.yaml")
	if err := os.WriteFile(f, []byte(y), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("load with retired keys: %v", err)
	}
	if cfg.Server.Port != 8555 {
		t.Errorf("port = %d, want 8555", cfg.Server.Port)
	}
}
