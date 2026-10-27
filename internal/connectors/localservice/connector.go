package localservice

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/external_gateway/internal/connectors"
)

type TunnelType string

const (
	TunnelTypeWireGuard     TunnelType = "wireguard"
	TunnelTypeGlobalProtect TunnelType = "globalprotect"
	TunnelTypeTCP           TunnelType = "tcp"
)

type TunnelConfig struct {
	Type      TunnelType
	Name      string
	CheckHost string
	CheckPort int
}

type TunnelStatus struct {
	Name      string     `json:"name"`
	Type      TunnelType `json:"type"`
	Connected bool       `json:"connected"`
	Details   string     `json:"details,omitempty"`
}

type Connector struct{}

func NewConnector() *Connector {
	return &Connector{}
}

func (c *Connector) Name() string {
	return "local-service"
}

func (c *Connector) Execute(ctx context.Context, req *connectors.Request) (*connectors.Response, error) {
	return nil, fmt.Errorf("use CheckTunnel instead")
}

func (c *Connector) CheckTunnel(ctx context.Context, cfg TunnelConfig) (*TunnelStatus, error) {
	switch cfg.Type {
	case TunnelTypeWireGuard:
		return c.checkWireGuard(ctx, cfg.Name)
	case TunnelTypeGlobalProtect:
		return c.checkGlobalProtect(ctx, cfg.Name)
	case TunnelTypeTCP:
		return c.checkTCP(ctx, cfg.CheckHost, cfg.CheckPort)
	default:
		return nil, fmt.Errorf("unknown tunnel type: %s", cfg.Type)
	}
}

func (c *Connector) checkWireGuard(ctx context.Context, tunnelName string) (*TunnelStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	svcName := fmt.Sprintf("WireGuardTunnel$%s", tunnelName)
	cmd := exec.CommandContext(ctx, "sc", "query", svcName)
	output, err := cmd.Output()
	if err != nil {
		return &TunnelStatus{
			Name:      tunnelName,
			Type:      TunnelTypeWireGuard,
			Connected: false,
			Details:   fmt.Sprintf("service query failed: %v", err),
		}, nil
	}

	outputStr := string(output)
	running := strings.Contains(outputStr, "RUNNING") && !strings.Contains(outputStr, "STOPPED")

	return &TunnelStatus{
		Name:      tunnelName,
		Type:      TunnelTypeWireGuard,
		Connected: running,
		Details:   fmt.Sprintf("service status: %s", func() string {
			if running { return "running" }
			return "stopped"
		}()),
	}, nil
}

// pangpAdapterPrefix starts the description of GlobalProtect's virtual adapter
// ("PANGP Virtual Ethernet Adapter Secure" on current clients).
const pangpAdapterPrefix = "PANGP Virtual Ethernet Adapter"

type netAdapter struct {
	Name        string
	Description string
	Up          bool
}

// checkGlobalProtect reports the VPN connected when GlobalProtect's virtual
// adapter is up. The PanGPS service is no signal: it runs while disconnected.
func (c *Connector) checkGlobalProtect(ctx context.Context, portal string) (*TunnelStatus, error) {
	adapters, err := netAdapters()
	if err != nil {
		return &TunnelStatus{
			Name:      portal,
			Type:      TunnelTypeGlobalProtect,
			Connected: false,
			Details:   fmt.Sprintf("listing network adapters failed: %v", err),
		}, nil
	}
	return globalProtectStatus(portal, adapters), nil
}

func globalProtectStatus(portal string, adapters []netAdapter) *TunnelStatus {
	status := &TunnelStatus{
		Name:    portal,
		Type:    TunnelTypeGlobalProtect,
		Details: "PANGP adapter not found (disabled, or GlobalProtect not installed)",
	}
	for _, a := range adapters {
		if !strings.HasPrefix(a.Description, pangpAdapterPrefix) {
			continue
		}
		if a.Up {
			status.Connected = true
			status.Details = fmt.Sprintf("PANGP adapter %q up, GlobalProtect connected", a.Name)
			return status
		}
		status.Details = fmt.Sprintf("PANGP adapter %q not up, GlobalProtect disconnected", a.Name)
	}
	return status
}

func (c *Connector) checkTCP(ctx context.Context, host string, port int) (*TunnelStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	target := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return &TunnelStatus{
			Name:      host,
			Type:      TunnelTypeTCP,
			Connected: false,
			Details:   fmt.Sprintf("connection failed: %v", err),
		}, nil
	}
	conn.Close()

	return &TunnelStatus{
		Name:      host,
		Type:      TunnelTypeTCP,
		Connected: true,
		Details:   fmt.Sprintf("port %d open", port),
	}, nil
}

func (c *Connector) WireGuardStatus(tunnelName string) (*TunnelStatus, error) {
	return c.CheckTunnel(context.Background(), TunnelConfig{
		Type: TunnelTypeWireGuard,
		Name: tunnelName,
	})
}

func (c *Connector) TestConnectivity(host string, port int) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := c.checkTCP(ctx, host, port)
	if err != nil {
		return false, err
	}
	return result.Connected, nil
}
