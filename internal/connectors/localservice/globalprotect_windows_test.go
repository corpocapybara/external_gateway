//go:build windows

package localservice

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// The GlobalProtect check must agree with Windows on whether the PANGP virtual
// adapter is up. The PanGPS service runs whether or not the VPN is connected,
// so a service-based check reported "connected" with the adapter disabled.
func TestCheckGlobalProtectMatchesPANGPAdapter(t *testing.T) {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"@(Get-NetAdapter -InterfaceDescription 'PANGP Virtual Ethernet Adapter*' -ErrorAction SilentlyContinue | Where-Object Status -eq 'Up').Count").Output()
	if err != nil {
		t.Skipf("Get-NetAdapter unavailable: %v", err)
	}
	wantUp := strings.TrimSpace(string(out)) != "0"

	status, err := NewConnector().CheckTunnel(context.Background(), TunnelConfig{Type: TunnelTypeGlobalProtect, Name: "gp"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PANGP adapter up per Get-NetAdapter: %v; check: connected=%v (%s)", wantUp, status.Connected, status.Details)
	if status.Connected != wantUp {
		t.Errorf("connected = %v, want %v (PANGP adapter up per Get-NetAdapter)", status.Connected, wantUp)
	}
}
