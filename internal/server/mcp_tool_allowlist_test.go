package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
// TestMcpCallLiveOpsToolEnum checks the live files against this list (EG0076).
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

// mcpCallUnrestrictedMarker, a comment line in a live ops file, marks its mcp.call
// as unrestricted by the owner's decision, so the drift test skips that file. The
// exception lives in the gitignored file, not here: committed code must not name a
// live workspace (leakcheck, EG0047; owner 2026-11-28, EG0076).
const mcpCallUnrestrictedMarker = "# egw-drift: mcp.call unrestricted"

// hasUnrestrictedMarker reports the marker line (after leading spaces) and the
// rest of that line.
func hasUnrestrictedMarker(data []byte) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, mcpCallUnrestrictedMarker) {
			return line, true
		}
	}
	return "", false
}

// fakeRovo is an MCP server that records every JSON-RPC method it receives and
// the raw params of each tools/call.
type fakeRovo struct {
	srv     *httptest.Server
	mu      sync.Mutex
	methods []string
	calls   []json.RawMessage
}

func newFakeRovo(t *testing.T) *fakeRovo {
	t.Helper()
	fr := &fakeRovo{}
	fr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rpc struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &rpc)
		fr.mu.Lock()
		fr.methods = append(fr.methods, rpc.Method)
		if rpc.Method == "tools/call" {
			fr.calls = append(fr.calls, rpc.Params)
		}
		fr.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
		case "tools/call":
			w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`))
		case "tools/list":
			w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(fr.srv.Close)
	return fr
}

// seen returns the methods and tools/call params received so far.
func (fr *fakeRovo) seen() ([]string, []json.RawMessage) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return append([]string(nil), fr.methods...), append([]json.RawMessage(nil), fr.calls...)
}

func (fr *fakeRovo) reset() {
	fr.mu.Lock()
	fr.methods, fr.calls = nil, nil
	fr.mu.Unlock()
}

// toolNames returns params.name of each recorded tools/call.
func (fr *fakeRovo) toolNames() []string {
	_, calls := fr.seen()
	var names []string
	for _, c := range calls {
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(c, &p)
		names = append(names, p.Name)
	}
	return names
}

// newMcpFixture is an env fixture whose mcp site points at a fake Rovo server and
// whose mcp.call is the read-only op. Add more ops before the first call.
func newMcpFixture(t *testing.T) (*envFixture, *fakeRovo) {
	t.Helper()
	rovo := newFakeRovo(t)
	var op policy.Operation
	if err := yaml.Unmarshal([]byte(mcpCallReadOnlyYAML), &op); err != nil {
		t.Fatal(err)
	}
	op.ID = "mcp.call"
	f := newEnvFixture(t)
	f.sites["mcp"] = config.Site{Environments: map[string]config.Environment{"prod": {BaseURL: rovo.srv.URL}}}
	f.ops["mcp.call"] = &op
	return f, rovo
}

// An mcp.call whose `tool` param is an enum reaches the MCP server only for a
// listed tool; any other tool name is refused with 403 rule validation before the
// gateway sends anything upstream (not even the initialize handshake).
func TestMcpCallToolAllowlist(t *testing.T) {
	f, rovo := newMcpFixture(t)

	code, out := f.call(t, "mcp.call", map[string]interface{}{"tool": "getTeamworkGraphObject", "arguments": `{"ids":["x"]}`})
	if code/100 != 2 || out["isError"] != false {
		t.Fatalf("listed tool: status %d, body %v; want 2xx with isError false", code, out)
	}
	if called := rovo.toolNames(); len(called) != 1 || called[0] != "getTeamworkGraphObject" {
		t.Fatalf("tools/call names = %v, want [getTeamworkGraphObject]", called)
	}

	refused := []interface{}{"addTeamworkGraphContext", "AddTeamworkGraphContext", "getteamworkgraphobject", "", 1}
	for _, tool := range refused {
		rovo.reset()
		code, out := f.call(t, "mcp.call", map[string]interface{}{"tool": tool, "arguments": "{}"})
		if code != http.StatusForbidden || out["error"] != "denied" || out["rule"] != "validation" {
			t.Fatalf("tool %#v: status %d, body %v; want 403 rule validation", tool, code, out)
		}
		if _, isString := tool.(string); isString {
			if detail, _ := out["detail"].(string); !strings.Contains(detail, "getContentFormatGuide getTeamworkGraphContext getTeamworkGraphObject") {
				t.Fatalf("tool %#v: detail %q does not name the allowed tools", tool, detail)
			}
		}
		if methods, _ := rovo.seen(); len(methods) != 0 {
			t.Fatalf("tool %#v: upstream got %v, want no request", tool, methods)
		}
	}
}

// A dry run validates the tool enum like a real call: an unlisted tool is 403
// rule validation, a listed one is allowed, and neither reaches the MCP server.
func TestMcpCallDryRunToolAllowlist(t *testing.T) {
	f, rovo := newMcpFixture(t)

	code, out := f.send(t, "mcp.call", `{"tool":"addTeamworkGraphContext","arguments":"{}"}`, true)
	if code != http.StatusForbidden || out["error"] != "denied" || out["rule"] != "validation" {
		t.Fatalf("unlisted tool dry run: status %d, body %v; want 403 rule validation", code, out)
	}
	code, out = f.send(t, "mcp.call", `{"tool":"getTeamworkGraphObject","arguments":"{}"}`, true)
	if code != http.StatusOK || out["decision"] != "allow" {
		t.Fatalf("listed tool dry run: status %d, body %v; want 200 decision allow", code, out)
	}
	if methods, _ := rovo.seen(); len(methods) != 0 {
		t.Fatalf("dry runs sent %v upstream, want no request", methods)
	}
}

// Keys named name or tool inside `arguments` stay inside params.arguments: the
// tools/call name is always the validated `tool` param. A regression guard (the
// current code already holds it).
func TestMcpCallArgumentsCannotNameTool(t *testing.T) {
	f, rovo := newMcpFixture(t)

	args := `{"name":"addTeamworkGraphContext","tool":"addTeamworkGraphContext"}`
	code, out := f.call(t, "mcp.call", map[string]interface{}{"tool": "getTeamworkGraphObject", "arguments": args})
	if code/100 != 2 || out["status"] == "error" {
		t.Fatalf("status %d, body %v; want success", code, out)
	}
	_, calls := rovo.seen()
	if len(calls) != 1 {
		t.Fatalf("tools/call count = %d, want 1", len(calls))
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(calls[0], &params); err != nil {
		t.Fatalf("tools/call params %s: %v", calls[0], err)
	}
	if len(params) != 2 || string(params["name"]) != `"getTeamworkGraphObject"` || params["arguments"] == nil {
		t.Fatalf("tools/call params = %s, want exactly name getTeamworkGraphObject and arguments", calls[0])
	}
	var inner map[string]string
	if err := json.Unmarshal(params["arguments"], &inner); err != nil || inner["name"] != "addTeamworkGraphContext" || inner["tool"] != "addTeamworkGraphContext" {
		t.Fatalf("params.arguments = %s (%v), want the two keys kept inside it", params["arguments"], err)
	}
}

// Invalid arguments JSON is refused before the initialize handshake; an op on
// path tools/list in the same workspace still reaches tools/list.
func TestMcpCallInvalidArgumentsRefused(t *testing.T) {
	f, rovo := newMcpFixture(t)
	f.ops["mcp.tools"] = &policy.Operation{ID: "mcp.tools", Connector: "mcp", Site: "mcp", Path: "tools/list"}

	_, out := f.call(t, "mcp.call", map[string]interface{}{"tool": "getTeamworkGraphObject", "arguments": `{}, "name":"addTeamworkGraphContext"`})
	if errMsg, _ := out["error"].(string); out["status"] != "error" || !strings.Contains(errMsg, "invalid arguments JSON") {
		t.Fatalf("body %v; want status error naming invalid arguments JSON", out)
	}
	if methods, _ := rovo.seen(); len(methods) != 0 {
		t.Fatalf("invalid arguments sent %v upstream, want no request", methods)
	}

	_, out = f.call(t, "mcp.tools", map[string]interface{}{})
	if out["status"] == "error" {
		t.Fatalf("tools/list op: body %v; want success", out)
	}
	methods, _ := rovo.seen()
	if len(methods) == 0 || methods[len(methods)-1] != "tools/list" {
		t.Fatalf("tools/list op: upstream got %v, want it to end with tools/list", methods)
	}
}

// checkMcpCallToolEnum returns an error unless the ops file's mcp.call `tool`
// param is an enum of exactly the read-only tools in mcpCallReadOnlyYAML (order
// free; duplicates and a missing param are errors). A file without mcp.call
// passes. It keys on the op id mcp.call only.
func checkMcpCallToolEnum(opsYAML []byte) error {
	var doc struct {
		Operations map[string]yaml.Node `yaml:"operations"`
	}
	if err := yaml.Unmarshal(opsYAML, &doc); err != nil {
		return fmt.Errorf("parsing ops: %w", err)
	}
	node, ok := doc.Operations["mcp.call"]
	if !ok {
		return nil
	}
	var op policy.Operation
	if err := node.Decode(&op); err != nil {
		return fmt.Errorf("parsing mcp.call: %w", err)
	}
	var want policy.Operation
	if err := yaml.Unmarshal([]byte(mcpCallReadOnlyYAML), &want); err != nil {
		return fmt.Errorf("parsing mcpCallReadOnlyYAML: %w", err)
	}
	wantSet := map[string]bool{}
	for _, v := range want.Params["tool"].Values {
		wantSet[v] = true
	}
	tool, ok := op.Params["tool"]
	if !ok {
		return fmt.Errorf("mcp.call has no tool param")
	}
	if tool.Type != "enum" {
		return fmt.Errorf("mcp.call tool is type %q, want enum %v", tool.Type, want.Params["tool"].Values)
	}
	seen := map[string]bool{}
	for _, v := range tool.Values {
		if !wantSet[v] {
			return fmt.Errorf("mcp.call tool enum has %q, not a read-only tool", v)
		}
		if seen[v] {
			return fmt.Errorf("mcp.call tool enum lists %q twice", v)
		}
		seen[v] = true
	}
	if len(seen) != len(wantSet) {
		return fmt.Errorf("mcp.call tool enum %v, want exactly %v", tool.Values, want.Params["tool"].Values)
	}
	return nil
}

func TestMcpCallToolEnumCheck(t *testing.T) {
	ops := func(tool string) []byte {
		return []byte("operations:\n  jira.issue:\n    connector: http-rest\n  mcp.call:\n    connector: mcp\n    site: mcp\n    params:\n" + tool + "      arguments:\n        type: string\n")
	}
	enum := func(values ...string) string {
		s := "      tool:\n        type: enum\n        required: true\n        values:\n"
		for _, v := range values {
			s += "          - " + v + "\n"
		}
		return s
	}
	cases := []struct {
		name string
		yaml []byte
		ok   bool
	}{
		{"read-only enum", ops(enum("getTeamworkGraphObject", "getContentFormatGuide", "getTeamworkGraphContext")), true},
		{"no mcp.call", []byte("operations:\n  jira.issue:\n    connector: http-rest\n"), true},
		{"type string", ops("      tool:\n        type: string\n        required: true\n"), false},
		{"write tool added", ops(enum("getContentFormatGuide", "getTeamworkGraphContext", "getTeamworkGraphObject", "addTeamworkGraphContext")), false},
		{"value missing", ops(enum("getContentFormatGuide", "getTeamworkGraphObject")), false},
		{"duplicate value", ops(enum("getContentFormatGuide", "getContentFormatGuide", "getTeamworkGraphObject")), false},
		{"no tool param", ops(""), false},
	}
	for _, c := range cases {
		err := checkMcpCallToolEnum(c.yaml)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
	marked := append([]byte("operations:\n  # egw-drift: mcp.call unrestricted (owner 2026-11-28)\n"), ops("      tool:\n        type: string\n")[len("operations:\n"):]...)
	if _, ok := hasUnrestrictedMarker(marked); !ok {
		t.Error("marker line not found")
	}
	if _, ok := hasUnrestrictedMarker(ops(enum("getContentFormatGuide"))); ok {
		t.Error("marker found in a file without it")
	}
}

// The live (gitignored) ops files at the checkout root keep mcp.call's tool enum
// equal to the read-only list, except files carrying the owner's unrestricted
// marker (mcpCallUnrestrictedMarker).
func TestMcpCallLiveOpsToolEnum(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "operations.*.yaml"))
	if len(files) == 0 {
		t.Skip("no live operations.*.yaml at the checkout root")
	}
	for _, p := range files {
		name := filepath.Base(p)
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if marker, ok := hasUnrestrictedMarker(data); ok {
				t.Skipf("%s: owner-approved exception: %s", name, marker)
			}
			if err := checkMcpCallToolEnum(data); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}
