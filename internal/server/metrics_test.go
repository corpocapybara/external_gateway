package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/external_gateway/internal/audit"
	"github.com/external_gateway/internal/metrics"
)

// EG0066: GET /admin/metrics reports per workspace+op counts and latency.

const metricsOps = "operations:\n" +
	"  api.get:\n    connector: http-rest\n    site: api\n    method: GET\n    path: /thing\n" +
	"  api.need:\n    connector: http-rest\n    site: api\n    method: GET\n    path: /thing\n" +
	"    params:\n      n:\n        type: string\n        required: true\n" +
	"  api.secret:\n    connector: http-rest\n    site: api\n    method: GET\n    path: /thing\n"

// metricsUpstream answers every call with the status and body set last.
type metricsUpstream struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	body   string
}

func newMetricsUpstream(t *testing.T) *metricsUpstream {
	u := &metricsUpstream{status: 200, body: `{"ok":true}`}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		status, body := u.status, u.body
		u.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *metricsUpstream) set(status int, body string) {
	u.mu.Lock()
	u.status, u.body = status, body
	u.mu.Unlock()
}

// metricsHarness is a server with workspace "m" whose api site points at u, the
// role granting api.get and api.need (api.secret exists but is not granted).
func metricsHarness(t *testing.T, base string) *credHarness {
	sites := "      api:\n        environments:\n          prod: {base_url: " + base + "}\n"
	return newCredHarness(t, map[string]string{"m": sites}, []string{"api.get", "api.need"}, metricsOps)
}

// opCall sends one op request through ServeHTTP and returns the status.
func opCall(h *credHarness, tok, ws, op, query string, body io.Reader) int {
	if body == nil {
		body = strings.NewReader(`{}`)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+ws+"/op/"+op+query, body)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	return w.Code
}

func getMetrics(t *testing.T, h *credHarness) metrics.Snapshot {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/admin/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+adminTok)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET /admin/metrics: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	// The field names are the API: decode into a plain map first.
	var raw map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"started_at", "uptime_seconds", "denied_unattributed", "process", "ops"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("metrics JSON lacks %q: %s", k, w.Body.String())
		}
	}
	var snap metrics.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func opEntry(snap metrics.Snapshot, ws, op string) *metrics.OpStats {
	for i := range snap.Ops {
		if snap.Ops[i].Workspace == ws && snap.Ops[i].Op == op {
			return &snap.Ops[i]
		}
	}
	return nil
}

type opWant struct{ requests, denied, rejected, errors, count uint64 }

func checkOp(t *testing.T, snap metrics.Snapshot, ws, op string, want opWant) *metrics.OpStats {
	t.Helper()
	e := opEntry(snap, ws, op)
	if e == nil {
		t.Fatalf("%s/%s: no entry in %+v", ws, op, snap.Ops)
	}
	got := opWant{e.Requests, e.Denied, e.Rejected, e.Errors, e.LatencyMS.Count}
	if got != want {
		t.Fatalf("%s/%s: got %+v, want %+v", ws, op, got, want)
	}
	return e
}

// lastAllowLatency reads the newest allow line for op from the audit file.
func lastAllowLatency(t *testing.T, op string) int64 {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(audit.GetLogger().Dir(), "audit_*.log"))
	var lat int64 = -1
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var rec audit.Record
			if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Operation == op && rec.Decision == "allow" && rec.Workspace == "m" {
				lat = rec.LatencyMS
			}
		}
		fh.Close()
	}
	if lat < 0 {
		t.Fatalf("no audit allow line for %s", op)
	}
	return lat
}

func TestAdminMetricsAuth(t *testing.T) {
	h := metricsHarness(t, "http://127.0.0.1:1")
	send := func(method, tok string) (int, string) {
		r := httptest.NewRequest(method, "/admin/metrics", nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		h.s.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, _ := send(http.MethodGet, ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := send(http.MethodGet, "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := send(http.MethodPost, ""); code != http.StatusUnauthorized {
		t.Fatalf("POST no token: %d, want 401 (auth before method)", code)
	}
	if code, _ := send(http.MethodPost, adminTok); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST admin token: %d", code)
	}
	if code, body := send(http.MethodGet, adminTok); code != 200 || !strings.Contains(body, `"ops":[]`) {
		t.Fatalf("GET admin token: %d %s", code, body)
	}
	if code, body := send(http.MethodGet, ""); code == 200 || strings.Contains(body, "ops") {
		t.Fatalf("unauthenticated GET leaked: %d %s", code, body)
	}

	r := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "GET /admin/metrics") {
		t.Fatalf("admin index lacks the endpoint: %s", w.Body.String())
	}

	// admin_token_sha256 unset: fail closed even with the old admin token.
	h.s.cfg().AdminTokenSHA = ""
	if code, _ := send(http.MethodGet, adminTok); code != http.StatusUnauthorized {
		t.Fatalf("admin token unset: %d", code)
	}
}

func TestAdminMetricsCountsOps(t *testing.T) {
	u := newMetricsUpstream(t)
	h := metricsHarness(t, u.srv.URL)
	tok := credAgentTok

	// allowed, 200 JSON
	if c := opCall(h, tok, "m", "api.get", "", nil); c != 200 {
		t.Fatalf("allowed: %d", c)
	}
	e := checkOp(t, getMetrics(t, h), "m", "api.get", opWant{1, 0, 0, 0, 1})
	if want := lastAllowLatency(t, "api.get"); e.LatencyMS.Max != want {
		t.Fatalf("latency max %d, audit latency_ms %d", e.LatencyMS.Max, want)
	}

	// 200 non-JSON and 204 empty: executed, not errors
	u.set(200, "plain text")
	opCall(h, tok, "m", "api.get", "", nil)
	u.set(204, "")
	opCall(h, tok, "m", "api.get", "", nil)
	checkOp(t, getMetrics(t, h), "m", "api.get", opWant{3, 0, 0, 0, 3})

	// upstream 500: executed and an error
	u.set(500, `{"error":"boom"}`)
	opCall(h, tok, "m", "api.get", "", nil)
	checkOp(t, getMetrics(t, h), "m", "api.get", opWant{4, 0, 0, 1, 4})
	u.set(200, `{"ok":true}`)

	// invalid JSON 400 and a body over maxOpBodyBytes 413: rejected
	if c := opCall(h, tok, "m", "api.get", "", strings.NewReader("{not json")); c != http.StatusBadRequest {
		t.Fatalf("invalid JSON: %d", c)
	}
	big := io.MultiReader(strings.NewReader(`{"x":"`), io.LimitReader(repeatReader('a'), maxOpBodyBytes+1))
	if c := opCall(h, tok, "m", "api.get", "", big); c != http.StatusRequestEntityTooLarge {
		t.Fatalf("big body: %d", c)
	}
	checkOp(t, getMetrics(t, h), "m", "api.get", opWant{6, 0, 2, 1, 4})

	// grant deny (api.secret exists, not granted) and validation deny (api.need without n)
	if c := opCall(h, tok, "m", "api.secret", "", nil); c != http.StatusForbidden {
		t.Fatalf("grant deny: %d", c)
	}
	if c := opCall(h, tok, "m", "api.need", "", nil); c != http.StatusForbidden {
		t.Fatalf("validation deny: %d", c)
	}
	snap := getMetrics(t, h)
	checkOp(t, snap, "m", "api.secret", opWant{1, 1, 0, 0, 0})
	checkOp(t, snap, "m", "api.need", opWant{1, 1, 0, 0, 0})

	// connector error: the upstream is gone
	u.srv.Close()
	opCall(h, tok, "m", "api.get", "", nil)
	checkOp(t, getMetrics(t, h), "m", "api.get", opWant{7, 0, 2, 2, 5})

	// unattributed: unknown ops, unknown workspace, bad token
	for _, op := range []string{"api.nope1", "api.nope2", "zzz.nope3"} {
		if c := opCall(h, tok, "m", op, "", nil); c != http.StatusNotFound {
			t.Fatalf("unknown op %s: %d", op, c)
		}
	}
	if c := opCall(h, tok, "nows", "api.get", "", nil); c != http.StatusForbidden {
		t.Fatalf("unknown workspace: %d", c)
	}
	if c := opCall(h, "bad-token", "m", "api.get", "", nil); c != http.StatusForbidden {
		t.Fatalf("bad token: %d", c)
	}
	before := getMetrics(t, h)
	if before.DeniedUnattributed != 5 {
		t.Fatalf("denied_unattributed %d, want 5", before.DeniedUnattributed)
	}

	// dry runs count nothing: bad token, allowed op, grant-denied op
	opCall(h, "bad-token", "m", "api.get", "?dry_run=true", nil)
	opCall(h, tok, "m", "api.get", "?dry_run=true", nil)
	opCall(h, tok, "m", "api.secret", "?dry_run=true", nil)
	after := getMetrics(t, h)
	if after.DeniedUnattributed != 5 {
		t.Fatalf("dry runs changed denied_unattributed: %d", after.DeniedUnattributed)
	}

	if len(after.Ops) != 3 {
		t.Fatalf("ops %+v, want only api.get, api.need, api.secret", after.Ops)
	}
	for i, o := range after.Ops {
		if o != before.Ops[i] {
			t.Fatalf("dry run changed %s: %+v -> %+v", o.Op, before.Ops[i], o)
		}
		if o.Requests != o.Denied+o.Rejected+o.LatencyMS.Count {
			t.Fatalf("%s: requests %d != denied+rejected+count %+v", o.Op, o.Requests, o)
		}
	}
	if after.Process.Goroutines < 1 || after.StartedAt.IsZero() {
		t.Fatalf("process/start missing: %+v", after)
	}
}

// repeatReader is an endless stream of one byte.
type repeatReader byte

func (b repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

func TestAdminMetricsSurvivesReload(t *testing.T) {
	u := newMetricsUpstream(t)
	h := metricsHarness(t, u.srv.URL)
	if c := opCall(h, credAgentTok, "m", "api.need", "", nil); c != http.StatusForbidden {
		t.Fatalf("validation deny: %d", c)
	}
	opCall(h, credAgentTok, "m", "api.get", "", nil)
	before := getMetrics(t, h)

	// Drop api.need from the role and the ops file, then reload.
	cfgPath := filepath.Join(h.dir, "config.yaml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	newCfg := strings.Replace(string(cfg), "[api.get, api.need]", "[api.get]", 1)
	if newCfg == string(cfg) {
		t.Fatalf("role line not found in %s", cfg)
	}
	ops := strings.Replace(metricsOps,
		"  api.need:\n    connector: http-rest\n    site: api\n    method: GET\n    path: /thing\n"+
			"    params:\n      n:\n        type: string\n        required: true\n", "", 1)
	if err := os.WriteFile(cfgPath, []byte(newCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "ops.yaml"), []byte(ops), 0o600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	r.Header.Set("Authorization", "Bearer "+adminTok)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("reload: %d %s", w.Code, w.Body.String())
	}
	if _, ok := h.s.registry().Get("api.need"); ok {
		t.Fatal("reload kept api.need")
	}

	mid := getMetrics(t, h)
	if len(mid.Ops) != len(before.Ops) || mid.DeniedUnattributed != before.DeniedUnattributed {
		t.Fatalf("reload changed metrics: %+v -> %+v", before, mid)
	}
	for i := range mid.Ops {
		if mid.Ops[i] != before.Ops[i] {
			t.Fatalf("reload changed %+v -> %+v", before.Ops[i], mid.Ops[i])
		}
	}

	// The removed op is now unknown: unattributed, its old entry unchanged.
	if c := opCall(h, credAgentTok, "m", "api.need", "", nil); c != http.StatusNotFound {
		t.Fatalf("removed op: %d", c)
	}
	after := getMetrics(t, h)
	if after.DeniedUnattributed != before.DeniedUnattributed+1 {
		t.Fatalf("denied_unattributed %d, want %d", after.DeniedUnattributed, before.DeniedUnattributed+1)
	}
	checkOp(t, after, "m", "api.need", opWant{1, 1, 0, 0, 0})
}
