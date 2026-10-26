//go:build windows

package main

import (
	"slices"
	"testing"

	"github.com/external_gateway/internal/config"
)

func TestIgnoredDisabledSites(t *testing.T) {
	sites := map[string]config.Site{
		"jira":      {},
		"authentik": {Disabled: true},
		"gmail":     {Disabled: true},
	}
	secrets := []secretRow{
		{Name: "jira-token", UsedBy: []usage{{Site: "jira", Env: "prod"}}},
		{Name: "authentik-token", UsedBy: []usage{{Site: "authentik", Env: "prod", Disabled: true}}},
		{Name: "gmail-token", UsedBy: []usage{{Site: "gmail", Env: "prod"}, {Site: "gmail", Env: "dev"}}},
	}
	if got := ignoredDisabledSites(sites, secrets); !slices.Equal(got, []string{"gmail"}) {
		t.Fatalf("only a disabled site the gateway reports active is ignored, got %v", got)
	}
	secrets[2].UsedBy[0].Disabled, secrets[2].UsedBy[1].Disabled = true, true
	if got := ignoredDisabledSites(sites, secrets); len(got) != 0 {
		t.Fatalf("a gateway that reports every disabled site is not ignoring them, got %v", got)
	}
}

func TestDisabledSites(t *testing.T) {
	sites := map[string]config.Site{"b": {Disabled: true}, "a": {Disabled: true}, "c": {}}
	if got := disabledSites(sites); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("got %v", got)
	}
}
