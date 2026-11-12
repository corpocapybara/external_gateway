package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T0008: ops loading rules for jira_attachment_content.
func TestJiraAttachmentContentLoad(t *testing.T) {
	const keyID = "    params:\n      key: {type: string, required: true}\n      id: {type: string, required: true}\n"
	op := func(connector, extra, params string) string {
		return "operations:\n  a.b:\n    connector: " + connector + "\n    site: s\n    method: GET\n    path: /x\n" +
			"    jira_attachment_content: true\n" + extra + params
	}
	const shape = "operation a.b: jira_attachment_content needs an http-rest op without slack_file_content, multipart, body_template, response.allow_fields or precheck"
	cases := []struct{ name, body, want string }{
		{"valid", op("http-rest", "", keyID), ""},
		{"postgres", op("postgres", "", keyID), shape},
		{"slack_file_content", op("http-rest", "    slack_file_content: true\n", keyID), shape},
		{"multipart", op("http-rest", "    multipart: {field: f, filename_param: key, content_param: id}\n", keyID), shape},
		{"body_template", op("http-rest", "    body_template: '{}'\n", keyID), shape},
		{"allow_fields", op("http-rest", "    response:\n      allow_fields: [x]\n", keyID), shape},
		{"key missing", op("http-rest", "", "    params:\n      id: {type: string, required: true}\n"), "operation a.b: jira_attachment_content needs a required key param"},
		{"key optional", op("http-rest", "", "    params:\n      key: {type: string}\n      id: {type: string, required: true}\n"), "needs a required key param"},
		{"id missing", op("http-rest", "", "    params:\n      key: {type: string, required: true}\n"), "operation a.b: jira_attachment_content needs a required id param"},
		{"id optional", op("http-rest", "", "    params:\n      key: {type: string, required: true}\n      id: {type: string, required: false}\n"), "needs a required id param"},
		// A jira_issue_link precheck op with the field is refused by the precheck guard, which names it.
		{"precheck", op("http-rest", "    precheck: jira_issue_link\n    constraints:\n      - type: jira_key_scope\n        projects: [SRTHP]\n        issue_keys: [key]\n",
			"    params:\n      key: {type: string, required: true}\n      id: {type: string, required: true, pattern: '^[0-9]+$'}\n"),
			"operation a.b: precheck jira_issue_link needs an http-rest op without slack_file_content, multipart or jira_attachment_content"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "ops.yaml")
			if err := os.WriteFile(p, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewRegistry(p)
			if c.want == "" {
				if err != nil {
					t.Fatalf("valid op refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
