package config

import "testing"

func TestValidateRejectsUnknownDefaultEnv(t *testing.T) {
	cfg := func(defaultEnv string) *Config {
		return &Config{
			Agents: []Agent{{ID: "bot", TokenSHA: "sha"}},
			Workspaces: map[string]Workspace{"thp": {Sites: map[string]Site{"jira": {
				DefaultEnv:   defaultEnv,
				Environments: map[string]Environment{"prod": {}, "corpo": {}},
			}}}},
		}
	}
	if err := cfg("prod").Validate(); err != nil {
		t.Errorf("default_env prod: %v", err)
	}
	if err := cfg("").Validate(); err != nil {
		t.Errorf("no default_env: %v", err)
	}
	want := `workspace thp site jira: default_env "prdo" is not one of its environments`
	if err := cfg("prdo").Validate(); err == nil || err.Error() != want {
		t.Errorf("default_env prdo: err = %v, want %q", err, want)
	}
}
