package policy

import (
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
)

// kibanaOp is a kibana.search-like op with one kibana_index_scope constraint.
func kibanaOp(def interface{}) *Operation {
	return &Operation{
		ID:     "kibana.search",
		Params: map[string]ParamDef{"index": {Type: "string", Default: def}},
		Constraints: []map[string]interface{}{
			{"type": "kibana_index_scope", "indices": kibanaIndexScopeSource},
		},
	}
}

func kibanaWS(globs ...string) *config.Workspace {
	return &config.Workspace{Policy: config.WorkspacePolicy{KibanaIndices: globs}}
}

func checkKibana(t *testing.T, op *Operation, params map[string]interface{}, ws *config.Workspace) error {
	t.Helper()
	r := &OperationRegistry{Operations: map[string]*Operation{op.ID: op}}
	return r.CheckConstraints(op, params, ws)
}

func TestKibanaIndexScopeLoad(t *testing.T) {
	if constraintTypes["kibana_index_scope"] != constraintChecked {
		t.Fatal("kibana_index_scope is not constraintChecked")
	}
	const badIndices = "operation a.b: kibana_index_scope indices must be $workspace.kibana_indices"
	const badParam = "operation a.b: kibana_index_scope needs a string index param"
	strParam := "    params:\n      index: {type: string, default: logstash-*}\n"
	cases := []struct{ name, extra, want string }{
		{"jira_projects", strParam + "    constraints:\n      - {type: kibana_index_scope, indices: $workspace.jira_projects}\n", badIndices},
		{"unknown var", strParam + "    constraints:\n      - {type: kibana_index_scope, indices: $workspace.nope}\n", badIndices},
		{"missing", strParam + "    constraints:\n      - {type: kibana_index_scope}\n", badIndices},
		{"list", strParam + "    constraints:\n      - {type: kibana_index_scope, indices: [logstash-*]}\n", badIndices},
		{"no index param", "    constraints:\n      - {type: kibana_index_scope, indices: $workspace.kibana_indices}\n", badParam},
		{"int index param", "    params:\n      index: {type: int}\n    constraints:\n      - {type: kibana_index_scope, indices: $workspace.kibana_indices}\n", badParam},
		{"second bad of two", strParam + "    constraints:\n      - {type: kibana_index_scope, indices: $workspace.kibana_indices}\n      - {type: kibana_index_scope, indices: $workspace.nope}\n", badIndices},
		{"valid", strParam + "    constraints:\n      - {type: kibana_index_scope, indices: $workspace.kibana_indices}\n", ""},
		{"valid two", strParam + "    constraints:\n      - {type: kibana_index_scope, indices: $workspace.kibana_indices}\n      - {type: kibana_index_scope, indices: $workspace.kibana_indices}\n", ""},
	}
	for _, c := range cases {
		_, err := NewRegistry(constraintOpsFile(t, c.extra))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.want)
		}
	}
	// An op built in code that skipped the load check fails closed per call.
	op := kibanaOp(nil)
	op.Constraints[0]["indices"] = "$workspace.nope"
	if err := checkKibana(t, op, map[string]interface{}{"index": "logstash-nth-stg-1"}, kibanaWS("*")); err == nil {
		t.Error("bad indices on a code-built op passed")
	}
}

func TestKibanaIndexScopeElements(t *testing.T) {
	long := "logstash-nth-stg" + strings.Repeat("a", 255-len("logstash-nth-stg"))
	cases := []struct {
		index string
		ok    bool
	}{
		{"logstash-nth-stg-1, logstash-nth-stg-2", true},
		{" logstash-nth-stg-1\t,logstash-nth-stg-2 ", true},
		{"a,,b", false},
		{"logstash-nth-stg-1,", false},
		{"", false},
		{"  ", false},
		{"_all", false},
		{"-x", false},
		{"logstash-nth-stg-1,-logstash-nth-stg-2", false},
		{"logstash-nth-stg?x", false},
		{"logstash-nth-stg[1]", false},
		{"logstash-nth-stg-x/../secret", false},
		{"logstash-nth-stg*:secret", false},
		{"..", false},
		{".", false},
		{"logstash-nth-stg..x", false},
		{"_cat", false},
		{"logstash-nth-stg x", false},
		{" logstash-nth-stg-1", false},
		{"<logstash-nth-stg-{now}>", false},
		{"logstash-nth-stg-1#x", false},
		{"logstash-nth-stg-1%2Fx", false},
		{"logstash-nth-stg-1&x", false},
		{"logstash-nth-stg-1>x", false},
		{"logstash-nth-stg-1+x", false},
		{"logstash-nth-stg-1 x", false},
		{long, true},
		{long + "a", false},
	}
	ws := kibanaWS("logstash-nth-stg*")
	for _, c := range cases {
		err := checkKibana(t, kibanaOp(nil), map[string]interface{}{"index": c.index}, ws)
		if (err == nil) != c.ok {
			t.Errorf("index %q: err = %v, want ok=%v", c.index, err, c.ok)
		}
	}
}

func TestKibanaIndexScopeCovers(t *testing.T) {
	stg := []string{"logstash-nth-stg*"}
	globex := []string{"logstash-nth-stg*", "logstash-*"}
	cases := []struct {
		globs []string
		index string
		ok    bool
	}{
		{stg, "logstash-nth-stg-2026.11", true},
		{stg, "logstash-nth-stg-2026*", true},
		{stg, "logstash-nth-stg**", true},
		{stg, "secret-1", false},
		{stg, "*", false},
		{stg, "logstash-*", false},
		{stg, "logstash-*-2026", false},
		{stg, "Logstash-nth-stg-1", false},
		{stg, "logstash-nth-stg-1,secret", false},
		{[]string{"*"}, "anything-*", true},
		{[]string{"*"}, "*", true},
		{globex, "logstash-prod-1", true},
		{globex, "logstash-*", true},
		{globex, "secret-*", false},
		{[]string{""}, "logstash-nth-stg-1", false},
		{[]string{"logstash-*-2026"}, "logstash-a-2026", true},
		{[]string{"logstash-*-2026"}, "logstash-*-2026", true},
		{[]string{"logstash-*-2026"}, "logstash-*", false},
	}
	for _, c := range cases {
		err := checkKibana(t, kibanaOp(nil), map[string]interface{}{"index": c.index}, kibanaWS(c.globs...))
		if (err == nil) != c.ok {
			t.Errorf("globs %v index %q: err = %v, want ok=%v", c.globs, c.index, err, c.ok)
		}
	}
	err := checkKibana(t, kibanaOp(nil), map[string]interface{}{"index": "logstash-nth-stg-1,secret"}, kibanaWS(stg...))
	if err == nil || err.Error() != `kibana_index_scope on kibana.search: index "secret" is not within kibana_indices` {
		t.Errorf("error = %v, want it to name the first refused element", err)
	}
}

// refGlob is a plain recursive glob match of a concrete name (only * wild),
// written independently of kibanaPatternCovers.
func refGlob(name, glob string) bool {
	if glob == "" {
		return name == ""
	}
	if glob[0] == '*' {
		for k := 0; k <= len(name); k++ {
			if refGlob(name[k:], glob[1:]) {
				return true
			}
		}
		return false
	}
	return name != "" && name[0] == glob[0] && refGlob(name[1:], glob[1:])
}

// strings over alphabet up to length n, the empty string included.
func allStrings(alpha string, n int) []string {
	out := []string{""}
	prev := []string{""}
	for l := 1; l <= n; l++ {
		var next []string
		for _, p := range prev {
			for i := 0; i < len(alpha); i++ {
				next = append(next, p+alpha[i:i+1])
			}
		}
		out = append(out, next...)
		prev = next
	}
	return out
}

// expansions replaces every * in elem by each string of fill.
func expansions(elem string, fill []string) []string {
	i := strings.IndexByte(elem, '*')
	if i < 0 {
		return []string{elem}
	}
	var out []string
	for _, f := range fill {
		for _, rest := range expansions(elem[i+1:], fill) {
			out = append(out, elem[:i]+f+rest)
		}
	}
	return out
}

func TestKibanaIndexScopeCoversSound(t *testing.T) {
	pats := allStrings("ab*", 4)
	fill := allStrings("ab", 2)
	covered := 0
	for _, e := range pats {
		for _, g := range pats {
			if !kibanaPatternCovers(e, g) {
				continue
			}
			covered++
			for _, name := range expansions(e, fill) {
				if !refGlob(name, g) {
					t.Fatalf("covers(%q, %q) but %q does not match", e, g, name)
				}
			}
		}
	}
	if covered == 0 {
		t.Fatal("no pair was covered")
	}
}

func TestKibanaIndexScopeDefault(t *testing.T) {
	ws := kibanaWS("logstash-nth-stg*")
	none := map[string]interface{}{}
	cases := []struct {
		name   string
		op     *Operation
		params map[string]interface{}
		ws     *config.Workspace
		ok     bool
	}{
		{"omitted, covered default", kibanaOp("logstash-nth-stg-1"), none, ws, true},
		{"omitted, uncovered default", kibanaOp("secret-*"), none, ws, false},
		{"omitted, no default", kibanaOp(nil), none, ws, false},
		{"param wins over default", kibanaOp("secret-*"), map[string]interface{}{"index": "logstash-nth-stg-1"}, ws, true},
		{"empty list unscoped", kibanaOp(nil), map[string]interface{}{"index": "_all"}, kibanaWS(), true},
		{"omitted, empty list", kibanaOp(nil), none, kibanaWS(), true},
		{"nil workspace unscoped", kibanaOp(nil), map[string]interface{}{"index": "_all"}, nil, true},
		{"non-string index", kibanaOp(nil), map[string]interface{}{"index": 7}, ws, false},
		{"non-string default", kibanaOp(7), none, ws, false},
	}
	for _, c := range cases {
		err := checkKibana(t, c.op, c.params, c.ws)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
	// Two constraints on one op: both must pass.
	op := kibanaOp(nil)
	op.Constraints = append(op.Constraints, map[string]interface{}{"type": "kibana_index_scope", "indices": "$workspace.jira_projects"})
	if err := checkKibana(t, op, map[string]interface{}{"index": "logstash-nth-stg-1"}, ws); err == nil {
		t.Error("second, malformed constraint was not checked")
	}
}
