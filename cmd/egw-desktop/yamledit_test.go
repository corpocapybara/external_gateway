//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const sampleConfig = "server:\r\n  # port: assigned at deploy time, sorted by\r\n  # workspace name\r\n  port: 8444\r\n\r\n# roles\r\nroles:\r\n  dev-bot:\r\n    operations:\r\n      - jira.search\r\n      - jira.issue\r\n\r\n  readonly:\r\n    operations:\r\n      - jira.search\r\n\r\nworkspaces:\r\n  w:\r\n    sites:\r\n      jira:\r\n        environments:\r\n          prod:\r\n            base_url: https://x.atlassian.net\r\n            insecure_skip_verify: true   # self-signed\r\n\r\n            user: me\r\n"

func writeTemp(t *testing.T, text string) string {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func changedLines(before, after string) (removed, added []string) {
	for _, op := range diffLines(splitLines(before), splitLines(after)) {
		switch op.kind {
		case '-':
			removed = append(removed, splitLines(before)[op.i])
		case '+':
			added = append(added, splitLines(after)[op.j])
		}
	}
	return
}

func TestPatchedKeepsFormattingAndAddsOnlyNewLines(t *testing.T) {
	f, err := openYAML(writeTemp(t, sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	ops := seqEnsure(mapGet(mapGet(f.root(), "roles"), "dev-bot"), "operations")
	ops.Content = append(ops.Content, scalar("jira.comments"))

	after, err := f.patched()
	if err != nil {
		t.Fatal(err)
	}
	removed, added := changedLines(sampleConfig, after)
	if len(removed) != 0 || len(added) != 1 || strings.TrimSpace(added[0]) != "- jira.comments" {
		t.Fatalf("want exactly one added line, got removed=%q added=%q\n%s", removed, added, after)
	}
	if !strings.Contains(after, "insecure_skip_verify: true   # self-signed") || !strings.Contains(after, "\r\n\r\n  readonly:") {
		t.Fatalf("formatting not preserved:\n%s", after)
	}
	if strings.Count(after, "\r\n") != strings.Count(after, "\n") {
		t.Fatal("line endings were mixed")
	}
}

func TestPatchedRemovalAndNewBlock(t *testing.T) {
	f, err := openYAML(writeTemp(t, sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	seqRemove(mapGet(mapGet(mapGet(f.root(), "roles"), "readonly"), "operations"), "jira.search")
	sites := mapGet(mapGet(mapGet(f.root(), "workspaces"), "w"), "sites")
	mapSet(sites, "gitlab", mapping("environments", mapping("prod", mapping("base_url", "https://gitlab.com/api/v4", "secret_ref", "secret://w/gitlab"))))

	after, err := f.patched()
	if err != nil {
		t.Fatal(err)
	}
	var check yaml.Node
	if err := yaml.Unmarshal([]byte(after), &check); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, after)
	}
	removed, added := changedLines(sampleConfig, after)
	if len(removed) != 2 || strings.TrimSpace(removed[1]) != "- jira.search" || !strings.Contains(after, "    operations: []") {
		t.Fatalf("emptied list should become operations: [], removed = %q\n%s", removed, after)
	}
	if len(added) < 4 || !strings.Contains(after, "      gitlab:") || !strings.Contains(after, "secret_ref: secret://w/gitlab") {
		t.Fatalf("new site block missing:\n%s", after)
	}
}

func TestPatchedRealConfigsUnchangedRoundTrip(t *testing.T) {
	for _, p := range []string{"../../config.thp.yaml", "../../operations.thp.yaml"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		f, err := openYAML(p)
		if err != nil {
			t.Fatal(err)
		}
		after, err := f.patched()
		if err != nil {
			t.Fatal(err)
		}
		if after != f.original {
			t.Fatalf("%s: an unedited document must be returned byte-identical", p)
		}
	}
}
