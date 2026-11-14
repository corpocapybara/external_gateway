package server

import (
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// TestExampleHotmailConfig loads the shipped globex example config and ops and
// checks the hotmail site, its four ops and that every role granting an email.X
// op grants hotmail.X too.
func TestExampleHotmailConfig(t *testing.T) {
	cfg, err := config.Load("../../config.example/config.globex.yaml")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	reg, err := policy.NewRegistry("../../config.example/operations.globex.yaml")
	if err != nil {
		t.Fatalf("policy.NewRegistry: %v", err)
	}

	env, ok := cfg.Workspaces["globex"].Sites["hotmail"].Environments["prod"]
	if !ok {
		t.Fatal("globex has no hotmail/prod env")
	}
	if env.Host != "outlook.office365.com" || env.ImapPort != 993 || env.SmtpHost != "smtp-mail.outlook.com" ||
		env.SmtpPort != 587 || env.User == "" || env.Auth.Type != "oauth2" || env.SecretRef != "secret://globex/hotmail" {
		t.Fatalf("hotmail env = %+v", env)
	}
	if env.OAuth == nil || env.OAuth.TokenURL != "https://login.microsoftonline.com/consumers/oauth2/v2.0/token" ||
		env.OAuth.ClientID == "" || env.OAuth.RefreshSecretRef != "secret://globex/hotmail-refresh" || env.OAuth.ClientSecretRef != "" {
		t.Fatalf("hotmail oauth = %+v", env.OAuth)
	}

	for _, action := range []string{"list_mailboxes", "search", "read", "send"} {
		op, ok := reg.Operations["hotmail."+action]
		if !ok {
			t.Fatalf("op hotmail.%s missing", action)
		}
		if op.Connector != "email" || op.Site != "hotmail" {
			t.Fatalf("hotmail.%s: connector %q site %q", action, op.Connector, op.Site)
		}
	}

	for name, role := range cfg.Roles {
		granted := map[string]bool{}
		for _, op := range role.Operations {
			granted[op] = true
		}
		for _, op := range role.Operations {
			if action, ok := strings.CutPrefix(op, "email."); ok && !granted["hotmail."+action] {
				t.Errorf("role %s grants %s but not hotmail.%s", name, op, action)
			}
		}
	}
}
