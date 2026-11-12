package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/external_gateway/internal/tokencheck"
	"github.com/rs/zerolog"
)

// credStore is a mutable secret store (admin_hardening_test.go's fakeStore never resolves).
type credStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (c *credStore) Resolve(ws, name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[ws+"/"+name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("secret %s/%s not found", ws, name)
}

func (c *credStore) Set(ws, name string, v []byte) error {
	c.mu.Lock()
	c.m[ws+"/"+name] = v
	c.mu.Unlock()
	return nil
}

func (c *credStore) Delete(ws, name string) error {
	c.mu.Lock()
	delete(c.m, ws+"/"+name)
	c.mu.Unlock()
	return nil
}

// fakeGitLab answers the two calls tokencheck.checkGitLab makes, under /gitlab/api/v4
// so DetectKind sees "gitlab" in the base URL. Fields may change between requests.
type fakeGitLab struct {
	srv        *httptest.Server
	mu         sync.Mutex
	userStatus int    // 0 = 200
	selfStatus int    // 0 = 200
	expiresAt  string // "" = null
	delay      time.Duration
	userHits   atomic.Int32
	selfHits   atomic.Int32
}

const (
	leakToken    = "glpat-LEAKCHECK-token"
	leakUser     = "leakcheck-username"
	leakTokName  = "leakcheck-token-name"
	leakScope    = "leakcheck_scope"
	credAgentTok = "cred-agent-token"
)

func newFakeGitLab(t *testing.T) *fakeGitLab {
	g := &fakeGitLab{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		us, ss, exp, d := g.userStatus, g.selfStatus, g.expiresAt, g.delay
		g.mu.Unlock()
		if d > 0 {
			time.Sleep(d)
		}
		switch r.URL.Path {
		case "/gitlab/api/v4/user":
			g.userHits.Add(1)
			if us != 0 && us != 200 {
				w.WriteHeader(us)
				w.Write([]byte(`{"error":"invalid_token","error_description":"Token is expired. ` + leakUser + `"}`))
				return
			}
			w.Write([]byte(`{"username":"` + leakUser + `","name":"` + leakUser + `"}`))
		case "/gitlab/api/v4/personal_access_tokens/self":
			g.selfHits.Add(1)
			if ss != 0 && ss != 200 {
				w.WriteHeader(ss)
				return
			}
			e := "null"
			if exp != "" {
				e = `"` + exp + `"`
			}
			w.Write([]byte(`{"name":"` + leakTokName + `","expires_at":` + e + `,"scopes":["` + leakScope + `"]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitLab) set(f func(g *fakeGitLab)) {
	g.mu.Lock()
	f(g)
	g.mu.Unlock()
}

func (g *fakeGitLab) base() string { return g.srv.URL + "/gitlab/api/v4" }

func (g *fakeGitLab) hits() int32 { return g.userHits.Load() + g.selfHits.Load() }

// credNoon is the fixed "now" of these tests: local noon, so date math is away from midnight.
var credNoon = time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)

func credDate(days int) string { return credNoon.AddDate(0, 0, days).Format("2006-01-02") }

type credHarness struct {
	s     *Server
	store *credStore
	dir   string
}

// newCredHarness writes the config and ops files (so /admin/reload works), installs a
// fresh store and returns a server whose credential clock is credNoon.
// sitesYAML maps workspace -> its "sites:" block body (indented 6 spaces).
// grants is the role's op list; every workspace gets the same role.
func newCredHarness(t *testing.T, sitesYAML map[string]string, grants []string, opsYAML string) *credHarness {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	opsPath := filepath.Join(dir, "ops.yaml")
	var ws, gr strings.Builder
	for name, sites := range sitesYAML {
		ws.WriteString("  " + name + ":\n    sites:\n" + sites)
		gr.WriteString("      - {workspace: " + name + ", role: r}\n")
	}
	cfgYAML := "admin_token_sha256: " + sha(adminTok) + "\nworkspaces:\n" + ws.String() +
		"roles:\n  r:\n    operations: [" + strings.Join(grants, ", ") + "]\n" +
		"agents:\n  - id: bot\n    token_sha256: " + sha(credAgentTok) + "\n    grants:\n" + gr.String()
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opsPath, []byte(opsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := policy.NewRegistry(opsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Init(dir, 1); err != nil {
		t.Fatal(err)
	}
	store := &credStore{m: map[string][]byte{}}
	secrets.SetStore(store)
	secrets.GetResolver().FlushCache() // the resolver's own 30 s cache outlives SetStore
	s := New(cfg, reg, opsPath, zerolog.Nop())
	s.creds.now = func() time.Time { return credNoon }
	s.creds.timeout = 300 * time.Millisecond
	h := &credHarness{s: s, store: store, dir: dir}
	t.Cleanup(func() { secrets.SetStore(&secrets.WinCredStore{}); secrets.GetResolver().FlushCache() })
	return h
}

// advance moves the credential clock forward (tests call it between requests, not during one).
func (h *credHarness) advance(d time.Duration) {
	prev := h.s.creds.now
	at := prev().Add(d)
	h.s.creds.now = func() time.Time { return at }
}

func (h *credHarness) caps(t *testing.T, ws string) ([]credentialEntry, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/capabilities?workspace="+ws, nil)
	r.Header.Set("Authorization", "Bearer "+credAgentTok)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("/capabilities %s: %d %s", ws, w.Code, w.Body.String())
	}
	var out struct {
		Credentials []credentialEntry `json:"credentials"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Credentials == nil {
		t.Fatalf("credentials field missing or null: %s", w.Body.String())
	}
	return out.Credentials, w.Body.String()
}

func gitlabSite(envs map[string]string) string {
	var b strings.Builder
	b.WriteString("      gitlab:\n        environments:\n")
	for env, line := range envs {
		b.WriteString("          " + env + ": " + line + "\n")
	}
	return b.String()
}

const credOps = "operations:\n" +
	"  gitlab.a:\n    connector: http-rest\n    site: gitlab\n    method: GET\n    path: /x\n" +
	"  jira.a:\n    connector: http-rest\n    site: jira\n    method: GET\n    path: /x\n" +
	"  hidden.a:\n    connector: http-rest\n    site: hidden\n    method: GET\n    path: /x\n" +
	"  off.a:\n    connector: http-rest\n    site: off\n    method: GET\n    path: /x\n"

// oneGitLab is a harness with one gitlab/prod env on g, secret stored unless noSecret.
func oneGitLab(t *testing.T, ws string, g *fakeGitLab, noSecret bool) *credHarness {
	h := newCredHarness(t, map[string]string{ws: gitlabSite(map[string]string{
		"prod": "{base_url: " + g.base() + ", secret_ref: secret://" + ws + "/gitlab}"})}, []string{"gitlab.a"}, credOps)
	if !noSecret {
		h.store.Set(ws, "gitlab", []byte(leakToken))
	}
	return h
}

func intp(n int) *int { return &n }

func checkEntry(t *testing.T, name string, got []credentialEntry, want credentialEntry, warnHas string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s: %d entries %+v", name, len(got), got)
	}
	e := got[0]
	if e.Site != want.Site || e.Env != want.Env || e.State != want.State || e.ExpiresAt != want.ExpiresAt {
		t.Errorf("%s: got %+v, want %+v", name, e, want)
	}
	if (e.DaysLeft == nil) != (want.DaysLeft == nil) || (e.DaysLeft != nil && *e.DaysLeft != *want.DaysLeft) {
		t.Errorf("%s: days_left %v, want %v", name, e.DaysLeft, want.DaysLeft)
	}
	if warnHas == "" && e.Warning != "" {
		t.Errorf("%s: unexpected warning %q", name, e.Warning)
	}
	if warnHas != "" && !strings.Contains(e.Warning, warnHas) {
		t.Errorf("%s: warning %q lacks %q", name, e.Warning, warnHas)
	}
}

func TestCredentialsExpiryWarning(t *testing.T) {
	cases := []struct {
		name, exp  string
		selfStatus int
		days       *int
		warn       string
	}{
		{"today", credDate(0), 0, intp(0), "expires today (" + credDate(0) + ")"},
		{"yesterday", credDate(-1), 0, intp(-1), "expired on " + credDate(-1)},
		{"in 7 days", credDate(7), 0, intp(7), "expires in 7 days (" + credDate(7) + "): ask the owner to renew it"},
		{"in 8 days", credDate(8), 0, intp(8), ""},
		{"never expires", "", 0, nil, ""},
		{"self 404", credDate(1), 404, nil, ""},
	}
	for i, c := range cases {
		g := newFakeGitLab(t)
		g.set(func(g *fakeGitLab) { g.expiresAt, g.selfStatus = c.exp, c.selfStatus })
		h := oneGitLab(t, fmt.Sprintf("exp%d", i), g, false)
		got, _ := h.caps(t, fmt.Sprintf("exp%d", i))
		want := credentialEntry{Site: "gitlab", Env: "prod", State: "ok", DaysLeft: c.days}
		if c.selfStatus == 0 {
			want.ExpiresAt = c.exp
		}
		checkEntry(t, c.name, got, want, c.warn)
	}
}

func TestCredentialsRejectedMissingUnreachable(t *testing.T) {
	g := newFakeGitLab(t)
	g.set(func(g *fakeGitLab) { g.userStatus = 401 })
	got, _ := oneGitLab(t, "rej", g, false).caps(t, "rej")
	checkEntry(t, "401", got, credentialEntry{Site: "gitlab", Env: "prod", State: "rejected"}, "rejected by upstream (HTTP 401): ask the owner to renew it")

	g = newFakeGitLab(t)
	g.set(func(g *fakeGitLab) { g.userStatus = 429 })
	got, _ = oneGitLab(t, "limit", g, false).caps(t, "limit")
	checkEntry(t, "429", got, credentialEntry{Site: "gitlab", Env: "prod", State: "unreachable"}, "")

	g = newFakeGitLab(t)
	got, _ = oneGitLab(t, "miss", g, true).caps(t, "miss")
	checkEntry(t, "missing", got, credentialEntry{Site: "gitlab", Env: "prod", State: "missing"}, "no usable stored token for gitlab/prod")
	if g.hits() != 0 {
		t.Errorf("missing secret still called upstream %d times", g.hits())
	}

	g = newFakeGitLab(t)
	h := oneGitLab(t, "closed", g, false)
	g.srv.Close()
	got, _ = h.caps(t, "closed")
	checkEntry(t, "closed server", got, credentialEntry{Site: "gitlab", Env: "prod", State: "unreachable"}, "")

	g = newFakeGitLab(t)
	g.set(func(g *fakeGitLab) { g.delay = 500 * time.Millisecond })
	h = oneGitLab(t, "slow", g, false)
	start := time.Now()
	got, _ = h.caps(t, "slow")
	checkEntry(t, "slow upstream", got, credentialEntry{Site: "gitlab", Env: "prod", State: "unreachable"}, "")
	if d := time.Since(start); d > 450*time.Millisecond {
		t.Errorf("slow upstream held /capabilities for %v; timeout is %v", d, h.s.creds.timeout)
	}
}

func TestCredentialsScope(t *testing.T) {
	ok, rej := newFakeGitLab(t), newFakeGitLab(t)
	ok.set(func(g *fakeGitLab) { g.expiresAt = credDate(30) })
	rej.set(func(g *fakeGitLab) { g.userStatus = 401 })
	sites := gitlabSite(map[string]string{
		"prod":     "{base_url: " + ok.base() + ", secret_ref: secret://scope/gl-prod}",
		"dev":      "{base_url: " + rej.base() + ", secret_ref: secret://scope/gl-dev}",
		"nosecret": "{base_url: " + ok.base() + "}",
	}) +
		"      jira:\n        environments:\n          prod: {base_url: \"https://x.atlassian.net\", secret_ref: secret://scope/jira}\n" +
		"      hidden:\n        environments:\n          prod: {base_url: " + ok.base() + ", secret_ref: secret://scope/hidden}\n" +
		"      off:\n        disabled: true\n        environments:\n          prod: {base_url: " + ok.base() + ", secret_ref: secret://scope/off}\n"
	h := newCredHarness(t, map[string]string{"scope": sites}, []string{"gitlab.a", "jira.a", "off.a"}, credOps)
	for _, n := range []string{"gl-prod", "gl-dev", "jira", "hidden", "off"} {
		h.store.Set("scope", n, []byte(leakToken))
	}
	got, _ := h.caps(t, "scope")
	if len(got) != 2 || got[0].Env != "dev" || got[1].Env != "prod" || got[0].State != "rejected" || got[1].State != "ok" {
		t.Fatalf("want gitlab dev rejected, gitlab prod ok (sorted, nothing else); got %+v", got)
	}
}

func TestCredentialsCache(t *testing.T) {
	g := newFakeGitLab(t)
	g.set(func(g *fakeGitLab) { g.expiresAt = credDate(30) })
	h := oneGitLab(t, "cache", g, false)
	h.caps(t, "cache")
	if g.userHits.Load() != 1 || g.selfHits.Load() != 1 {
		t.Fatalf("cold: hits user=%d self=%d, want 1 each", g.userHits.Load(), g.selfHits.Load())
	}
	h.caps(t, "cache")
	h.advance(59 * time.Minute)
	got, _ := h.caps(t, "cache")
	if n := g.hits(); n != 2 {
		t.Fatalf("warm calls within 1 h hit upstream: %d hits, want 2", n)
	}
	if *got[0].DaysLeft != 30 {
		t.Errorf("days_left %d", *got[0].DaysLeft)
	}
	h.advance(2 * time.Minute) // 61 min after the ok check
	h.caps(t, "cache")
	if n := g.hits(); n != 4 {
		t.Fatalf("ok entry not re-checked after 1 h: %d hits, want 4", n)
	}

	// A non-ok result lives 5 min.
	g.set(func(g *fakeGitLab) { g.userStatus = 401 })
	h.s.creds.clear()
	h.caps(t, "cache") // user only: rejected stops before /self
	base := g.hits()
	h.advance(4 * time.Minute)
	h.caps(t, "cache")
	if g.hits() != base {
		t.Fatalf("rejected entry re-checked before 5 min")
	}
	h.advance(2 * time.Minute)
	h.caps(t, "cache")
	if g.hits() != base+1 {
		t.Fatalf("rejected entry not re-checked after 5 min: %d, want %d", g.hits(), base+1)
	}

	// /admin/secrets set and delete clear the cache (Resolver.Set/Delete also forget its own 30 s entry).
	g.set(func(g *fakeGitLab) { g.userStatus = 0 })
	if w := adminDo(h.s, http.MethodPost, "/admin/secrets", `{"workspace":"cache","name":"gitlab","value":"renewed-token"}`); w.Code != 200 {
		t.Fatalf("admin set: %d %s", w.Code, w.Body.String())
	}
	got, _ = h.caps(t, "cache")
	if got[0].State != "ok" {
		t.Fatalf("after admin set: %+v, want ok (cache cleared)", got[0])
	}
	if w := adminDo(h.s, http.MethodDelete, "/admin/secrets?workspace=cache&name=gitlab", ""); w.Code != 200 {
		t.Fatalf("admin delete: %d %s", w.Code, w.Body.String())
	}
	got, _ = h.caps(t, "cache")
	if got[0].State != "missing" {
		t.Fatalf("after admin delete: %+v, want missing", got[0])
	}

	// /admin/reload clears it too.
	h.store.Set("cache", "gitlab", []byte(leakToken))
	before := g.hits()
	if w := adminDo(h.s, http.MethodPost, "/admin/reload", ""); w.Code != 200 {
		t.Fatalf("reload: %d %s", w.Code, w.Body.String())
	}
	got, _ = h.caps(t, "cache")
	if got[0].State != "ok" || g.hits() == before {
		t.Fatalf("after reload: %+v, hits %d -> %d; want ok and a fresh check", got[0], before, g.hits())
	}
}

// A check that started before a clear must not store its (stale) result.
func TestCredCacheClearDropsInFlightResult(t *testing.T) {
	c := newCredCache()
	_, _, gen := c.get("k")
	c.clear()
	c.put("k", gen, credCached{res: tokencheck.Result{State: tokencheck.StateOK}, at: time.Now()})
	if _, fresh, _ := c.get("k"); fresh {
		t.Fatal("result of a check started before clear was stored")
	}
}

func TestCredentialsNoLeak(t *testing.T) {
	g := newFakeGitLab(t)
	g.set(func(g *fakeGitLab) { g.expiresAt = credDate(3) })
	_, body := oneGitLab(t, "leak", g, false).caps(t, "leak")
	for _, s := range []string{leakToken, leakUser, leakTokName, leakScope} {
		if strings.Contains(body, s) {
			t.Errorf("response contains %q: %s", s, body)
		}
	}
	g = newFakeGitLab(t)
	g.set(func(g *fakeGitLab) { g.userStatus = 401 }) // the 401 body names leakUser
	_, body = oneGitLab(t, "leak2", g, false).caps(t, "leak2")
	if strings.Contains(body, leakUser) {
		t.Errorf("rejected response leaks upstream detail: %s", body)
	}
}

func TestCredentialsWorkspaces(t *testing.T) {
	ok, rej := newFakeGitLab(t), newFakeGitLab(t)
	rej.set(func(g *fakeGitLab) { g.userStatus = 401 })
	h := newCredHarness(t, map[string]string{
		"wsa": gitlabSite(map[string]string{"prod": "{base_url: " + ok.base() + ", secret_ref: secret://wsa/gitlab}"}),
		"wsb": gitlabSite(map[string]string{"prod": "{base_url: " + rej.base() + ", secret_ref: secret://wsb/gitlab}"}),
	}, []string{"gitlab.a"}, credOps)
	h.store.Set("wsa", "gitlab", []byte(leakToken))
	h.store.Set("wsb", "gitlab", []byte(leakToken))
	a, _ := h.caps(t, "wsa")
	b, _ := h.caps(t, "wsb")
	a2, _ := h.caps(t, "wsa")
	if a[0].State != "ok" || b[0].State != "rejected" || a2[0].State != "ok" {
		t.Fatalf("workspaces mixed: wsa=%+v wsb=%+v wsa again=%+v", a, b, a2)
	}
}

func TestEntryForGitHubDate(t *testing.T) {
	res := tokencheck.Result{State: tokencheck.StateOK, ExpiresAt: credDate(5) + " 12:00:00 UTC"}
	e := entryFor("github", "prod", res, nil, credNoon)
	if e.DaysLeft == nil || *e.DaysLeft != 5 || !strings.Contains(e.Warning, "expires in 5 days ("+credDate(5)+")") {
		t.Fatalf("GitHub date: %+v", e)
	}
	e = entryFor("github", "prod", tokencheck.Result{State: tokencheck.StateOK, ExpiresAt: "soon"}, nil, credNoon)
	if e.DaysLeft != nil || e.Warning != "" || e.ExpiresAt != "soon" {
		t.Fatalf("unparseable date: %+v", e)
	}
}

func TestCredentialsParallelNoRace(t *testing.T) {
	g := newFakeGitLab(t)
	h := oneGitLab(t, "par", g, false)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/capabilities?workspace=par", nil)
			r.Header.Set("Authorization", "Bearer "+credAgentTok)
			h.s.ServeHTTP(httptest.NewRecorder(), r)
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); h.s.creds.clear() }()
	wg.Wait()
}
