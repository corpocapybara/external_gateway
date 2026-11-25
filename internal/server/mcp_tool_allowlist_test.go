package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"gopkg.in/yaml.v3"
)

// mcpCallReadOnlyYAML mirrors the live workspace's mcp.call block (the ops file is
// gitignored): `tool` is an enum of the read-only Rovo MCP tools, so a write tool
// such as addTeamworkGraphContext is refused by param validation (EG0075).
const mcpCallReadOnlyYAML = `
connector: mcp
site: mcp
params:
  tool:
    type: enum
    required: true
    values:
      - getContentFormatGuide
      - getTeamworkGraphContext
      - getTeamworkGraphObject
  arguments:
    type: string
response:
  allow_fields:
    - content[].type
    - content[].text
    - isError
`

// An mcp.call whose `tool` param is an enum reaches the MCP server only for a
// listed tool; any other tool name is refused with 403 rule validation before the
// gateway sends anything upstream (not even the initialize handshake).
func TestMcpCallToolAllowlist(t *testing.T) {
	var mu sync.Mutex
	var methods, called []string
	rovo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &rpc)
		mu.Lock()
		methods = append(methods, rpc.Method)
		if rpc.Method == "tools/call" {
			called = append(called, rpc.Params.Name)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
		case "tools/call":
			w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`))
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer rovo.Close()

	var op policy.Operation
	if err := yaml.Unmarshal([]byte(mcpCallReadOnlyYAML), &op); err != nil {
		t.Fatal(err)
	}
	op.ID = "mcp.call"
	f := newEnvFixture(t)
	f.sites["mcp"] = config.Site{Environments: map[string]config.Environment{"prod": {BaseURL: rovo.URL}}}
	f.ops["mcp.call"] = &op

	code, out := f.call(t, "mcp.call", map[string]interface{}{"tool": "getTeamworkGraphObject", "arguments": `{"ids":["x"]}`})
	if code/100 != 2 || out["isError"] != false {
		t.Fatalf("listed tool: status %d, body %v; want 2xx with isError false", code, out)
	}
	if len(called) != 1 || called[0] != "getTeamworkGraphObject" {
		t.Fatalf("tools/call names = %v, want [getTeamworkGraphObject]", called)
	}

	refused := []interface{}{"addTeamworkGraphContext", "AddTeamworkGraphContext", "getteamworkgraphobject", "", 1}
	for _, tool := range refused {
		mu.Lock()
		methods = nil
		mu.Unlock()
		code, out := f.call(t, "mcp.call", map[string]interface{}{"tool": tool, "arguments": "{}"})
		if code != http.StatusForbidden || out["error"] != "denied" || out["rule"] != "validation" {
			t.Fatalf("tool %#v: status %d, body %v; want 403 rule validation", tool, code, out)
		}
		if _, isString := tool.(string); isString {
			if detail, _ := out["detail"].(string); !strings.Contains(detail, "getContentFormatGuide getTeamworkGraphContext getTeamworkGraphObject") {
				t.Fatalf("tool %#v: detail %q does not name the allowed tools", tool, detail)
			}
		}
		if len(methods) != 0 {
			t.Fatalf("tool %#v: upstream got %v, want no request", tool, methods)
		}
	}
}
