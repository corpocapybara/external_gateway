package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// EG0044: oauth.refresh_cooldown is a Go duration; absent stays nil (the server's
// 5 min default), negative is refused by Validate.

func decodeEnv(t *testing.T, cooldownLine string) Environment {
	t.Helper()
	src := "secret_ref: secret://w/api\noauth:\n  token_url: https://example.invalid/token\n  client_id: cid\n"
	if cooldownLine != "" {
		src += "  " + cooldownLine + "\n"
	}
	var env Environment
	if err := yaml.Unmarshal([]byte(src), &env); err != nil {
		t.Fatalf("decoding %q: %v", cooldownLine, err)
	}
	if env.OAuth == nil {
		t.Fatalf("decoding %q: oauth block missing", cooldownLine)
	}
	return env
}

func TestOAuthRefreshCooldownYAML(t *testing.T) {
	for _, tc := range []struct {
		line string
		want time.Duration
	}{
		{"refresh_cooldown: 5m", 5 * time.Minute},
		{`refresh_cooldown: "0s"`, 0},
	} {
		got := decodeEnv(t, tc.line).OAuth.RefreshCooldown
		if got == nil || *got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.line, got, tc.want)
		}
	}
	if got := decodeEnv(t, "").OAuth.RefreshCooldown; got != nil {
		t.Errorf("absent: got %v, want nil", *got)
	}
	// yaml.v3 refuses a bare integer for a time.Duration, so the config does not load.
	for _, n := range []string{"300", "-1"} {
		var env Environment
		err := yaml.Unmarshal([]byte("oauth:\n  refresh_cooldown: "+n+"\n"), &env)
		if err == nil || !strings.Contains(err.Error(), "cannot unmarshal !!int") {
			t.Errorf("bare %s: err = %v, want a decode error", n, err)
		}
	}
}

func TestValidateRejectsNegativeRefreshCooldown(t *testing.T) {
	cfg := func(prod, corpo Environment) *Config {
		return &Config{
			Agents: []Agent{{ID: "bot", TokenSHA: "sha"}},
			Workspaces: map[string]Workspace{"acme": {Sites: map[string]Site{"api": {
				Environments: map[string]Environment{"prod": prod, "corpo": corpo},
			}}}},
		}
	}
	ok := decodeEnv(t, "refresh_cooldown: 5m")
	if err := cfg(ok, decodeEnv(t, "")).Validate(); err != nil {
		t.Errorf("5m and absent: %v", err)
	}
	for _, line := range []string{"refresh_cooldown: -1m", "refresh_cooldown: -1ns"} {
		err := cfg(ok, decodeEnv(t, line)).Validate()
		if err == nil {
			t.Errorf("%s: Validate passed, want an error", line)
			continue
		}
		if msg := err.Error(); !strings.HasPrefix(msg, "workspace acme site api env corpo: oauth.refresh_cooldown ") ||
			!strings.HasSuffix(msg, " is negative") {
			t.Errorf("%s: err = %q, want it to name workspace acme, site api, env corpo", line, msg)
		}
	}
}
