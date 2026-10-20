package server

import (
	"testing"

	"github.com/external_gateway/internal/config"
)

func TestCollectSecrets(t *testing.T) {
	ws := &config.Workspace{Sites: map[string]config.Site{
		"jira":       {Environments: map[string]config.Environment{"prod": {BaseURL: "https://x.atlassian.net", User: "me@x", SecretRef: "secret://w/jira"}}},
		"confluence": {Environments: map[string]config.Environment{"prod": {BaseURL: "https://x.atlassian.net/wiki", SecretRef: "secret://w/jira"}}},
		"slack": {Environments: map[string]config.Environment{"prod": {Auth: config.AuthConfig{Type: "slack-session"},
			SecretRef: "secret://w/slack-xoxc", CookieSecretRef: "secret://w/slack-xoxd"}}},
		"gdocs": {Environments: map[string]config.Environment{"prod": {SecretRef: "secret://w/gdocs",
			OAuth: &config.OAuthRefreshConfig{ClientSecretRef: "secret://w/gdocs-client", RefreshSecretRef: "secret://w/gdocs-refresh"}}}},
		"foreign": {Environments: map[string]config.Environment{"prod": {SecretRef: "secret://other/x"}}},
		"old":     {Disabled: true, Environments: map[string]config.Environment{"prod": {SecretRef: "secret://w/old"}}},
		"wiki":    {Disabled: true, Environments: map[string]config.Environment{"prod": {SecretRef: "secret://w/jira"}}},
	}}

	got := map[string]*secretStatus{}
	for _, st := range collectSecrets("w", ws) {
		got[st.Name] = st
	}

	if len(got) != 7 {
		t.Fatalf("want 7 secrets (foreign workspace excluded), got %d: %v", len(got), got)
	}
	if n := len(got["jira"].UsedBy); n != 3 {
		t.Fatalf("shared jira secret should list 3 usages, got %d", n)
	}
	if !got["old"].Disabled || got["jira"].Disabled {
		t.Fatalf("a secret is disabled only when every site using it is: old=%v jira=%v", got["old"].Disabled, got["jira"].Disabled)
	}
	if got["slack-xoxc"].Pair != "slack-xoxd" || got["slack-xoxd"].Pair != "slack-xoxc" || got["slack-xoxd"].Role != "secondary" {
		t.Fatalf("slack pair wrong: %+v %+v", got["slack-xoxc"], got["slack-xoxd"])
	}
	if got["gdocs-refresh"].Role != "oauth-refresh" || got["gdocs-client"].Role != "oauth-client" {
		t.Fatalf("oauth roles wrong: %+v %+v", got["gdocs-refresh"], got["gdocs-client"])
	}
}
