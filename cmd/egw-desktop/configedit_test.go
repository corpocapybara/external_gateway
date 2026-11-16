//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// realWorkspace returns the first workspace, in name order, whose config.<ws>.yaml and operations.<ws>.yaml
// both exist in dir. The tests below run on the checkout's real (gitignored) workspace files without naming one.
func realWorkspace(dir string) (string, bool) {
	configs, _ := filepath.Glob(filepath.Join(dir, "config.*.yaml"))
	sort.Strings(configs)
	for _, c := range configs {
		ws := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(c), "config."), ".yaml")
		if ws == "" || strings.Contains(ws, ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "operations."+ws+".yaml")); err == nil {
			return ws, true
		}
	}
	return "", false
}

func TestRealWorkspacePick(t *testing.T) {
	dir := t.TempDir()
	if ws, ok := realWorkspace(dir); ok {
		t.Fatalf("empty dir picked %q", ws)
	}
	for _, f := range []string{"config.yaml", "operations.yaml", "config.a.yaml", "config.c.yaml", "operations.c.yaml", "config.b.yaml", "config.a.b.yaml", "operations.a.b.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if ws, ok := realWorkspace(dir); !ok || ws != "c" {
		t.Fatalf("want c (config.yaml, dotted names and configs without operations ignored), got %q %v", ws, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "operations.b.yaml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if ws, ok := realWorkspace(dir); !ok || ws != "b" {
		t.Fatalf("want the first in name order, b, got %q %v", ws, ok)
	}
}

// testApp copies the first real workspace's config and every operations file (the catalog reads them all) from
// the checkout root into a temp build dir, and returns the app and that workspace.
func testApp(t *testing.T) (*app, string) {
	t.Helper()
	root := filepath.Join("..", "..")
	ws, ok := realWorkspace(root)
	if !ok {
		t.Skip("no config.<ws>.yaml with an operations.<ws>.yaml at the checkout root")
	}
	ops, _ := filepath.Glob(filepath.Join(root, "operations*.yaml"))
	dir := t.TempDir()
	for _, src := range append(ops, filepath.Join(root, "config."+ws+".yaml")) {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(src)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &app{buildDir: dir, installDir: filepath.Join(dir, "install"), dataDir: filepath.Join(dir, "data"), adminTokens: map[string]string{}}, ws
}

func TestAddServiceWithCatalogCapabilities(t *testing.T) {
	a, ws := testApp(t)
	cfg, err := loadConfig(a.repoConfigPath(ws))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Roles) == 0 {
		t.Fatalf("%s has no roles", ws)
	}
	role := sortedKeys(cfg.Roles)[0]
	catalog := a.catalog()
	taken := func(site string) bool {
		if _, ok := cfg.Workspaces[workspaceKey(ws, cfg)].Sites[site]; ok {
			return true
		}
		for id := range catalog {
			if strings.HasPrefix(id, site+".") {
				return true
			}
		}
		return false
	}
	site := "github"
	for i := 2; taken(site); i++ {
		site = fmt.Sprintf("github%d", i)
	}
	e, err := a.openEdit(ws)
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
	newID := site + "." + strings.SplitN(githubOps[0], ".", 2)[1]
	req := addServiceRequest{WS: ws, Template: "github", Site: site, Env: "prod",
		BaseURL: "https://api.github.com", Secret: "github", Ops: githubOps[:1], Roles: []string{role}}
	if strings.Contains(a.catalog()[githubOps[0]].Path, "{site.org}") {
		if err := e.addService(req); err == nil || !strings.Contains(err.Error(), "fill in org") {
			t.Fatalf("missing org must be refused, got %v", err)
		}
		e, _ = a.openEdit(ws)
	}
	req.SiteVars = map[string]string{"org": "acme-test-org"}
	if err := e.addService(req); err != nil {
		t.Fatal(err)
	}
	changes, err := e.commit(false)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
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
	if _, added := changedLines(changes[0].Before, changes[0].After); !strings.Contains(strings.Join(added, "\n"), "org: acme-test-org") {
		t.Fatalf("site variable org not written in the new site block: %q", added)
	}
	if before, _ := os.ReadFile(a.repoConfigPath(ws)); string(before) != changes[0].Before {
		t.Fatal("a dry run must not write")
	}

	if _, err := e.commit(true); err != nil {
		t.Fatal(err)
	}
	caps, err := a.capabilities(ws)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range caps.Ops {
		if op.ID == newID {
			found = len(op.Roles) == 1 && op.Roles[0] == role
		}
	}
	if !found {
		t.Fatalf("%s not granted to %s alone after apply", newID, role)
	}
}

func TestRegenerateAgentUpdatesConfigAndKeys(t *testing.T) {
	a, ws := testApp(t)
	list, _, err := a.agents(ws)
	if err != nil || len(list) == 0 {
		t.Fatalf("agents: %v %v", list, err)
	}
	token, err := a.regenerateAgent(ws, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(a.repoConfigPath(ws))
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
	a, ws := testApp(t)
	// The test runs on the real config, so pick a site that is enabled there now.
	cfg, err := loadConfig(a.repoConfigPath(ws))
	if err != nil {
		t.Fatal(err)
	}
	sites := cfg.Workspaces[workspaceKey(ws, cfg)].Sites
	site := ""
	for _, name := range sortedKeys(sites) {
		if !sites[name].Disabled {
			site = name
			break
		}
	}
	if site == "" {
		t.Skipf("every %s site is disabled", ws)
	}
	e, err := a.openEdit(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.setSiteDisabled(site, true); err != nil {
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
	cfg, _ = loadConfig(a.repoConfigPath(ws))
	if !cfg.Workspaces[workspaceKey(ws, cfg)].Sites[site].Disabled {
		t.Fatalf("%s not disabled after apply", site)
	}

	e, _ = a.openEdit(ws)
	if err := e.setSiteDisabled(site, false); err != nil {
		t.Fatal(err)
	}
	changes, err = e.commit(true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ = loadConfig(a.repoConfigPath(ws)); cfg.Workspaces[workspaceKey(ws, cfg)].Sites[site].Disabled {
		t.Fatal("enable left the flag in place")
	}
	original, _ := os.ReadFile(filepath.Join("..", "..", "config."+ws+".yaml"))
	if after, _ := os.ReadFile(a.repoConfigPath(ws)); string(after) != string(original) {
		r, ad := changedLines(string(original), string(after))
		i := 0
		for i < len(original) && i < len(after) && original[i] == after[i] {
			i++
		}
		t.Fatalf("disable + enable must return the file to its original bytes: removed=%q added=%q len %d vs %d, first diff at %d: %q vs %q", r, ad, len(original), len(after), i, original[max(0, i-20):min(len(original), i+20)], after[max(0, i-20):min(len(after), i+20)])
	}
}
