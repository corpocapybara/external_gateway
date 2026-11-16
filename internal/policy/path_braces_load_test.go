package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EG0031: an op path with "{{" or "}}" is refused at load, naming the op; only
// the path is checked, so a body_template may still use {{x}}.
func TestLoadRefusesDoubleBracePath(t *testing.T) {
	op := func(path, extra string) string {
		return "operations:\n  k.search:\n    connector: http-rest\n    site: s\n    method: POST\n    path: " + path + "\n" +
			"    params:\n      index: {type: string}\n      x: {type: string}\n" + extra
	}
	cases := []struct{ name, body, want string }{
		{"double brace placeholder", op("/api/console/proxy?path={{index}}/_search&method=POST", ""),
			`operation k.search: path "/api/console/proxy?path={{index}}/_search&method=POST" has a double brace`},
		{"lone closing pair", op("/a/{index}}/b", ""), `operation k.search: path "/a/{index}}/b" has a double brace`},
		{"lone opening pair", op("/a/{{index/b", ""), `operation k.search: path "/a/{{index/b" has a double brace`},
		{"single brace path", op("/api/console/proxy?path={index}/_search&method=POST", ""), ""},
		{"double brace body_template", op("/a/{x}", "    body_template: '{\"q\":\"{{x}}\"}'\n"), ""},
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
