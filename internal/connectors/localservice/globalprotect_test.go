package localservice

import "testing"

func TestGlobalProtectStatus(t *testing.T) {
	pangp := func(up bool) netAdapter {
		return netAdapter{Name: "Ethernet 3", Description: "PANGP Virtual Ethernet Adapter Secure", Up: up}
	}
	wifi := netAdapter{Name: "Wi-Fi", Description: "Intel(R) Wi-Fi 6E AX211 160MHz", Up: true}
	cases := []struct {
		name        string
		adapters    []netAdapter
		want        bool
		wantDetails string
	}{
		{"no PANGP adapter", []netAdapter{wifi}, false, "PANGP adapter not found (disabled, or GlobalProtect not installed)"},
		{"PANGP adapter down", []netAdapter{wifi, pangp(false)}, false, `PANGP adapter "Ethernet 3" not up, GlobalProtect disconnected`},
		{"PANGP adapter up", []netAdapter{wifi, pangp(true)}, true, `PANGP adapter "Ethernet 3" up, GlobalProtect connected`},
		{"down PANGP adapter listed before an up one", []netAdapter{pangp(false), {Name: "Ethernet 4", Description: "PANGP Virtual Ethernet Adapter #2", Up: true}}, true, `PANGP adapter "Ethernet 4" up, GlobalProtect connected`},
		{"older client description", []netAdapter{{Name: "Ethernet 2", Description: "PANGP Virtual Ethernet Adapter", Up: true}}, true, `PANGP adapter "Ethernet 2" up, GlobalProtect connected`},
	}
	for _, c := range cases {
		got := globalProtectStatus("gp", c.adapters)
		if got.Connected != c.want || got.Details != c.wantDetails || got.Type != TunnelTypeGlobalProtect || got.Name != "gp" {
			t.Errorf("%s: got %+v, want connected=%v details=%q", c.name, got, c.want, c.wantDetails)
		}
	}
}
