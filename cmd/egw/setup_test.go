package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/rs/zerolog"
)

// TestSetupListensOnLoopbackOnly: the gateway listens on 127.0.0.1 only, never all interfaces (EG0054 R6).
func TestSetupListensOnLoopbackOnly(t *testing.T) {
	// Not t.TempDir: the audit logger keeps its file open, which would fail TempDir's cleanup on Windows.
	auditDir, err := os.MkdirTemp("", "egw-setup-audit-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(auditDir) }) // best effort
	cfg := &config.Config{Audit: config.AuditConfig{Dir: auditDir, MaxSizeMB: 1}}
	_, ln, httpSrv := setup(cfg, filepath.Join("..", "..", "config.example", "operations.yaml"), 0, zerolog.Nop())
	ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listener on %v, want 127.0.0.1", ln.Addr())
	}
	if !strings.HasPrefix(httpSrv.Addr, "127.0.0.1:") {
		t.Fatalf("httpSrv.Addr %q, want 127.0.0.1:<port>", httpSrv.Addr)
	}
}
