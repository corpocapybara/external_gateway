package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/external_gateway/internal/connectors/email"
	"github.com/external_gateway/internal/connectors/email/emailtest"
	"github.com/external_gateway/internal/secrets"
)

// EG0044: after a failed refresh, or a retry still rejected after a refresh, a
// rejected call skips the token endpoint for the env's oauth.refresh_cooldown.

// cdClock pins the cooldown clock at credNoon and returns a function moving it on.
func cdClock(h *credHarness) func(time.Duration) {
	var mu sync.Mutex
	at := credNoon
	h.s.oauthCooldown.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	return func(d time.Duration) { mu.Lock(); at = at.Add(d); mu.Unlock() }
}

// set changes the token the upstream accepts and its reject hook between calls.
func (u *apiUpstream) set(valid string, onReject func()) {
	u.mu.Lock()
	u.valid, u.onReject = valid, onReject
	u.mu.Unlock()
}

// cdAPIHarness is apiHarness with an optional extra line in the oauth block and
// a second env "other" (secret w/api2) on the same upstream and token endpoint.
func cdAPIHarness(t *testing.T, u *apiUpstream, tokenURL, oauthExtra string) *credHarness {
	env := func(secret string) string {
		s := "            base_url: " + u.srv.URL + "\n" +
			"            auth:\n              type: bearer\n" +
			"            secret_ref: secret://w/" + secret + "\n" +
			"            oauth:\n              token_url: " + tokenURL + "\n              client_id: cid\n" +
			"              refresh_secret_ref: secret://w/" + secret + "-refresh\n"
		if oauthExtra != "" {
			s += "              " + oauthExtra + "\n"
		}
		return s
	}
	h := newCredHarness(t, map[string]string{"w": "      api:\n        environments:\n" +
		"          prod:\n" + env("api") + "          other:\n" + env("api2")},
		[]string{"api.get"}, apiOps)
	for _, n := range []string{"api", "api2"} {
		h.store.Set("w", n, []byte("old"))
		h.store.Set("w", n+"-refresh", []byte("r1"))
	}
	return h
}

// status runs api.get on env through the full handler and returns the upstream
// status the gateway reports (or the gateway's own HTTP status when not 200).
func (h *credHarness) status(env string) int {
	r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/w/op/api.get", strings.NewReader(`{"env":"`+env+`"}`))
	r.Header.Set("Authorization", "Bearer "+credAgentTok)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	var out struct {
		UpstreamStatus int `json:"upstream_status"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.UpstreamStatus == 0 {
		return w.Code
	}
	return out.UpstreamStatus
}

// emailRejectAll is an email upstream that rejects every login, recording passwords.
func emailRejectAll(seen *[]string) func(*email.Config) error {
	return func(cfg *email.Config) error {
		*seen = append(*seen, cfg.Password)
		return email.ErrAuthRejected
	}
}

// cdEmailHarness is a hotmail harness on closed ports with access token "old".
func cdEmailHarness(t *testing.T, tokenURL string) *credHarness {
	h := hotmailHarness(t, "oauth2", emailtest.ClosedPort(t), emailtest.ClosedPort(t), tokenURL)
	h.store.Set("w", "hotmail", []byte("old"))
	return h
}

func wantTokenCalls(t *testing.T, te *tokenEndpoint, want int32, when string) {
	t.Helper()
	if n := te.calls.Load(); n != want {
		t.Fatalf("%s: token endpoint calls = %d, want %d", when, n, want)
	}
}

func wantStatus(t *testing.T, h *credHarness, env string, want int, when string) {
	t.Helper()
	if got := h.status(env); got != want {
		t.Fatalf("%s: status = %d, want %d", when, got, want)
	}
}

// coolAPI drives the prod env into a cooldown: the refresh succeeds, its retry is
// still rejected (the upstream accepts no token), then a second rejected call is
// proven to skip the token endpoint.
func coolAPI(t *testing.T, h *credHarness, te *tokenEndpoint) {
	t.Helper()
	wantStatus(t, h, "prod", 401, "first call")
	wantTokenCalls(t, te, 1, "first call")
	wantStatus(t, h, "prod", 401, "second call")
	wantTokenCalls(t, te, 1, "second call (cooling)")
}

func TestOAuthCooldownHTTPRest(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	u := newAPIUpstream(t, "none")
	h := cdAPIHarness(t, u, te.srv.URL, "")
	cdClock(h)
	coolAPI(t, h, te)
	if got := strings.Join(u.calls(), ","); got != "Bearer old,Bearer new,Bearer new" {
		t.Fatalf("upstream calls = %q, want old, new (retry), new (no retry)", got)
	}
}

func TestOAuthCooldownEmail(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	h := cdEmailHarness(t, te.srv.URL)
	cdClock(h)
	var seen []string
	err := h.s.withEmail(h.env(t), "w", emailRejectAll(&seen))
	if !errors.Is(err, email.ErrAuthRejected) || strings.Contains(err.Error(), "cooldown") {
		t.Fatalf("first call: err = %v, want the retry's ErrAuthRejected", err)
	}
	wantTokenCalls(t, te, 1, "first call")
	err = h.s.withEmail(h.env(t), "w", emailRejectAll(&seen))
	if !errors.Is(err, email.ErrAuthRejected) || !strings.Contains(err.Error(), "refresh skipped (cooldown)") {
		t.Fatalf("second call: err = %v, want ErrAuthRejected with refresh skipped (cooldown)", err)
	}
	wantTokenCalls(t, te, 1, "second call")
	if got := strings.Join(seen, ","); got != "old,new,new" {
		t.Fatalf("passwords used = %q, want old, new (retry), new (no retry)", got)
	}
}

func TestOAuthCooldownExpires(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	u := newAPIUpstream(t, "none")
	h := cdAPIHarness(t, u, te.srv.URL, "")
	advance := cdClock(h)
	coolAPI(t, h, te)
	advance(time.Minute)
	wantStatus(t, h, "prod", 401, "after 1m")
	wantTokenCalls(t, te, 1, "after 1m (cooling)")
	advance(4*time.Minute + time.Second)
	wantStatus(t, h, "prod", 401, "after 5m01s")
	wantTokenCalls(t, te, 2, "after 5m01s (refreshes again)")
}

func TestOAuthCooldownFailedRefresh(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http-rest non-200", http.StatusInternalServerError, ""},
		{"http-rest bad JSON", 0, `{"access_token":`},
		{"http-rest empty access_token", 0, `{"access_token":"","refresh_token":"r2"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTokenEndpoint(t, tc.status)
			te.body = tc.body
			u := newAPIUpstream(t, "none")
			h := cdAPIHarness(t, u, te.srv.URL, "")
			cdClock(h)
			coolAPI(t, h, te)
			if got := strings.Join(u.calls(), ","); got != "Bearer old,Bearer old" {
				t.Fatalf("upstream calls = %q, want two rejected calls, no retry", got)
			}
		})
	}
	t.Run("email", func(t *testing.T) {
		te := newTokenEndpoint(t, http.StatusInternalServerError)
		h := cdEmailHarness(t, te.srv.URL)
		cdClock(h)
		var seen []string
		err := h.s.withEmail(h.env(t), "w", emailRejectAll(&seen))
		if !errors.Is(err, email.ErrAuthRejected) || !strings.Contains(err.Error(), "OAuth token refresh failed") {
			t.Fatalf("first call: err = %v, want OAuth token refresh failed", err)
		}
		err = h.s.withEmail(h.env(t), "w", emailRejectAll(&seen))
		if !errors.Is(err, email.ErrAuthRejected) || !strings.Contains(err.Error(), "refresh skipped (cooldown)") {
			t.Fatalf("second call: err = %v, want refresh skipped (cooldown)", err)
		}
		wantTokenCalls(t, te, 1, "two calls")
	})
}

// A retry that fails for another reason (not a refused login) starts no cooldown.
func TestOAuthCooldownEmailRetryOtherError(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	h := cdEmailHarness(t, te.srv.URL)
	cdClock(h)
	netErr := errors.New("connection reset")
	var seen []string
	call := func(cfg *email.Config) error {
		seen = append(seen, cfg.Password)
		if len(seen) == 2 {
			return netErr
		}
		return email.ErrAuthRejected
	}
	if err := h.s.withEmail(h.env(t), "w", call); !errors.Is(err, netErr) {
		t.Fatalf("first call: err = %v, want the retry's network error", err)
	}
	wantTokenCalls(t, te, 1, "first call")
	h.s.withEmail(h.env(t), "w", call) // rejected with "new": refreshes, retry rejected: cooldown
	wantTokenCalls(t, te, 2, "second call (no cooldown after a non-auth retry error)")
	h.s.withEmail(h.env(t), "w", call)
	wantTokenCalls(t, te, 2, "third call (cooling)")
}

func TestOAuthCooldownConcurrentFailedRefresh(t *testing.T) {
	te := newTokenEndpoint(t, http.StatusInternalServerError)
	u := newAPIUpstream(t, "none")
	// Both calls are rejected with "old" before either starts its refresh; the
	// second waits on oauthRefreshMu while the first refresh fails.
	var arrived sync.WaitGroup
	arrived.Add(2)
	both := make(chan struct{})
	go func() { arrived.Wait(); close(both) }()
	var rejected atomic.Int32
	u.onReject = func() {
		if rejected.Add(1) > 2 {
			return
		}
		arrived.Done()
		select {
		case <-both:
		case <-time.After(10 * time.Second): // guard against a hang, not a timing assumption
		}
	}
	h := cdAPIHarness(t, u, te.srv.URL, "")
	cdClock(h)
	codes := make([]int, 2)
	var done sync.WaitGroup
	for i := range codes {
		done.Add(1)
		go func(i int) { defer done.Done(); codes[i] = h.status("prod") }(i)
	}
	done.Wait()
	select {
	case <-both:
	default:
		t.Fatal("the two calls never reached the upstream together")
	}
	if codes[0] != 401 || codes[1] != 401 {
		t.Fatalf("statuses = %v, want both 401", codes)
	}
	wantTokenCalls(t, te, 1, "two concurrent rejections")
}

func TestOAuthCooldownPerKey(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	u := newAPIUpstream(t, "none")
	h := cdAPIHarness(t, u, te.srv.URL, "")
	cdClock(h)
	coolAPI(t, h, te) // key w/api cooling
	wantStatus(t, h, "other", 401, "env other")
	wantTokenCalls(t, te, 2, "env other (own key, refreshes)")
}

func TestOAuthCooldownNoneAfterSuccess(t *testing.T) {
	t.Run("refresh then success", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		u := newAPIUpstream(t, "new")
		h := cdAPIHarness(t, u, te.srv.URL, "")
		cdClock(h)
		wantStatus(t, h, "prod", 200, "first call")
		wantTokenCalls(t, te, 1, "first call")
		u.set("none", nil)
		wantStatus(t, h, "prod", 401, "second call")
		wantTokenCalls(t, te, 2, "second call (no cooldown after success)")
		wantStatus(t, h, "prod", 401, "third call")
		wantTokenCalls(t, te, 2, "third call (cooling after the rejected retry)")
	})
	t.Run("skipped refresh then success", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		u := newAPIUpstream(t, "fresh")
		var once sync.Once
		u.onReject = func() {
			once.Do(func() {
				if err := secrets.GetResolver().Set("w", "api", []byte("fresh")); err != nil {
					t.Errorf("storing fresh token: %v", err)
				}
			})
		}
		h := cdAPIHarness(t, u, te.srv.URL, "")
		cdClock(h)
		wantStatus(t, h, "prod", 200, "first call")
		wantTokenCalls(t, te, 0, "first call (skipped refresh)")
		u.set("none", nil)
		wantStatus(t, h, "prod", 401, "second call")
		wantTokenCalls(t, te, 1, "second call (no cooldown after success)")
		wantStatus(t, h, "prod", 401, "third call")
		wantTokenCalls(t, te, 1, "third call (cooling)")
	})
}

func TestOAuthCooldownRetryUnresolved(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	u := newAPIUpstream(t, "none")
	h := cdAPIHarness(t, u, te.srv.URL, "")
	cdClock(h)
	// Armed from inside the refresh: the token is stored, but the caller's
	// re-read fails, so no retry is sent.
	te.onCall = func() { h.store.setFailResolve("w", "api", true) }
	wantStatus(t, h, "prod", 401, "first call")
	wantTokenCalls(t, te, 1, "first call")
	te.onCall = nil
	h.store.setFailResolve("w", "api", false)
	if got := strings.Join(u.calls(), ","); got != "Bearer old" {
		t.Fatalf("upstream calls = %q, want only the first (no retry)", got)
	}
	wantStatus(t, h, "prod", 401, "second call")
	wantTokenCalls(t, te, 2, "second call (no cooldown without a retry)")
	wantStatus(t, h, "prod", 401, "third call")
	wantTokenCalls(t, te, 2, "third call (cooling after the rejected retry)")
}

func TestOAuthCooldownAfterSkippedRefreshRejected(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	u := newAPIUpstream(t, "none")
	var once sync.Once
	u.onReject = func() {
		once.Do(func() {
			if err := secrets.GetResolver().Set("w", "api", []byte("fresh")); err != nil {
				t.Errorf("storing fresh token: %v", err)
			}
		})
	}
	h := cdAPIHarness(t, u, te.srv.URL, "")
	cdClock(h)
	wantStatus(t, h, "prod", 401, "first call")
	wantStatus(t, h, "prod", 401, "second call")
	wantTokenCalls(t, te, 0, "two calls")
	if got := strings.Join(u.calls(), ","); got != "Bearer old,Bearer fresh,Bearer fresh" {
		t.Fatalf("upstream calls = %q, want old, fresh (retry), fresh (no retry)", got)
	}
}

func TestOAuthCooldownChangedTokenBeatsCooldown(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	u := newAPIUpstream(t, "none")
	h := cdAPIHarness(t, u, te.srv.URL, "")
	cdClock(h)
	coolAPI(t, h, te)
	// A token fixed outside the admin paths while the call is in flight: the
	// stored-token check runs before the cooldown check.
	u.set("fixed", func() {
		if err := secrets.GetResolver().Set("w", "api", []byte("fixed")); err != nil {
			t.Errorf("storing fixed token: %v", err)
		}
	})
	wantStatus(t, h, "prod", 200, "call with the token changed")
	wantTokenCalls(t, te, 1, "call with the token changed")
}

func TestOAuthCooldownReset(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"secret set", http.MethodPost, "/admin/secrets", `{"workspace":"w","name":"unrelated","value":"x"}`},
		{"secret delete", http.MethodDelete, "/admin/secrets?workspace=w&name=unrelated", ""},
		{"reload", http.MethodPost, "/admin/reload", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTokenEndpoint(t, 0)
			u := newAPIUpstream(t, "none")
			h := cdAPIHarness(t, u, te.srv.URL, "")
			cdClock(h)
			coolAPI(t, h, te)
			if w := adminDo(h.s, tc.method, tc.path, tc.body); w.Code != 200 {
				t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
			}
			wantStatus(t, h, "prod", 401, "after "+tc.name)
			wantTokenCalls(t, te, 2, "after "+tc.name+" (refreshes again)")
		})
	}
}

// gate blocks the goroutine that calls wait until release; reached reports it.
type gate struct {
	reached, release chan struct{}
	once             sync.Once
}

func newGate() *gate { return &gate{reached: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) wait() {
	g.once.Do(func() {
		close(g.reached)
		select {
		case <-g.release:
		case <-time.After(10 * time.Second): // guard against a hang, not a timing assumption
		}
	})
}

func TestOAuthCooldownClearDuringRefresh(t *testing.T) {
	clear := func(t *testing.T, h *credHarness) {
		if w := adminDo(h.s, http.MethodPost, "/admin/reload", ""); w.Code != 200 {
			t.Fatalf("reload: %d %s", w.Code, w.Body.String())
		}
	}
	// run starts call in a goroutine, waits until it blocks at g, clears (when
	// asked) and lets it finish.
	run := func(t *testing.T, h *credHarness, g *gate, withClear bool, call func()) {
		done := make(chan struct{})
		go func() { defer close(done); call() }()
		select {
		case <-g.reached:
		case <-time.After(10 * time.Second):
			t.Fatal("call never reached the gate")
		}
		if withClear {
			clear(t, h)
		}
		close(g.release)
		<-done
	}

	for _, withClear := range []bool{true, false} {
		// After the clear the next rejected call refreshes again (no cooldown left);
		// without it (control) the cooldown holds.
		want := int32(1)
		name := "control, no clear"
		if withClear {
			want, name = 2, "clear"
		}
		t.Run("http-rest failing refresh, "+name, func(t *testing.T) {
			te := newTokenEndpoint(t, http.StatusInternalServerError)
			g := newGate()
			te.onCall = g.wait
			u := newAPIUpstream(t, "none")
			h := cdAPIHarness(t, u, te.srv.URL, "")
			cdClock(h)
			run(t, h, g, withClear, func() { h.status("prod") })
			te.onCall = nil
			wantStatus(t, h, "prod", 401, "next call")
			wantTokenCalls(t, te, want, "next call")
		})
		t.Run("http-rest retry rejected, "+name, func(t *testing.T) {
			te := newTokenEndpoint(t, 0)
			g := newGate()
			u := newAPIUpstream(t, "none")
			var rejected atomic.Int32
			u.onReject = func() {
				if rejected.Add(1) == 2 { // the retry with the refreshed token
					g.wait()
				}
			}
			h := cdAPIHarness(t, u, te.srv.URL, "")
			cdClock(h)
			run(t, h, g, withClear, func() { h.status("prod") })
			wantStatus(t, h, "prod", 401, "next call")
			wantTokenCalls(t, te, want, "next call")
		})
		t.Run("email failing refresh, "+name, func(t *testing.T) {
			te := newTokenEndpoint(t, http.StatusInternalServerError)
			g := newGate()
			te.onCall = g.wait
			h := cdEmailHarness(t, te.srv.URL)
			cdClock(h)
			var seen []string
			run(t, h, g, withClear, func() { h.s.withEmail(h.env(t), "w", emailRejectAll(&seen)) })
			te.onCall = nil
			h.s.withEmail(h.env(t), "w", emailRejectAll(&seen))
			wantTokenCalls(t, te, want, "next call")
		})
		t.Run("email retry rejected, "+name, func(t *testing.T) {
			te := newTokenEndpoint(t, 0)
			g := newGate()
			h := cdEmailHarness(t, te.srv.URL)
			cdClock(h)
			var seen []string
			call := func(cfg *email.Config) error {
				seen = append(seen, cfg.Password)
				if len(seen) == 2 { // the retry with the refreshed token
					g.wait()
				}
				return email.ErrAuthRejected
			}
			run(t, h, g, withClear, func() { h.s.withEmail(h.env(t), "w", call) })
			h.s.withEmail(h.env(t), "w", call)
			wantTokenCalls(t, te, want, "next call")
		})
	}
}

func TestOAuthCooldownWindow(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		after       time.Duration
		want        int32 // token calls after the third rejected call
	}{
		{"absent at 0", "", 0, 1},
		{"absent at 4m59s", "", 4*time.Minute + 59*time.Second, 1},
		{"absent at 5m00s", "", 5 * time.Minute, 2},
		{"absent at 5m01s", "", 5*time.Minute + time.Second, 2},
		{"2m at 1m59s", "refresh_cooldown: 2m", 2*time.Minute - time.Second, 1},
		{"2m at 2m01s", "refresh_cooldown: 2m", 2*time.Minute + time.Second, 2},
		{"0s at 0", `refresh_cooldown: "0s"`, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTokenEndpoint(t, 0)
			u := newAPIUpstream(t, "none")
			h := cdAPIHarness(t, u, te.srv.URL, tc.extra)
			advance := cdClock(h)
			wantStatus(t, h, "prod", 401, "first call")
			wantTokenCalls(t, te, 1, "first call")
			advance(tc.after)
			wantStatus(t, h, "prod", 401, "second call")
			wantTokenCalls(t, te, tc.want, "second call")
		})
	}
}
