package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/email"
	"github.com/external_gateway/internal/connectors/email/emailtest"
)

var (
	emailTLSOnce sync.Once
	emailTLS     *emailtest.TLS
	emailTLSErr  error
)

// testEmailTLS is the package's one generated certificate (SAN 127.0.0.1).
func testEmailTLS(t *testing.T) *emailtest.TLS {
	t.Helper()
	emailTLSOnce.Do(func() { emailTLS, emailTLSErr = emailtest.NewTLS() })
	if emailTLSErr != nil {
		t.Fatal(emailTLSErr)
	}
	return emailTLS
}

func TestEmailConfigFromEnv(t *testing.T) {
	for _, authType := range []string{"oauth2", "basic", ""} {
		for _, port := range []int{465, 587, 0} {
			env := &config.Environment{Host: "imap.example", SmtpHost: "smtp.example", SmtpPort: port,
				User: "me@example.com", Auth: config.AuthConfig{Type: authType}}
			cfg := emailConfig(env, []byte("secret"), nil)
			wantMode := email.AuthBasic
			if authType == "oauth2" {
				wantMode = email.AuthOAuth2
			}
			wantPort := port
			if port == 0 {
				wantPort = 587
			}
			name := authType + "/" + strconv.Itoa(port)
			if cfg.AuthMode != wantMode {
				t.Errorf("%s: AuthMode = %q, want %q", name, cfg.AuthMode, wantMode)
			}
			if cfg.SmtpPort != wantPort || cfg.SmtpImplicitTLS != (wantPort == 465) {
				t.Errorf("%s: SmtpPort %d SmtpImplicitTLS %v, want %d %v", name, cfg.SmtpPort, cfg.SmtpImplicitTLS, wantPort, wantPort == 465)
			}
			if cfg.ImapHost != "imap.example" || cfg.ImapPort != 993 || !cfg.ImapTLS || cfg.SmtpHost != "smtp.example" ||
				cfg.User != "me@example.com" || cfg.Password != "secret" || cfg.RootCAs != nil {
				t.Errorf("%s: unexpected config %+v", name, cfg)
			}
		}
	}
}

// tokenEndpoint is an httptest OAuth token endpoint counting its calls.
type tokenEndpoint struct {
	srv    *httptest.Server
	calls  atomic.Int32
	status int // 0 = 200 with access_token new, refresh_token r2
}

func newTokenEndpoint(t *testing.T, status int) *tokenEndpoint {
	te := &tokenEndpoint{status: status}
	te.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		te.calls.Add(1)
		if te.status != 0 {
			w.WriteHeader(te.status)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new","refresh_token":"r2","expires_in":3599}`))
	}))
	t.Cleanup(te.srv.Close)
	return te
}

const hotmailOps = "operations:\n" +
	"  hotmail.list_mailboxes:\n    connector: email\n    site: hotmail\n    params:\n      env:\n        type: string\n        required: true\n" +
	"    response:\n      allow_fields:\n        - mailboxes[].name\n"

// hotmailHarness is a credHarness with one hotmail/prod env on the given IMAP and
// SMTP ports; tokenURL "" leaves out the oauth block.
func hotmailHarness(t *testing.T, authType string, imapPort, smtpPort int, tokenURL string) *credHarness {
	env := "            host: 127.0.0.1\n            imap_port: " + strconv.Itoa(imapPort) + "\n" +
		"            smtp_port: " + strconv.Itoa(smtpPort) + "\n" +
		"            user: me@example.com\n            auth:\n              type: " + authType + "\n" +
		"            secret_ref: secret://w/hotmail\n"
	if tokenURL != "" {
		env += "            oauth:\n              token_url: " + tokenURL + "\n              client_id: cid\n" +
			"              refresh_secret_ref: secret://w/hotmail-refresh\n"
	}
	h := newCredHarness(t, map[string]string{"w": "      hotmail:\n        environments:\n          prod:\n" + env},
		[]string{"hotmail.list_mailboxes"}, hotmailOps)
	h.s.emailRootCAs = testEmailTLS(t).Roots
	h.store.Set("w", "hotmail-refresh", []byte("r1"))
	return h
}

func (h *credHarness) stored(t *testing.T, name string) string {
	t.Helper()
	v, err := h.store.Resolve("w", name)
	if err != nil {
		t.Fatal(err)
	}
	return string(v)
}

func (h *credHarness) env(t *testing.T) *config.Environment {
	t.Helper()
	env := h.s.cfg().Workspaces["w"].Sites["hotmail"].Environments["prod"]
	return &env
}

func (h *credHarness) listMailboxes(t *testing.T) error {
	t.Helper()
	return h.s.withEmail(h.env(t), "w", func(cfg *email.Config) error {
		_, err := h.s.emailConn.ListMailboxes(cfg)
		return err
	})
}

func TestEmailOAuth2RefreshRetry(t *testing.T) {
	tl := testEmailTLS(t)

	t.Run("refresh then retry succeeds", func(t *testing.T) {
		imap := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: tl.Server, ValidToken: "new"})
		te := newTokenEndpoint(t, 0)
		h := hotmailHarness(t, "oauth2", imap.Port, emailtest.ClosedPort(t), te.srv.URL)
		h.store.Set("w", "hotmail", []byte("old"))
		r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/w/op/hotmail.list_mailboxes", strings.NewReader(`{"env":"prod"}`))
		r.Header.Set("Authorization", "Bearer "+credAgentTok)
		w := httptest.NewRecorder()
		h.s.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "INBOX") {
			t.Fatalf("op: %d %s", w.Code, w.Body.String())
		}
		if n := te.calls.Load(); n != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", n)
		}
		if n := imap.AuthAttempts(); n != 2 {
			t.Fatalf("IMAP auth attempts = %d, want 2 (old rejected, new accepted)", n)
		}
		if got := h.stored(t, "hotmail"); got != "new" {
			t.Fatalf("stored access token = %q, want new", got)
		}
		if got := h.stored(t, "hotmail-refresh"); got != "r2" {
			t.Fatalf("stored refresh token = %q, want r2", got)
		}
	})

	t.Run("failed refresh", func(t *testing.T) {
		imap := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: tl.Server, ValidToken: "new"})
		te := newTokenEndpoint(t, http.StatusBadRequest)
		h := hotmailHarness(t, "oauth2", imap.Port, emailtest.ClosedPort(t), te.srv.URL)
		h.store.Set("w", "hotmail", []byte("old"))
		err := h.listMailboxes(t)
		if !errors.Is(err, email.ErrAuthRejected) || !strings.Contains(err.Error(), "OAuth token refresh failed") {
			t.Fatalf("want ErrAuthRejected with refresh failure text, got %v", err)
		}
		for _, tok := range []string{"old", "new"} {
			if strings.Contains(err.Error(), tok) {
				t.Fatalf("error text leaks token %q: %v", tok, err)
			}
		}
		if n := te.calls.Load(); n != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", n)
		}
		if n := imap.AuthAttempts(); n != 1 {
			t.Fatalf("IMAP auth attempts = %d, want 1", n)
		}
		if got := h.stored(t, "hotmail"); got != "old" {
			t.Fatalf("stored access token changed to %q", got)
		}
	})

	t.Run("rejected again after refresh: retry's error, one refresh", func(t *testing.T) {
		imap := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: tl.Server, ValidToken: "newer"})
		te := newTokenEndpoint(t, 0)
		h := hotmailHarness(t, "oauth2", imap.Port, emailtest.ClosedPort(t), te.srv.URL)
		h.store.Set("w", "hotmail", []byte("old"))
		err := h.listMailboxes(t)
		if !errors.Is(err, email.ErrAuthRejected) || strings.Contains(err.Error(), "refresh failed") {
			t.Fatalf("want the retry's rejection, got %v", err)
		}
		if n := te.calls.Load(); n != 1 {
			t.Fatalf("token endpoint calls = %d, want 1", n)
		}
		if n := imap.AuthAttempts(); n != 2 {
			t.Fatalf("IMAP auth attempts = %d, want 2", n)
		}
	})

	t.Run("basic auth rejected: no refresh", func(t *testing.T) {
		imap := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: tl.Server, ValidToken: "new"})
		te := newTokenEndpoint(t, 0)
		h := hotmailHarness(t, "basic", imap.Port, emailtest.ClosedPort(t), te.srv.URL)
		h.store.Set("w", "hotmail", []byte("wrong-password"))
		if err := h.listMailboxes(t); !errors.Is(err, email.ErrAuthRejected) {
			t.Fatalf("want a rejected LOGIN, got %v", err)
		}
		if n := te.calls.Load(); n != 0 {
			t.Fatalf("token endpoint calls = %d, want 0", n)
		}
	})

	t.Run("no oauth block: no refresh", func(t *testing.T) {
		imap := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: tl.Server, ValidToken: "new"})
		te := newTokenEndpoint(t, 0)
		h := hotmailHarness(t, "oauth2", imap.Port, emailtest.ClosedPort(t), "")
		h.store.Set("w", "hotmail", []byte("old"))
		if err := h.listMailboxes(t); !errors.Is(err, email.ErrAuthRejected) {
			t.Fatalf("want ErrAuthRejected, got %v", err)
		}
		if n := te.calls.Load(); n != 0 {
			t.Fatalf("token endpoint calls = %d, want 0", n)
		}
	})

	t.Run("IMAP port closed: no refresh", func(t *testing.T) {
		te := newTokenEndpoint(t, 0)
		h := hotmailHarness(t, "oauth2", emailtest.ClosedPort(t), emailtest.ClosedPort(t), te.srv.URL)
		h.store.Set("w", "hotmail", []byte("old"))
		if err := h.listMailboxes(t); err == nil || errors.Is(err, email.ErrAuthRejected) {
			t.Fatalf("want a dial error, got %v", err)
		}
		if n := te.calls.Load(); n != 0 {
			t.Fatalf("token endpoint calls = %d, want 0", n)
		}
	})
}

func TestDiagEmailOAuth2(t *testing.T) {
	tl := testEmailTLS(t)
	diag := func(t *testing.T, h *credHarness) map[string]interface{} {
		t.Helper()
		w := adminDo(h.s, http.MethodPost, "/admin/diag", `{"workspace":"w","site":"hotmail"}`)
		if w.Code != 200 {
			t.Fatalf("/admin/diag: %d %s", w.Code, w.Body.String())
		}
		var out map[string]map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out["prod"]
	}
	for _, tc := range []struct {
		name      string
		token     string
		wantCalls int32
	}{
		{"valid token", "new", 0},
		{"expired token refreshed once", "old", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imap := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: tl.Server, ValidToken: "new"})
			smtp := emailtest.StartSMTP(t, emailtest.SMTPOptions{TLS: tl.Server, ValidToken: "new"})
			te := newTokenEndpoint(t, 0)
			h := hotmailHarness(t, "oauth2", imap.Port, smtp.Port, te.srv.URL)
			h.store.Set("w", "hotmail", []byte(tc.token))
			res := diag(t, h)
			if res["imap_login"] != "ok" || res["smtp_login"] != "ok" {
				t.Fatalf("diag = %v, want imap_login and smtp_login ok", res)
			}
			if n := te.calls.Load(); n != tc.wantCalls {
				t.Fatalf("token endpoint calls = %d, want %d", n, tc.wantCalls)
			}
			if got := smtp.XOAuth2(); len(got) != 1 || !strings.Contains(got[0], "auth=Bearer new\x01") {
				t.Fatalf("SMTP XOAUTH2 = %q, want one attempt with the new token", got)
			}
		})
	}
}
