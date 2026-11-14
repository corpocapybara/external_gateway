package server

import (
	"reflect"
	"slices"
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
		// Same params and response shaping as the email.X op it mirrors.
		ref := reg.Operations["email."+action]
		if ref == nil || !reflect.DeepEqual(op.Params, ref.Params) || !reflect.DeepEqual(op.Response, ref.Response) ||
			len(op.Response.AllowFields) == 0 {
			t.Fatalf("hotmail.%s params/response differ from email.%s or are empty", action, action)
		}
	}

	emailRoles := 0
	for name, role := range cfg.Roles {
		granted := map[string]bool{}
		for _, op := range role.Operations {
			granted[op] = true
		}
		for _, op := range role.Operations {
			if action, ok := strings.CutPrefix(op, "email."); ok {
				emailRoles++
				if !granted["hotmail."+action] {
					t.Errorf("role %s grants %s but not hotmail.%s", name, op, action)
				}
			}
		}
	}
	if emailRoles == 0 {
		t.Fatal("no role grants an email.X op; the grant check proved nothing")
	}

	// The agent hash is the 64-zero placeholder (owner 2026-11-14) and the agent
	// reaches the hotmail ops through its dev-bot grant.
	if len(cfg.Agents) != 1 || cfg.Agents[0].TokenSHA != strings.Repeat("0", 64) {
		t.Fatalf("example agent hash is not the 64-zero placeholder: %+v", cfg.Agents)
	}
	reached := false
	for _, g := range cfg.Agents[0].Grants {
		if g.Workspace == "globex" && slices.Contains(cfg.Roles[g.Role].Operations, "hotmail.send") {
			reached = true
		}
	}
	if !reached {
		t.Fatal("the example agent has no grant reaching hotmail.send in globex")
	}
}
