package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adminCalls is every admin endpoint with its own method and a body or query that succeeds with a valid token.
var adminCalls = []struct{ method, path, body string }{
	{http.MethodPost, "/admin/secrets", `{"workspace":"acme","name":"jira","value":"v"}`},
	{http.MethodPut, "/admin/secrets", `{"workspace":"acme","name":"jira","value":"v"}`},
	{http.MethodDelete, "/admin/secrets?workspace=acme&name=jira", ""},
	{http.MethodGet, "/admin/secrets/status?workspace=acme", ""},
	{http.MethodPost, "/admin/diag", `{"workspace":"acme","site":"jira"}`},
	{http.MethodPost, "/admin/reload", ""},
	{http.MethodGet, "/admin/metrics", ""},
}

func adminCall(s *Server, method, path, body, auth string, withAuth bool) int {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if withAuth {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w.Code
}

// TestAdminRefusesEveryCallWithoutAdminToken: with admin_token_sha256 empty, every /admin/* call is 401 whatever
// the Authorization header, and the secret store is never written (EG0054 R6).
func TestAdminRefusesEveryCallWithoutAdminToken(t *testing.T) {
	s, fs := adminServer(t)
	s.cfg().AdminTokenSHA = ""
	headers := []struct {
		name, value string
		set         bool
	}{
		{"admin bearer", "Bearer " + adminTok, true},
		{"no header", "", false},
		{"basic", "Basic YWRtaW46YWRtaW4=", true},
		{"bare bearer", "Bearer ", true},
	}
	for _, c := range adminCalls {
		for _, h := range headers {
			if code := adminCall(s, c.method, c.path, c.body, h.value, h.set); code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: code %d, want 401", c.method, c.path, h.name, code)
			}
		}
	}
	if len(fs.sets)+len(fs.deletes) != 0 {
		t.Fatalf("store touched with no admin token configured: sets %v deletes %v", fs.sets, fs.deletes)
	}

	// Positive counterpart: the same calls with the token configured and sent are not refused.
	s.cfg().AdminTokenSHA = sha(adminTok)
	for _, c := range adminCalls {
		if code := adminCall(s, c.method, c.path, c.body, "Bearer "+adminTok, true); code == http.StatusUnauthorized {
			t.Errorf("%s %s with the admin token: 401", c.method, c.path)
		}
	}
}
