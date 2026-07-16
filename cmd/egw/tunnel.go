package main

import (
	"context"
	"fmt"

	"github.com/external_gateway/internal/config"
	ls "github.com/external_gateway/internal/connectors/localservice"
	"github.com/external_gateway/internal/policy"
)

type gatewayTunnelChecker struct {
	cfg    *config.Config
	locals *ls.Connector
}

func (c *gatewayTunnelChecker) CheckTunnel(ctx context.Context, tunnelName string) (bool, string, error) {
	tunnel := c.cfg.GetTunnel(tunnelName)
	if tunnel == nil {
		return false, "", fmt.Errorf("unknown tunnel: %s", tunnelName)
	}

	var t ls.TunnelType
	switch tunnel.Type {
	case "wireguard":
		t = ls.TunnelTypeWireGuard
	case "globalprotect":
		t = ls.TunnelTypeGlobalProtect
	case "tcp":
		t = ls.TunnelTypeTCP
	default:
		return false, "", fmt.Errorf("unsupported tunnel type: %s", tunnel.Type)
	}

	status, err := c.locals.CheckTunnel(ctx, ls.TunnelConfig{
		Type:      t,
		Name:      tunnel.Tunnel,
		CheckHost: tunnel.CheckHost,
		CheckPort: tunnel.CheckPort,
	})
	if err != nil {
		return false, "", err
	}

	return status.Connected, status.Details, nil
}

func newTunnelChecker(cfg *config.Config) policy.TunnelChecker {
	return &gatewayTunnelChecker{
		cfg:    cfg,
		locals: ls.NewConnector(),
	}
}
