package tokencheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
)

func TestDetectKind(t *testing.T) {
	cases := []struct {
		site string
		env  config.Environment
		want Kind
	}{
		{"gitlab", config.Environment{BaseURL: "https://gitlab.com/api/v4", Auth: config.AuthConfig{Type: "bearer"}}, KindGitLab},
		{"github", config.Environment{BaseURL: "https://api.github.com", Auth: config.AuthConfig{Type: "bearer"}}, KindGitHub},
		{"jira", config.Environment{BaseURL: "https://x.atlassian.net", Auth: config.AuthConfig{Type: "basic"}}, KindAtlassian},
		{"confluence", config.Environment{BaseURL: "https://x.atlassian.net/wiki"}, KindAtlassian},
		{"jira", config.Environment{BaseURL: "https://jiraops.corp-apps.com"}, KindJira},
		{"mcp", config.Environment{BaseURL: "https://mcp.atlassian.com/v1/mcp"}, KindMCP},
		{"jenkins", config.Environment{BaseURL: "https://jenkins.dev.example"}, KindJenkins},
		{"slack", config.Environment{Auth: config.AuthConfig{Type: "slack-session"}}, KindSlack},
		{"trello", config.Environment{Auth: config.AuthConfig{Type: "trello"}}, KindTrello},
		{"clockify", config.Environment{BaseURL: "https://api.clockify.me/api/v1", Auth: config.AuthConfig{Type: "api-key"}}, KindClockify},
		{"postgres", config.Environment{Host: "db.example"}, KindPostgres},
		{"gmail", config.Environment{Host: "imap.gmail.com"}, KindIMAP},
		{"gdocs", config.Environment{OAuth: &config.OAuthRefreshConfig{}}, KindOAuth},
		{"kibana", config.Environment{}, KindKibana},
	}
	for _, c := range cases {
		if got := DetectKind(c.site, c.env); got != c.want {
			t.Errorf("DetectKind(%s, %s) = %s, want %s", c.site, c.env.BaseURL, got, c.want)
		}
	}
}

func TestCheckGitLab(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_token","error_description":"Token is expired."}`))
			return
		}
		switch r.URL.Path {
		case "/user":
			w.Write([]byte(`{"username":"kk","name":"K K"}`))
		case "/personal_access_tokens/self":
			w.Write([]byte(`{"name":"egw","expires_at":"2027-01-01","scopes":["read_api"]}`))
		}
	}))
	defer srv.Close()
	env := config.Environment{BaseURL: srv.URL}

	ok := Check(context.Background(), KindGitLab, env, "good", "")
	if ok.State != StateOK || ok.Identity != "kk / K K" || ok.ExpiresAt != "2027-01-01" || len(ok.Scopes) != 1 {
		t.Fatalf("good token: %+v", ok)
	}
	bad := Check(context.Background(), KindGitLab, env, "old", "")
	if bad.State != StateRejected || bad.HTTPStatus != 401 || bad.Detail != "Token is expired." {
		t.Fatalf("expired token: %+v", bad)
	}
}

func TestCheckSlack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("token") == "xoxc-1" && r.Header.Get("Cookie") == "d=xoxd-1" {
			w.Write([]byte(`{"ok":true,"user":"kk","team":"T"}`))
			return
		}
		w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer srv.Close()
	env := config.Environment{BaseURL: srv.URL}

	if r := Check(context.Background(), KindSlack, env, "xoxc-1", "xoxd-1"); r.State != StateOK || r.Identity != "kk @ T" {
		t.Fatalf("valid pair: %+v", r)
	}
	if r := Check(context.Background(), KindSlack, env, "xoxc-1", "xoxd-2"); r.State != StateRejected || r.Detail != "invalid_auth" {
		t.Fatalf("bad cookie: %+v", r)
	}
	if r := Check(context.Background(), KindSlack, env, "xoxc-1", ""); r.State != StateUnchecked {
		t.Fatalf("missing cookie: %+v", r)
	}
}

func TestUnreachableMentionsTunnel(t *testing.T) {
	env := config.Environment{BaseURL: "http://127.0.0.1:1", RequiresTunnel: "thp-vpn"}
	r := Check(context.Background(), KindJenkins, env, "x", "")
	if r.State != StateUnreachable {
		t.Fatalf("want unreachable, got %+v", r)
	}
	if want := `needs tunnel "thp-vpn" up`; !strings.Contains(r.Detail, want) {
		t.Fatalf("detail %q lacks %q", r.Detail, want)
	}
}

