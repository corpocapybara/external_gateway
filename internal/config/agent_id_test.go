package config

import "testing"

// Since EG0064 the audit agent_id is the config agents[].id, so Validate
// refuses an empty or duplicated id (EG0069).
func TestValidateRejectsEmptyOrDuplicateAgentID(t *testing.T) {
	cfg := func(ids ...string) *Config {
		c := &Config{}
		for _, id := range ids {
			c.Agents = append(c.Agents, Agent{ID: id, TokenSHA: "sha-" + id})
		}
		return c
	}
	if err := cfg("bot", "worker").Validate(); err != nil {
		t.Errorf("distinct ids: %v", err)
	}
	for _, tc := range []struct {
		name string
		ids  []string
		want string
	}{
		{"empty", []string{"bot", ""}, "agents[1] has no id"},
		{"whitespace", []string{"bot", " \t"}, "agents[1] has no id"},
		{"duplicate", []string{"bot", "bot"}, `agents[1]: duplicate agent id "bot" (also agents[0])`},
	} {
		err := cfg(tc.ids...).Validate()
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
