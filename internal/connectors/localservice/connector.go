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

func (c *Connector) checkGlobalProtect(ctx context.Context, portal string) (*TunnelStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sc", "query", "PanGPS")
	output, err := cmd.Output()
	if err != nil {
		return &TunnelStatus{
			Name:      portal,
			Type:      TunnelTypeGlobalProtect,
			Connected: false,
			Details:   fmt.Sprintf("PanGPS service not found: %v", err),
		}, nil
	}

	outputStr := string(output)
	connected := strings.Contains(outputStr, "RUNNING") && !strings.Contains(outputStr, "STOPPED")

	details := "service stopped"
	if connected {
		details = "PanGPS running, GlobalProtect connected"
	}

	return &TunnelStatus{
		Name:      portal,
		Type:      TunnelTypeGlobalProtect,
		Connected: connected,
		Details:   details,
	}, nil
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
