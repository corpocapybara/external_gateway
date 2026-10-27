package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

type fakeTunnelChecker map[string]bool

func (f fakeTunnelChecker) CheckTunnel(ctx context.Context, name string) (bool, string, error) {
	return f[name], "fake", nil
}

// envFixture is a workspace whose jira site has a cloud env and a VPN-only env,
// mirroring config.thp.yaml, with one upstream per env counting its hits.
type envFixture struct {
	srv       *Server
	prod, vpn *atomic.Int32
	prodURL   string
	vpnURL    string
	sites     map[string]config.Site
	ops       map[string]*policy.Operation
	tunnelsUp fakeTunnelChecker
}

func newEnvFixture(t *testing.T) *envFixture {
	t.Helper()
	if err := audit.Init(t.TempDir(), 1); err != nil {
		t.Fatal(err)
	}
	f := &envFixture{prod: new(atomic.Int32), vpn: new(atomic.Int32), tunnelsUp: fakeTunnelChecker{}}
	upstream := func(hits *atomic.Int32) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Write([]byte(`{"comments":[]}`))
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	f.prodURL, f.vpnURL = upstream(f.prod), upstream(f.vpn)
	f.sites = map[string]config.Site{
		"jira": {Environments: map[string]config.Environment{
			"prod":  {BaseURL: f.prodURL},
			"corpo": {BaseURL: f.vpnURL, RequiresTunnel: "thp-vpn"},
		}},
		"corpjira": {Environments: map[string]config.Environment{
			"prod": {BaseURL: f.vpnURL, RequiresTunnel: "thp-vpn"},
		}},
	}
	f.ops = map[string]*policy.Operation{}
	return f
}

func (f *envFixture) addOp(id, site string, params map[string]policy.ParamDef) {
	if params == nil {
		params = map[string]policy.ParamDef{}
	}
	params["key"] = policy.ParamDef{Type: "string", Required: true}
	f.ops[id] = &policy.Operation{ID: id, Connector: "http-rest", Site: site, Method: "GET", Path: "/rest/api/3/issue/{key}/comment", Params: params}
}

// call runs the op through executeOp, the full request pipeline, and returns
// the HTTP status and decoded body.
func (f *envFixture) call(t *testing.T, opID string, params map[string]interface{}) (int, map[string]interface{}) {
	t.Helper()
	body, _ := json.Marshal(params)
	return f.send(t, opID, string(body), false)
}

// send posts a raw request body; dryRun goes through handleDryRun instead.
func (f *envFixture) send(t *testing.T, opID, body string, dryRun bool) (int, map[string]interface{}) {
	t.Helper()
	if f.srv == nil {
		var opIDs []string
		for id := range f.ops {
			opIDs = append(opIDs, id)
		}
		cfg := &config.Config{
			Workspaces: map[string]config.Workspace{"thp": {Sites: f.sites}},
			Roles:      map[string]config.Role{"dev": {Operations: opIDs}},
			Agents:     []config.Agent{{ID: "bot", TokenSHA: "bot-sha", Grants: []config.Grant{{Workspace: "thp", Role: "dev"}}}},
			Tunnels:    map[string]config.Tunnel{"thp-vpn": {Type: "globalprotect"}},
		}
		f.srv = &Server{
			reg:        &policy.OperationRegistry{Operations: f.ops, TunnelChecker: f.tunnelsUp},
			logger:     zerolog.Nop(),
			auditLog:   audit.GetLogger(),
			httpClient: httprest.NewConnector(),
		}
		f.srv.cfgPtr.Store(cfg)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/thp/op/"+opID, strings.NewReader(body))
	w := httptest.NewRecorder()
	if dryRun {
		f.srv.handleDryRun(w, r, "bot-sha", "thp", opID)
	} else {
		f.srv.executeOp(w, r, "bot-sha", "thp", opID)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: response is not JSON: %v (%s)", opID, err, w.Body.String())
	}
	return w.Code, out
}

// An env-less call to a site with several environments used to go to whichever
// env Go's randomised map iteration yielded first (THP jira: 4 of 18 calls hit
// the VPN-only jiraops). With default_env set, it always goes to that env.
func TestEnvlessCallUsesSiteDefaultEnv(t *testing.T) {
	f := newEnvFixture(t)
	jira := f.sites["jira"]
	jira.DefaultEnv = "prod"
	f.sites["jira"] = jira
	f.addOp("jira.comments", "jira", nil)
	for i := 0; i < 30; i++ {
		if code, out := f.call(t, "jira.comments", map[string]interface{}{"key": "THP-1"}); code != http.StatusOK || out["status"] == "error" {
			t.Fatalf("call %d: status %d, body %v", i, code, out)
		}
	}
	if p, v := f.prod.Load(), f.vpn.Load(); p != 30 || v != 0 {
		t.Errorf("prod hits = %d, vpn hits = %d; want 30 and 0", p, v)
	}
	if code, out := f.call(t, "jira.comments", map[string]interface{}{"key": "THP-1", "env": "corpo"}); code != http.StatusForbidden || out["rule"] != "tunnel" {
		t.Errorf("explicit env=corpo with the tunnel down: status %d, body %v; want a tunnel deny", code, out)
	}
}

// Without default_env, an env-less call to a multi-env site is refused, every
// time, before any upstream call (dry run included).
func TestEnvlessCallToMultiEnvSiteIsDenied(t *testing.T) {
	f := newEnvFixture(t)
	f.addOp("jira.comments", "jira", nil)
	for i := 0; i < 30; i++ {
		code, out := f.call(t, "jira.comments", map[string]interface{}{"key": "THP-1"})
		if code != http.StatusForbidden || out["rule"] != "validation" {
			t.Fatalf("call %d: status %d, body %v; want a validation deny", i, code, out)
		}
		want := "missing required parameter: env (site jira has environments corpo, prod)"
		if out["detail"] != want {
			t.Fatalf("call %d: detail = %q, want %q", i, out["detail"], want)
		}
	}
	if code, out := f.send(t, "jira.comments", `{"key":"THP-1"}`, true); code != http.StatusForbidden || out["rule"] != "validation" {
		t.Errorf("dry run: status %d, body %v; want a validation deny", code, out)
	}
	if n := f.prod.Load() + f.vpn.Load(); n != 0 {
		t.Errorf("upstream called %d times for refused requests", n)
	}
}

func TestUnknownEnvIsDenied(t *testing.T) {
	f := newEnvFixture(t)
	f.addOp("jira.comments", "jira", nil)
	for i := 0; i < 30; i++ {
		code, out := f.call(t, "jira.comments", map[string]interface{}{"key": "THP-1", "env": "prdo"})
		want := `parameter env="prdo" is not an environment of site jira (one of corpo, prod)`
		if code != http.StatusForbidden || out["detail"] != want {
			t.Fatalf("call %d: status %d, body %v; want deny %q", i, code, out, want)
		}
	}
	if n := f.prod.Load() + f.vpn.Load(); n != 0 {
		t.Errorf("upstream called %d times for refused requests", n)
	}
}

// The op-level `env` default (jira.changelog's idiom) picks the env when the
// caller names none, so env-less jira.* calls keep working, always on prod.
func TestOpEnvDefaultIsDeterministic(t *testing.T) {
	f := newEnvFixture(t)
	f.addOp("jira.comments", "jira", map[string]policy.ParamDef{"env": {Type: "string", Default: "prod"}})
	for i := 0; i < 30; i++ {
		if code, out := f.call(t, "jira.comments", map[string]interface{}{"key": "THP-1"}); code != http.StatusOK || out["status"] == "error" {
			t.Fatalf("call %d: status %d, body %v", i, code, out)
		}
	}
	if p, v := f.prod.Load(), f.vpn.Load(); p != 30 || v != 0 {
		t.Errorf("prod hits = %d, vpn hits = %d; want 30 and 0", p, v)
	}
}

// The tunnel check must look at the env the request will actually use: one
// filled in by an op default, or the only env of a single-env site. Before, both
// skipped the check (it read only an explicit `env`) and failed later on DNS.
func TestTunnelCheckCoversDefaultedAndSoleEnv(t *testing.T) {
	f := newEnvFixture(t)
	f.addOp("jira.comments", "jira", map[string]policy.ParamDef{"env": {Type: "string", Default: "corpo"}})
	f.addOp("corpjira.comments", "corpjira", nil)
	want := "required tunnel thp-vpn is down: bring it up manually"
	for _, opID := range []string{"jira.comments", "corpjira.comments"} {
		code, out := f.call(t, opID, map[string]interface{}{"key": "THP-1"})
		if code != http.StatusForbidden || out["rule"] != "tunnel" || out["detail"] != want {
			t.Errorf("%s: status %d, body %v; want tunnel deny %q", opID, code, out, want)
		}
	}
	// A request with no body at all still gets the op's defaults.
	f.ops["jira.me"] = &policy.Operation{ID: "jira.me", Connector: "http-rest", Site: "jira", Method: "GET", Path: "/rest/api/3/myself",
		Params: map[string]policy.ParamDef{"env": {Type: "string", Default: "corpo"}}}
	f.srv = nil
	if code, out := f.send(t, "jira.me", "", false); code != http.StatusForbidden || out["detail"] != want {
		t.Errorf("jira.me with an empty body: status %d, body %v; want tunnel deny %q", code, out, want)
	}
	if n := f.vpn.Load(); n != 0 {
		t.Errorf("VPN-only upstream called %d times with the tunnel down", n)
	}

	f.tunnelsUp["thp-vpn"] = true
	if code, out := f.call(t, "corpjira.comments", map[string]interface{}{"key": "THP-1"}); code != http.StatusOK || out["status"] == "error" {
		t.Fatalf("single-env site without env, tunnel up: status %d, body %v", code, out)
	}
	if n := f.vpn.Load(); n != 1 {
		t.Errorf("VPN-only upstream hits = %d, want 1", n)
	}
}

func TestResolveEnv(t *testing.T) {
	two := config.Site{Environments: map[string]config.Environment{"prod": {BaseURL: "p"}, "corpo": {BaseURL: "c"}}}
	one := config.Site{Environments: map[string]config.Environment{"only": {BaseURL: "o"}}}
	cases := []struct {
		name    string
		site    config.Site
		params  map[string]interface{}
		wantEnv string
		wantErr string
	}{
		{"named env", two, map[string]interface{}{"env": "corpo"}, "corpo", ""},
		{"cluster selects env", two, map[string]interface{}{"cluster": "prod"}, "prod", ""},
		{"sole env without name", one, nil, "only", ""},
		{"default env without name", config.Site{DefaultEnv: "prod", Environments: two.Environments}, nil, "prod", ""},
		{"named env beats default", config.Site{DefaultEnv: "prod", Environments: two.Environments}, map[string]interface{}{"env": "corpo"}, "corpo", ""},
		{"several envs without name", two, map[string]interface{}{}, "", "missing required parameter: env (site s has environments corpo, prod)"},
		{"unknown env", one, map[string]interface{}{"env": "x"}, "", `parameter env="x" is not an environment of site s (one of only)`},
		{"no envs", config.Site{}, nil, "", "no environment found for site s"},
	}
	for _, c := range cases {
		name, env, err := resolveEnv(c.site, "s", c.params)
		if c.wantErr != "" {
			if err == nil || err.Error() != c.wantErr {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || name != c.wantEnv || env == nil || env.BaseURL != c.site.Environments[c.wantEnv].BaseURL {
			t.Errorf("%s: got (%q, %v, %v), want %q", c.name, name, env, err, c.wantEnv)
		}
	}
}
