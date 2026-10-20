//go:build windows

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func testApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"config.thp.yaml", "operations.thp.yaml", "operations.cm.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Skipf("%s not available: %v", f, err)
		}
		os.WriteFile(filepath.Join(dir, f), data, 0600)
	}
	return &app{buildDir: dir, installDir: filepath.Join(dir, "install"), dataDir: filepath.Join(dir, "data"), adminTokens: map[string]string{}}
}

func TestAddServiceWithCatalogCapabilities(t *testing.T) {
	a := testApp(t)
	e, err := a.openEdit("thp")
	if err != nil {
		t.Fatal(err)
	}
	var githubOps []string
	for id := range a.catalog() {
		if strings.HasPrefix(id, "github.") {
			githubOps = append(githubOps, id)
		}
	}
	if len(githubOps) == 0 {
		t.Skip("no github operations in the catalog")
	}
	sort.Strings(githubOps)
	req := addServiceRequest{WS: "thp", Template: "github", Site: "github", Env: "prod",
		BaseURL: "https://api.github.com", Secret: "github", Ops: githubOps[:1], Roles: []string{"dev-bot"}}
	if strings.Contains(a.catalog()[githubOps[0]].Path, "{site.org}") {
		if err := e.addService(req); err == nil || !strings.Contains(err.Error(), "fill in org") {
			t.Fatalf("missing org must be refused, got %v", err)
		}
		e, _ = a.openEdit("thp")
	}
	req.SiteVars = map[string]string{"org": "acme"}
	if err := e.addService(req); err != nil {
		t.Fatal(err)
	}
	changes, err := e.commit(false)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if !strings.Contains(changes[0].After, "org: acme") {
		t.Fatal("site variable org not written")
	}
	if len(changes) != 2 {
		t.Fatalf("want config and operations changed, got %d", len(changes))
	}
	for _, c := range changes {
		removed, _ := changedLines(c.Before, c.After)
		if len(removed) != 0 {
			t.Fatalf("%s: adding a service must not remove lines, removed %q", c.Path, removed)
		}
	}
	if before, _ := os.ReadFile(a.repoConfigPath("thp")); string(before) != changes[0].Before {
		t.Fatal("a dry run must not write")
	}

	if _, err := e.commit(true); err != nil {
		t.Fatal(err)
	}
	caps, err := a.capabilities("thp")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range caps.Ops {
		if op.ID == githubOps[0] {
			found = len(op.Roles) == 1 && op.Roles[0] == "dev-bot"
		}
	}
	if !found {
		t.Fatalf("%s not granted to dev-bot after apply", githubOps[0])
	}
}

func TestRegenerateAgentUpdatesConfigAndKeys(t *testing.T) {
	a := testApp(t)
	list, _, err := a.agents("thp")
	if err != nil || len(list) == 0 {
		t.Fatalf("agents: %v %v", list, err)
	}
	token, err := a.regenerateAgent("thp", list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(a.repoConfigPath("thp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ag := range cfg.Agents {
		if ag.ID == list[0].ID && ag.TokenSHA != sha256Hex(token) {
			t.Fatal("config hash not updated")
		}
	}
	if k := a.keyBySHA(sha256Hex(token)); k == nil || k.Token != token {
		t.Fatal("agent-keys.txt entry missing")
	}
}

func TestCustomOperationValidation(t *testing.T) {
	if _, err := buildCustomOp("gitlab", customOp{Method: "TRACE", Path: "/x"}); err == nil {
		t.Fatal("TRACE should be rejected")
	}
	n, err := buildCustomOp("gitlab", customOp{Method: "get", Path: "/projects/{project_id|urlencode}/issues",
		Params: []customParam{{Name: "project_id", Required: true}}, AllowFields: []string{"iid", "title"}})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := renderNode(n)
	for _, want := range []string{"method: GET", "required: true", "allow_fields:", "- iid"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestDisableAndEnableSite(t *testing.T) {
	a := testApp(t)
	e, err := a.openEdit("thp")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.setSiteDisabled("kibana", true); err != nil {
		t.Fatal(err)
	}
	changes, err := e.commit(true)
	if err != nil {
		t.Fatal(err)
	}
	removed, added := changedLines(changes[0].Before, changes[0].After)
	if len(changes) != 1 || len(removed) != 0 || len(added) != 1 || strings.TrimSpace(added[0]) != "disabled: true" {
		t.Fatalf("disable should add one line, removed=%q added=%q", removed, added)
	}
	cfg, _ := loadConfig(a.repoConfigPath("thp"))
	if !cfg.Workspaces[workspaceKey("thp", cfg)].Sites["kibana"].Disabled {
		t.Fatal("kibana not disabled after apply")
	}

	e, _ = a.openEdit("thp")
	if err := e.setSiteDisabled("kibana", false); err != nil {
		t.Fatal(err)
	}
	changes, err = e.commit(true)
	if err != nil {
		t.Fatal(err)
	}
	if changes[0].After != changes[0].Before && strings.Contains(changes[0].After, "disabled: true") {
		t.Fatal("enable left the flag in place")
	}
	original, _ := os.ReadFile(filepath.Join("..", "..", "config.thp.yaml"))
	if after, _ := os.ReadFile(a.repoConfigPath("thp")); string(after) != string(original) {
		r, ad := changedLines(string(original), string(after))
		i := 0
		for i < len(original) && i < len(after) && original[i] == after[i] {
			i++
		}
		t.Fatalf("disable + enable must return the file to its original bytes: removed=%q added=%q len %d vs %d, first diff at %d: %q vs %q", r, ad, len(original), len(after), i, original[max(0, i-20):min(len(original), i+20)], after[max(0, i-20):min(len(after), i+20)])
	}
}
