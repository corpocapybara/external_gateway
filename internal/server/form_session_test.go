package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/rs/zerolog"
)

// EG0042: form-session ops through the real handler, against httptest upstreams.

const (
	fsAgentTok = "agent-EG42"
	fsPassword = "pw-EG42-secret"
	fsCookie   = "sid-EG42"
)

// memSecretStore is a secret store whose Set and Delete write, so a rotated
// password reaches the next login (fakeSecretStore's writes are no-ops).
type memSecretStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecretStore) Resolve(ws, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[ws+"/"+name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("secret %s/%s not found", ws, name)
}

func (s *memSecretStore) Set(ws, name string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ws+"/"+name] = v
	return nil
}

func (s *memSecretStore) Delete(ws, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ws+"/"+name)
	return nil
}

// fsUpstream is a CodiMD stand-in: POST /login and GET /api/notes/{id}. It
// counts logins and data requests and records what each one carried.
type fsUpstream struct {
	srv        *httptest.Server
	logins     atomic.Int32
	data       atomic.Int32
	cookie     string // the connect.sid value a login sets
	mu         sync.Mutex
	passwords  []string
	gotCookies []string
	gotPaths   []string
	gotQueries []string
	// onLogin and onData override the default answers; n is the 1-based count.
	onLogin func(w http.ResponseWriter, r *http.Request, n int)
	onData  func(w http.ResponseWriter, r *http.Request, n int)
}

func newFSUpstream(t *testing.T, cookie string, tls bool) *fsUpstream {
	t.Helper()
	u := &fsUpstream{cookie: cookie}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/login" {
			n := int(u.logins.Add(1))
			r.ParseForm()
			u.mu.Lock()
			u.passwords = append(u.passwords, r.PostForm.Get("password"))
			u.mu.Unlock()
			if u.onLogin != nil {
				u.onLogin(w, r, n)
				return
			}
			fsLoginOK(w, u.cookie)
			return
		}
		n := int(u.data.Add(1))
		c, _ := r.Cookie("connect.sid")
		u.mu.Lock()
		if c != nil {
			u.gotCookies = append(u.gotCookies, c.Value)
		} else {
			u.gotCookies = append(u.gotCookies, "")
		}
		u.gotPaths = append(u.gotPaths, r.URL.Path)
		u.gotQueries = append(u.gotQueries, r.URL.RawQuery)
		u.mu.Unlock()
		if u.onData != nil {
			u.onData(w, r, n)
			return
		}
		if c == nil || c.Value != u.cookie {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"id":"n1","title":"T","secret_field":"hidden"}`))
	})
	if tls {
		u.srv = httptest.NewTLSServer(h)
	} else {
		u.srv = httptest.NewServer(h)
	}
	t.Cleanup(u.srv.Close)
	return u
}

// fsLoginOK answers a login as Express does: the cookie and a 302 to /.
func fsLoginOK(w http.ResponseWriter, cookie string) {
	http.SetCookie(w, &http.Cookie{Name: "connect.sid", Value: cookie, Path: "/", HttpOnly: true})
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusFound)
}

func (u *fsUpstream) port() string {
	return u.srv.URL[strings.LastIndex(u.srv.URL, ":")+1:]
}

// fsEnv is one site environment of the test config. fields is the YAML flow
// mapping body of the environment; empty means base_url, user and secret_ref
// pointing at the workspace's own codimd secret.
type fsEnv struct {
	ws, site, env string
	url           string
	fields        string
}

func (e fsEnv) yaml() string {
	if e.fields != "" {
		return e.fields
	}
	return fmt.Sprintf("base_url: %q, user: bot@example.com, secret_ref: \"secret://%s/codimd\"", e.url, e.ws)
}

// newFormSessionServer writes config and ops files for envs (one op
// <site>.note per site), stores the secrets and builds the server with New.
func newFormSessionServer(t *testing.T, store map[string]string, envs ...fsEnv) *Server {
	t.Helper()
	ms := &memSecretStore{m: map[string][]byte{}}
	for k, v := range store {
		ms.m[k] = []byte(v)
	}
	secrets.SetStore(ms)
	secrets.GetResolver().FlushCache()
	t.Cleanup(func() {
		secrets.SetStore(&secrets.WinCredStore{})
		secrets.GetResolver().FlushCache()
	})

	// workspace -> site -> env lines
	tree := map[string]map[string][]string{}
	siteSet := map[string]bool{}
	for _, e := range envs {
		if tree[e.ws] == nil {
			tree[e.ws] = map[string][]string{}
		}
		tree[e.ws][e.site] = append(tree[e.ws][e.site], fmt.Sprintf("          %s: {%s}\n", e.env, e.yaml()))
		siteSet[e.site] = true
	}
	var sites []string
	for s := range siteSet {
		sites = append(sites, s)
	}
	sort.Strings(sites)

	var b strings.Builder
	b.WriteString("admin_token_sha256: " + sha(adminTok) + "\nworkspaces:\n")
	var wsNames []string
	for ws := range tree {
		wsNames = append(wsNames, ws)
	}
	sort.Strings(wsNames)
	for _, ws := range wsNames {
		b.WriteString("  " + ws + ":\n    sites:\n")
		for site, lines := range tree[ws] {
			b.WriteString("      " + site + ":\n        environments:\n")
			for _, l := range lines {
				b.WriteString(l)
			}
		}
	}
	// One role per workspace: a role may only name ops whose site the workspace has.
	b.WriteString("roles:\n")
	for _, ws := range wsNames {
		var wsOps []string
		for _, site := range sites {
			if _, ok := tree[ws][site]; ok {
				wsOps = append(wsOps, site+".note")
			}
		}
		b.WriteString("  r_" + ws + ":\n    operations: [" + strings.Join(wsOps, ", ") + "]\n")
	}
	b.WriteString("agents:\n  - id: bot\n    token_sha256: " + sha(fsAgentTok) + "\n    grants:\n")
	for _, ws := range wsNames {
		b.WriteString("      - {workspace: " + ws + ", role: r_" + ws + "}\n")
	}

	var ops strings.Builder
	ops.WriteString("operations:\n")
	for _, s := range sites {
		ops.WriteString("  " + s + ".note:\n    connector: form-session\n    site: " + s + "\n    method: GET\n" +
			"    path: /api/notes/{noteId}\n    params:\n      noteId: {type: string, required: true}\n" +
			"      view: {type: string}\n      env: {type: string}\n    response:\n      allow_fields: [id, title]\n")
	}

	dir := t.TempDir()
	cfgPath, opsPath := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "operations.yaml")
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opsPath, []byte(ops.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config: %v\n%s", err, b.String())
	}
	reg, err := policy.NewRegistry(opsPath)
	if err != nil {
		t.Fatalf("ops: %v", err)
	}
	// audit.Init is process-global (first call wins), so the dir may be an
	// earlier test's removed TempDir: recreate it.
	if err := audit.Init(t.TempDir(), 1); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(audit.GetLogger().Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return New(cfg, reg, opsPath, zerolog.Nop())
}

// fsCall runs op in ws through the handler and returns the status, the decoded
// body and the raw body.
func fsCall(t *testing.T, s *Server, ws, op string, params map[string]interface{}) (int, map[string]interface{}, string) {
	t.Helper()
	if params == nil {
		params = map[string]interface{}{"noteId": "n1"}
	}
	body, _ := json.Marshal(params)
	r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+ws+"/op/"+op, strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+fsAgentTok)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// assertNoLeak checks that neither secret value is in any response body or
// audit log file.
func assertNoLeak(t *testing.T, bodies ...string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(audit.GetLogger().Dir(), "audit_*.log"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(b))
	}
	for _, b := range bodies {
		for _, secret := range []string{fsPassword, fsCookie} {
			if strings.Contains(b, secret) {
				t.Errorf("secret %q leaked: %.300s", secret, b)
			}
		}
	}
}

func fsStore(ws string) map[string]string {
	return map[string]string{ws + "/codimd": fsPassword}
}

func fsWantError(t *testing.T, out map[string]interface{}, raw string) {
	t.Helper()
	if out["status"] != "error" || out["error"] == nil || out["error"] == "" {
		t.Fatalf("want status:error with an error text, got %s", raw)
	}
}

func fsWantCounts(t *testing.T, u *fsUpstream, logins, data int32) {
	t.Helper()
	if l, d := u.logins.Load(), u.data.Load(); l != logins || d != data {
		t.Fatalf("logins %d data %d, want %d and %d", l, d, logins, data)
	}
}

func TestFormSessionOpRoundTrip(t *testing.T) {
	t.Run("200", func(t *testing.T) {
		u := newFSUpstream(t, fsCookie, false)
		s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", url: u.srv.URL})
		code, out, raw := fsCall(t, s, "fs1", "codimd.note", map[string]interface{}{"noteId": "n1", "view": "x"})
		if code != 200 || out["id"] != "n1" || out["title"] != "T" {
			t.Fatalf("code %d body %s", code, raw)
		}
		if _, ok := out["secret_field"]; ok {
			t.Fatalf("field outside allow_fields returned: %s", raw)
		}
		if out["upstream_status"] != float64(200) {
			t.Fatalf("upstream_status: %s", raw)
		}
		fsWantCounts(t, u, 1, 1)
		if u.gotCookies[0] != fsCookie || u.gotPaths[0] != "/api/notes/n1" || u.gotQueries[0] != "view=x" {
			t.Fatalf("upstream got cookie %q path %q query %q", u.gotCookies[0], u.gotPaths[0], u.gotQueries[0])
		}
		if u.passwords[0] != fsPassword {
			t.Fatalf("login password %q", u.passwords[0])
		}
		recs := auditRecords(t, out["request_id"].(string))
		if len(recs) != 1 || recs[0].UpstreamStatus != 200 {
			t.Fatalf("audit records %+v", recs)
		}
		assertNoLeak(t, raw)
	})
	for _, tc := range []struct {
		status int
		body   string
	}{
		// The 404 body echoes the session cookie: the taint must redact it.
		{404, `{"message":"no such note","echo":"` + fsCookie + `"}`},
		{503, "maintenance"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			u := newFSUpstream(t, fsCookie, false)
			u.onData = func(w http.ResponseWriter, r *http.Request, n int) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}
			s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", url: u.srv.URL})
			code, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
			if code != 200 || out["upstream_status"] != float64(tc.status) {
				t.Fatalf("code %d body %s", code, raw)
			}
			if tc.status == 404 && out["message"] != "no such note" {
				t.Fatalf("non-2xx body was filtered: %s", raw)
			}
			if tc.status == 503 && out["upstream_body"] != "maintenance" {
				t.Fatalf("503 body: %s", raw)
			}
			recs := auditRecords(t, out["request_id"].(string))
			if len(recs) != 1 || recs[0].UpstreamStatus != tc.status {
				t.Fatalf("audit records %+v", recs)
			}
			fsWantCounts(t, u, 1, 1)
			assertNoLeak(t, raw)
		})
	}
}

func TestFormSessionLoginFails(t *testing.T) {
	withCookie := func(w http.ResponseWriter) {
		http.SetCookie(w, &http.Cookie{Name: "connect.sid", Value: fsCookie})
	}
	// Each case that sets a cookie fails only through the status check, so the
	// error text pins which branch of Login refused it.
	cases := map[string]struct {
		answer   func(w http.ResponseWriter)
		wantText string
	}{
		"200 without cookie": {func(w http.ResponseWriter) { w.Write([]byte("login page")) }, "no session cookie"},
		"401":                {func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }, "login refused (status 401)"},
		"cookie plus 401": {func(w http.ResponseWriter) {
			withCookie(w)
			w.WriteHeader(http.StatusUnauthorized)
		}, "login refused (status 401)"},
		"cookie plus 302 to login": {func(w http.ResponseWriter) {
			withCookie(w)
			w.Header().Set("Location", "/login")
			w.WriteHeader(http.StatusFound)
		}, "form-session login refused"},
		"500":             {func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }, "login failed (status 500)"},
		"cookie plus 500": {func(w http.ResponseWriter) { withCookie(w); w.WriteHeader(http.StatusInternalServerError) }, "login failed (status 500)"},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			u := newFSUpstream(t, fsCookie, false)
			u.onLogin = func(w http.ResponseWriter, r *http.Request, n int) { tc.answer(w) }
			s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", url: u.srv.URL})
			_, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
			fsWantError(t, out, raw)
			if !strings.Contains(raw, tc.wantText) {
				t.Fatalf("want %q in %s", tc.wantText, raw)
			}
			fsWantCounts(t, u, 1, 0)
			assertNoLeak(t, raw)
		})
	}
}

func TestFormSessionSessionsIsolated(t *testing.T) {
	a, b, c := newFSUpstream(t, "sid-A-EG42x", false), newFSUpstream(t, "sid-B-EG42x", false), newFSUpstream(t, "sid-C-EG42x", false)
	x := newFSUpstream(t, fsCookie, false)
	store := map[string]string{"fs1/codimd": fsPassword, "fs2/codimd": fsPassword, "other/codimd": fsPassword}
	s := newFormSessionServer(t, store,
		fsEnv{ws: "fs1", site: "codimd", env: "prod", url: a.srv.URL},
		fsEnv{ws: "fs1", site: "codimd2", env: "prod", url: b.srv.URL, fields: fmt.Sprintf("base_url: %q, user: bot@example.com, secret_ref: \"secret://fs1/codimd\"", b.srv.URL)},
		fsEnv{ws: "fs2", site: "codimd", env: "prod", url: c.srv.URL},
		fsEnv{ws: "fs3", site: "codimd", env: "prod", fields: fmt.Sprintf("base_url: %q, user: bot@example.com, secret_ref: \"secret://other/codimd\"", x.srv.URL)},
	)
	t.Run("a two sites", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			for _, op := range []string{"codimd.note", "codimd2.note"} {
				if code, out, raw := fsCall(t, s, "fs1", op, nil); code != 200 || out["id"] != "n1" {
					t.Fatalf("%s: %d %s", op, code, raw)
				}
			}
		}
		fsWantCounts(t, a, 1, 2)
		fsWantCounts(t, b, 1, 2)
		for _, got := range a.gotCookies {
			if got != a.cookie {
				t.Fatalf("server A saw cookie %q", got)
			}
		}
		for _, got := range b.gotCookies {
			if got != b.cookie {
				t.Fatalf("server B saw cookie %q", got)
			}
		}
	})
	t.Run("b second workspace", func(t *testing.T) {
		if code, out, raw := fsCall(t, s, "fs2", "codimd.note", nil); code != 200 || out["id"] != "n1" {
			t.Fatalf("%d %s", code, raw)
		}
		fsWantCounts(t, c, 1, 1)
		fsWantCounts(t, a, 1, 2)
	})
	t.Run("c cross-tenant secret_ref", func(t *testing.T) {
		_, out, raw := fsCall(t, s, "fs3", "codimd.note", nil)
		fsWantError(t, out, raw)
		if !strings.Contains(raw, "cross-tenant") {
			t.Fatalf("want the cross-tenant refusal: %s", raw)
		}
		fsWantCounts(t, x, 0, 0)
		assertNoLeak(t, raw)
	})
	t.Run("d two envs of one site", func(t *testing.T) {
		e1, e2 := newFSUpstream(t, "sid-E1-EG42x", false), newFSUpstream(t, "sid-E2-EG42x", false)
		s := newFormSessionServer(t, fsStore("fs1"),
			fsEnv{ws: "fs1", site: "codimd", env: "e1", url: e1.srv.URL},
			fsEnv{ws: "fs1", site: "codimd", env: "e2", url: e2.srv.URL},
		)
		for i := 0; i < 2; i++ {
			for _, env := range []string{"e1", "e2"} {
				if _, out, raw := fsCall(t, s, "fs1", "codimd.note", map[string]interface{}{"noteId": "n1", "env": env}); out["id"] != "n1" {
					t.Fatalf("env %s: %s", env, raw)
				}
			}
		}
		fsWantCounts(t, e1, 1, 2)
		fsWantCounts(t, e2, 1, 2)
	})
}

func TestFormSessionRelogin(t *testing.T) {
	run := func(t *testing.T, onData func(w http.ResponseWriter, r *http.Request, n int), onLogin func(w http.ResponseWriter, r *http.Request, n int)) (*fsUpstream, map[string]interface{}, string) {
		u := newFSUpstream(t, fsCookie, false)
		u.onData, u.onLogin = onData, onLogin
		s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", url: u.srv.URL})
		_, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
		return u, out, raw
	}
	ok := func(w http.ResponseWriter) { w.Write([]byte(`{"id":"n1","title":"T"}`)) }
	signal401 := func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }
	signal302 := func(w http.ResponseWriter) {
		w.Header().Set("Location", "http://codimd.example/login?next=/n1")
		w.WriteHeader(http.StatusFound)
	}
	for name, signal := range map[string]func(http.ResponseWriter){"401": signal401, "302 to login": signal302} {
		signal := signal
		t.Run("a once "+name, func(t *testing.T) {
			u, out, raw := run(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if n == 1 {
					signal(w)
					return
				}
				ok(w)
			}, nil)
			if out["id"] != "n1" || out["upstream_status"] != float64(200) {
				t.Fatalf("want success: %s", raw)
			}
			fsWantCounts(t, u, 2, 2)
		})
	}
	t.Run("b always", func(t *testing.T) {
		u := newFSUpstream(t, fsCookie, false)
		u.onData = func(w http.ResponseWriter, r *http.Request, n int) { signal401(w) }
		s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", url: u.srv.URL})
		_, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
		fsWantError(t, out, raw)
		if !strings.Contains(raw, "still asks for login") {
			t.Fatalf("want the re-login error: %s", raw)
		}
		fsWantCounts(t, u, 2, 2)
		assertNoLeak(t, raw)
		// The rejected cookie is not kept: the next call logs in first (and, still
		// refused, once more): 4 and 4. A kept cookie would give 3 logins, 4 data.
		fsCall(t, s, "fs1", "codimd.note", nil)
		fsWantCounts(t, u, 4, 4)
	})
	t.Run("c re-login fails", func(t *testing.T) {
		u, out, raw := run(t, func(w http.ResponseWriter, r *http.Request, n int) { signal401(w) },
			func(w http.ResponseWriter, r *http.Request, n int) {
				if n == 1 {
					fsLoginOK(w, fsCookie)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
			})
		fsWantError(t, out, raw)
		fsWantCounts(t, u, 2, 1)
		assertNoLeak(t, raw)
	})
	for name, answer := range map[string]func(http.ResponseWriter){
		"403": func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
		"302 elsewhere": func(w http.ResponseWriter) {
			w.Header().Set("Location", "/other")
			w.WriteHeader(http.StatusFound)
		},
	} {
		answer := answer
		t.Run("d passes "+name, func(t *testing.T) {
			u, out, raw := run(t, func(w http.ResponseWriter, r *http.Request, n int) { answer(w) }, nil)
			want := float64(http.StatusForbidden)
			if name == "302 elsewhere" {
				want = http.StatusFound
			}
			if out["upstream_status"] != want {
				t.Fatalf("want upstream_status %v: %s", want, raw)
			}
			fsWantCounts(t, u, 1, 1)
		})
	}
}

func TestFormSessionConcurrentCalls(t *testing.T) {
	const callers = 4
	u := newFSUpstream(t, fsCookie, false)
	s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", url: u.srv.URL})
	// Gate: the login answers only once all callers have reached the session
	// lock, so without the lock every caller would log in.
	arrived := make(chan struct{}, 16)
	s.formSessions.SetWaitHook(func() { arrived <- struct{}{} })
	u.onLogin = func(w http.ResponseWriter, r *http.Request, n int) {
		deadline := time.After(5 * time.Second)
		for i := 0; i < callers; i++ {
			select {
			case <-arrived:
			case <-deadline:
				t.Errorf("login %d: only %d of %d callers reached the session lock", n, i, callers)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		fsLoginOK(w, fsCookie)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
			if out["id"] != "n1" {
				results[i] = raw
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i, r := range results {
		if r != "" {
			t.Errorf("caller %d failed: %s", i, r)
		}
	}
	fsWantCounts(t, u, 1, callers)
}

func TestFormSessionDroppedOnReload(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		password                 string
	}{
		{"reload", http.MethodPost, "/admin/reload", "", fsPassword},
		{"secret set", http.MethodPost, "/admin/secrets", `{"workspace":"fs1","name":"codimd","value":"pw-EG42-rotated"}`, "pw-EG42-rotated"},
		{"secret delete", http.MethodDelete, "/admin/secrets?workspace=fs1&name=unrelated", "", fsPassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newFSUpstream(t, fsCookie, false)
			s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", url: u.srv.URL})
			if _, out, raw := fsCall(t, s, "fs1", "codimd.note", nil); out["id"] != "n1" {
				t.Fatalf("first call: %s", raw)
			}
			fsWantCounts(t, u, 1, 1)
			if w := adminDo(s, tc.method, tc.path, tc.body); w.Code != 200 {
				t.Fatalf("admin %s: %d %s", tc.path, w.Code, w.Body.String())
			}
			if _, out, raw := fsCall(t, s, "fs1", "codimd.note", nil); out["id"] != "n1" {
				t.Fatalf("call after admin: %s", raw)
			}
			fsWantCounts(t, u, 2, 2)
			if got := u.passwords[1]; got != tc.password {
				t.Fatalf("second login used password %q, want %q", got, tc.password)
			}
		})
	}
}

func TestFormSessionEnvSelection(t *testing.T) {
	a, b := newFSUpstream(t, fsCookie, false), newFSUpstream(t, fsCookie, false)
	s := newFormSessionServer(t, fsStore("fs1"),
		fsEnv{ws: "fs1", site: "codimd", env: "a", url: a.srv.URL},
		fsEnv{ws: "fs1", site: "codimd", env: "b", url: b.srv.URL},
	)
	code, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
	if code != http.StatusForbidden || out["rule"] != "validation" {
		t.Fatalf("want 403 validation: %d %s", code, raw)
	}
	fsWantCounts(t, a, 0, 0)
	fsWantCounts(t, b, 0, 0)
}

func TestFormSessionEnvFields(t *testing.T) {
	refused := func(t *testing.T, fields func(url string) string, wantText string) {
		u := newFSUpstream(t, fsCookie, false)
		s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod", fields: fields(u.srv.URL)})
		_, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
		fsWantError(t, out, raw)
		if !strings.Contains(raw, wantText) {
			t.Fatalf("want %q in %s", wantText, raw)
		}
		fsWantCounts(t, u, 0, 0)
	}
	for _, base := range []string{"", "-"} {
		base := base
		t.Run("base_url "+fmt.Sprintf("%q", base), func(t *testing.T) {
			refused(t, func(string) string {
				return fmt.Sprintf("base_url: %q, user: bot@example.com, secret_ref: \"secret://fs1/codimd\"", base)
			}, `\"prod\"`)
		})
	}
	t.Run("no user", func(t *testing.T) {
		refused(t, func(url string) string {
			return fmt.Sprintf("base_url: %q, secret_ref: \"secret://fs1/codimd\"", url)
		}, "needs user and secret_ref")
	})
	t.Run("no secret_ref", func(t *testing.T) {
		refused(t, func(url string) string {
			return fmt.Sprintf("base_url: %q, user: bot@example.com", url)
		}, "needs user and secret_ref")
	})
	t.Run("proxy_host", func(t *testing.T) {
		u := newFSUpstream(t, fsCookie, false)
		s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod",
			fields: "base_url: \"http://codimd.invalid\", proxy_host: 127.0.0.1, proxy_port: " + u.port() + ", user: bot@example.com, secret_ref: \"secret://fs1/codimd\""})
		if _, out, raw := fsCall(t, s, "fs1", "codimd.note", nil); out["id"] != "n1" {
			t.Fatalf("want success through the proxy host: %s", raw)
		}
		fsWantCounts(t, u, 1, 1)
	})
	for _, insecure := range []bool{true, false} {
		insecure := insecure
		t.Run(fmt.Sprintf("insecure_skip_verify %v", insecure), func(t *testing.T) {
			u := newFSUpstream(t, fsCookie, true)
			s := newFormSessionServer(t, fsStore("fs1"), fsEnv{ws: "fs1", site: "codimd", env: "prod",
				fields: fmt.Sprintf("base_url: %q, insecure_skip_verify: %v, user: bot@example.com, secret_ref: \"secret://fs1/codimd\"", u.srv.URL, insecure)})
			_, out, raw := fsCall(t, s, "fs1", "codimd.note", nil)
			if insecure {
				if out["id"] != "n1" {
					t.Fatalf("want success: %s", raw)
				}
				fsWantCounts(t, u, 1, 1)
				return
			}
			fsWantError(t, out, raw)
			fsWantCounts(t, u, 0, 0)
		})
	}
}
