package server

import (
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// localServiceOps declares a shaped and an unshaped (_raw) op per method.
const localServiceOps = `operations:
  wireguard.test:
    connector: local-service
    site: wireguard
    method: TEST
    params: &testParams
      host:
        type: string
        required: true
      port:
        type: int
        required: true
    response:
      allow_fields: [reachable, error]
  wireguard.test_raw:
    connector: local-service
    site: wireguard
    method: TEST
    params: *testParams
  wireguard.status:
    connector: local-service
    site: wireguard
    method: STATUS
    params: &statusParams
      tunnel:
        type: string
        default: kko
    response:
      allow_fields: [tunnel, type, connected]
  wireguard.status_raw:
    connector: local-service
    site: wireguard
    method: STATUS
    params: *statusParams
`

// localServiceHarness serves localServiceOps with a wireguard site and no tunnels.
func localServiceHarness(t *testing.T) *credHarness {
	t.Helper()
	return newCredHarness(t, map[string]string{"w": "      wireguard:\n        environments:\n          kko:\n            tunnel: kko\n"},
		[]string{"wireguard.test", "wireguard.test_raw", "wireguard.status", "wireguard.status_raw"}, localServiceOps)
}

// listenPort returns a listening 127.0.0.1 port, closed at cleanup.
func listenPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// TEST reads an int port (float64 from JSON), needs no tunnels entry, and its
// result is shaped by allow_fields (EG0051).
func TestLocalServiceTestAllowFields(t *testing.T) {
	h := localServiceHarness(t)
	open := listenPort(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	body := func(port int) string { return `{"host":"127.0.0.1","port":` + strconv.Itoa(port) + `}` }

	if got := emailOp(t, h, "wireguard.test", body(open)); !reflect.DeepEqual(got, map[string]interface{}{"reachable": true}) {
		t.Errorf("shaped, reachable: %v", got)
	}
	want := map[string]interface{}{"host": "127.0.0.1", "port": float64(open), "reachable": true}
	if got := emailOp(t, h, "wireguard.test_raw", body(open)); !reflect.DeepEqual(got, want) {
		t.Errorf("unshaped: %v, want %v", got, want)
	}
	got := emailOp(t, h, "wireguard.test", body(closed))
	if msg, _ := got["error"].(string); len(got) != 2 || got["reachable"] != false || msg == "" {
		t.Errorf("shaped, closed port: %v, want only reachable false and a non-empty error", got)
	}

	op := &policy.Operation{ID: "wireguard.test", Connector: "local-service", Site: "wireguard", Method: "TEST"}
	ws := &config.Workspace{}
	out, err := h.s.executeLocalServiceConnector(ws, op, map[string]interface{}{"host": "127.0.0.1", "port": strconv.Itoa(open)})
	if err != nil || !strings.Contains(string(out), `"reachable":true`) {
		t.Errorf("string port: %s, %v", out, err)
	}
	if _, err := h.s.executeLocalServiceConnector(ws, op, map[string]interface{}{"host": "127.0.0.1"}); err == nil || err.Error() != "host and port required" {
		t.Errorf("no port: err = %v", err)
	}
}

// STATUS output is shaped by allow_fields; the service query is stubbed.
func TestLocalServiceStatusAllowFields(t *testing.T) {
	h := localServiceHarness(t)
	h.s.cfg().Tunnels = map[string]config.Tunnel{"kko": {Type: "wireguard", Tunnel: "kko"}}
	orig := queryService
	t.Cleanup(func() { queryService = orig })
	var queried string
	queryService = func(name string) ([]byte, error) {
		queried = name
		return []byte("SERVICE_NAME: " + name + "\n        STATE              : 4  RUNNING\n"), nil
	}

	got := emailOp(t, h, "wireguard.status", `{"tunnel":"kko"}`)
	if want := map[string]interface{}{"connected": true, "tunnel": "kko", "type": "wireguard"}; !reflect.DeepEqual(got, want) {
		t.Errorf("shaped: %v, want %v", got, want)
	}
	if queried != "WireGuardTunnel$kko" {
		t.Errorf("queried %q", queried)
	}
	raw := keys(emailOp(t, h, "wireguard.status_raw", `{"tunnel":"kko"}`))
	sort.Strings(raw)
	if ks := strings.Join(raw, ","); ks != "connected,details,tunnel,type" {
		t.Errorf("unshaped keys = %s", ks)
	}
}

// The shipped example wireguard ops list only fields the connector emits.
func TestExampleWireguardAllowFields(t *testing.T) {
	for _, f := range []string{"../../config.example/operations.globex.yaml", "../../config.example/operations.yaml"} {
		reg, err := policy.NewRegistry(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for id, want := range map[string][]string{
			"wireguard.status": {"tunnel", "type", "connected"},
			"wireguard.test":   {"reachable", "error"},
		} {
			op, ok := reg.Get(id)
			if !ok {
				t.Fatalf("%s: no %s", f, id)
			}
			if !reflect.DeepEqual(op.Response.AllowFields, want) {
				t.Errorf("%s %s allow_fields = %v, want %v", f, id, op.Response.AllowFields, want)
			}
		}
	}
}
