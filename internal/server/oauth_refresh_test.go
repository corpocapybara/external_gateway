package server

import (
	"errors"
	"fmt"
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

// EG0045: refreshOAuthToken skips the token endpoint when the stored access
// token is no longer the one the rejected call used.

const apiOps = "operations:\n" +
	"  api.get:\n    connector: http-rest\n    site: api\n    method: GET\n    path: /thing\n" +
	"    params:\n      env:\n        type: string\n        required: true\n" +
	"    response:\n      allow_fields:\n        - ok\n"

// apiUpstream is an httptest bearer-auth upstream: it accepts only the token in
// valid and calls onReject (when set) for every rejected call before its 401.
type apiUpstream struct {
	srv      *httptest.Server
	mu       sync.Mutex
	valid    string
	onReject func()
	seen     []string // Authorization header of every call, in order
}

func newAPIUpstream(t *testing.T, valid string) *apiUpstream {
	u := &apiUpstream{valid: valid}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		u.mu.Lock()
		u.seen = append(u.seen, auth)
		ok, onReject := auth == "Bearer "+u.valid, u.onReject
		u.mu.Unlock()
		if !ok {
			if onReject != nil {
				onReject()
			}
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *apiUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

// apiHarness is a credHarness with one api/prod env: http-rest on u, bearer auth
// with secret w/api = "old", and an oauth block on tokenURL (refresh token w/api-refresh).
func apiHarness(t *testing.T, u *apiUpstream, tokenURL string) *credHarness {
	env := "            base_url: " + u.srv.URL + "\n" +
		"            auth:\n              type: bearer\n" +
		"            secret_ref: secret://w/api\n" +
		"            oauth:\n              token_url: " + tokenURL + "\n              client_id: cid\n" +
		"              refresh_secret_ref: secret://w/api-refresh\n"
	h := newCredHarness(t, map[string]string{"w": "      api:\n        environments:\n          prod:\n" + env},
		[]string{"api.get"}, apiOps)
	h.store.Set("w", "api", []byte("old"))
	h.store.Set("w", "api-refresh", []byte("r1"))
	return h
}

// callAPI runs api.get through the full HTTP handler and returns an error unless it succeeded.
func (h *credHarness) callAPI() error {
	r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/w/op/api.get", strings.NewReader(`{"env":"prod"}`))
	r.Header.Set("Authorization", "Bearer "+credAgentTok)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		return fmt.Errorf("op: %d %s", w.Code, w.Body.String())
	}
	return nil
}

// emailFake is the email path's fake upstream: call 1 runs onReject and is
// rejected; later calls succeed only with the password valid. It records the
// password of every call.
func emailFake(valid string, onReject func(), seen *[]string) func(*email.Config) error {
	return func(cfg *email.Config) error {
		*seen = append(*seen, cfg.Password)
		if len(*seen) == 1 {
			if onReject != nil {
				onReject()
			}
			return email.ErrAuthRejected
		}
		if cfg.Password != valid {
			return email.ErrAuthRejected
		}
		return nil
	}
}

func TestOAuthRefreshSkippedWhenTokenChanged(t *testing.T) {
	// The fake upstream stores "fresh" the way a concurrent refresh does
	// (Resolver.Set writes the installed store and forgets the cached key).
	storeFresh := func(name string) func() {
		return func() {
			if err := secrets.GetResolver().Set("w", name, []byte("fresh")); err != nil {
				panic(err)
			}
		}
	}

	t.Run("email", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		h := hotmailHarness(t, "oauth2", emailtest.ClosedPort(t), emailtest.ClosedPort(t), te.srv.URL)
		h.store.Set("w", "hotmail", []byte("old"))
		var seen []string
		if err := h.s.withEmail(h.env(t), "w", emailFake("fresh", storeFresh("hotmail"), &seen)); err != nil {
			t.Fatalf("withEmail: %v", err)
		}
		if n := te.calls.Load(); n != 0 {
			t.Fatalf("token endpoint calls = %d, want 0", n)
		}
		if strings.Join(seen, ",") != "old,fresh" {
			t.Fatalf("passwords used = %q, want old then fresh", seen)
		}
		if got := h.stored(t, "hotmail-refresh"); got != "r1" {
			t.Fatalf("refresh token = %q, want r1 (unspent)", got)
		}
	})

	t.Run("http-rest", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		u := newAPIUpstream(t, "fresh")
		u.onReject = storeFresh("api")
		h := apiHarness(t, u, te.srv.URL)
		if err := h.callAPI(); err != nil {
			t.Fatal(err)
		}
		if n := te.calls.Load(); n != 0 {
			t.Fatalf("token endpoint calls = %d, want 0", n)
		}
		if got := strings.Join(u.calls(), ","); got != "Bearer old,Bearer fresh" {
			t.Fatalf("upstream calls = %q, want old then fresh", got)
		}
		if got := h.stored(t, "api-refresh"); got != "r1" {
			t.Fatalf("refresh token = %q, want r1 (unspent)", got)
		}
	})
}

func TestOAuthRefreshWhenTokenUnchanged(t *testing.T) {
	t.Run("email", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		h := hotmailHarness(t, "oauth2", emailtest.ClosedPort(t), emailtest.ClosedPort(t), te.srv.URL)
		h.store.Set("w", "hotmail", []byte("old"))
		var seen []string
		if err := h.s.withEmail(h.env(t), "w", emailFake("new", nil, &seen)); err != nil {
			t.Fatalf("withEmail: %v", err)
		}
		if n := te.calls.Load(); n != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", n)
		}
		if strings.Join(seen, ",") != "old,new" {
			t.Fatalf("passwords used = %q, want old then new", seen)
		}
	})

	t.Run("http-rest", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		u := newAPIUpstream(t, "new")
		h := apiHarness(t, u, te.srv.URL)
		if err := h.callAPI(); err != nil {
			t.Fatal(err)
		}
		if n := te.calls.Load(); n != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", n)
		}
		if got := strings.Join(u.calls(), ","); got != "Bearer old,Bearer new" {
			t.Fatalf("upstream calls = %q, want old then new", got)
		}
		if got := h.stored(t, "api"); got != "new" {
			t.Fatalf("stored access token = %q, want new", got)
		}
		if got := h.stored(t, "api-refresh"); got != "r2" {
			t.Fatalf("stored refresh token = %q, want r2", got)
		}
	})

	t.Run("http-rest re-read fails: refresh", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		u := newAPIUpstream(t, "new")
		// Deleting through the resolver drops the cached key, so the read under the lock fails.
		u.onReject = func() {
			if err := secrets.GetResolver().Delete("w", "api"); err != nil {
				panic(err)
			}
		}
		h := apiHarness(t, u, te.srv.URL)
		if err := h.callAPI(); err != nil {
			t.Fatal(err)
		}
		if n := te.calls.Load(); n != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", n)
		}
		if got := strings.Join(u.calls(), ","); got != "Bearer old,Bearer new" {
			t.Fatalf("upstream calls = %q, want old then new", got)
		}
	})
}

func TestOAuthConcurrentRejectionsOneRefresh(t *testing.T) {
	te := newTokenEndpoint(t, 0)
	u := newAPIUpstream(t, "new")
	// The gate holds each rejected call until both have arrived with the old
	// token, so both 401s are answered before either call starts its refresh.
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
	h := apiHarness(t, u, te.srv.URL)

	errs := make([]error, 2)
	var done sync.WaitGroup
	for i := range errs {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			errs[i] = h.callAPI()
		}(i)
	}
	done.Wait()
	select {
	case <-both:
	default:
		t.Fatal("the two calls never reached the upstream together")
	}
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if n := te.calls.Load(); n != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", n)
	}
	if n := rejected.Load(); n != 2 {
		t.Fatalf("rejected upstream calls = %d, want 2", n)
	}
	if got := u.calls(); len(got) != 4 {
		t.Fatalf("upstream calls = %q, want 4 (two rejected, two retried)", got)
	}
}
