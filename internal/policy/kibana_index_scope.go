package policy

import (
	"fmt"
	"strings"

	"github.com/external_gateway/internal/config"
)

// kibanaIndexScopeSource is the only indices value kibana_index_scope accepts.
const kibanaIndexScopeSource = "$workspace.kibana_indices"

// kibanaIndexMaxLen is Elasticsearch's index name limit, in bytes.
const kibanaIndexMaxLen = 255

// validateKibanaIndexScope refuses, at load time, a kibana_index_scope whose
// indices is not kibanaIndexScopeSource, or an op with no string index param.
func validateKibanaIndexScope(id string, op *Operation) error {
	for _, constraint := range op.Constraints {
		if t, _ := constraint["type"].(string); t != "kibana_index_scope" {
			continue
		}
		if err := kibanaIndexScopeShape(op, constraint); err != nil {
			return fmt.Errorf("operation %s: %v", id, err)
		}
	}
	return nil
}

// kibanaIndexScopeShape checks the constraint's indices value and the op's index param.
func kibanaIndexScopeShape(op *Operation, constraint map[string]interface{}) error {
	if s, ok := constraint["indices"].(string); !ok || s != kibanaIndexScopeSource {
		return fmt.Errorf("kibana_index_scope indices must be %s", kibanaIndexScopeSource)
	}
	if def, ok := op.Params["index"]; !ok || def.Type != "string" {
		return fmt.Errorf("kibana_index_scope needs a string index param")
	}
	return nil
}

// checkKibanaIndexScope refuses an index (the param, else the op's declared
// default, since CheckConstraints runs before ApplyDefaults) with an element no
// workspace kibana_indices glob covers. An empty list leaves the op unscoped.
func checkKibanaIndexScope(op *Operation, constraint map[string]interface{},
	params map[string]interface{}, ws *config.Workspace) error {
	// Re-checked per call: fails closed for an op built in code.
	if err := kibanaIndexScopeShape(op, constraint); err != nil {
		return fmt.Errorf("%v on %s", err, op.ID)
	}
	if ws == nil || len(ws.Policy.KibanaIndices) == 0 {
		return nil
	}
	allowed := ws.Policy.KibanaIndices
	v, present := params["index"]
	if !present {
		v = op.Params["index"].Default
		present = v != nil
	}
	if !present {
		return fmt.Errorf("kibana_index_scope on %s: index is required", op.ID)
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("kibana_index_scope on %s: index must be a string", op.ID)
	}
	for _, raw := range strings.Split(s, ",") {
		e := strings.Trim(raw, " \t")
		if e == "" {
			return fmt.Errorf("kibana_index_scope on %s: index has an empty element", op.ID)
		}
		if !validKibanaIndexElement(e) {
			return fmt.Errorf("kibana_index_scope on %s: index %q is not a plain index name or pattern", op.ID, e)
		}
		covered := false
		for _, g := range allowed {
			if kibanaPatternCovers(e, g) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("kibana_index_scope on %s: index %q is not within kibana_indices", op.ID, e)
		}
	}
	return nil
}

// validKibanaIndexElement accepts only 1..255 bytes of A-Z a-z 0-9 . _ - *, not
// starting with - (exclusion) or _ (_all, _cat ...), not "." and no "..": other
// characters can reach a different index through Kibana's console proxy path.
func validKibanaIndexElement(e string) bool {
	if len(e) == 0 || len(e) > kibanaIndexMaxLen || e[0] == '-' || e[0] == '_' ||
		e == "." || strings.Contains(e, "..") {
		return false
	}
	for i := 0; i < len(e); i++ {
		c := e[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-', c == '*':
		default:
			return false
		}
	}
	return true
}

// collapseStars reduces every run of '*' to one '*'.
func collapseStars(s string) string {
	for strings.Contains(s, "**") {
		s = strings.ReplaceAll(s, "**", "*")
	}
	return s
}

// kibanaPatternCovers reports whether every name elem can match also matches
// glob (only * is a wildcard, case-sensitive). A glob * may eat any element
// byte, an element * included; an element * is eaten only by a glob *. Sound:
// it never says covered for an uncovered pair, and may refuse exotic covered ones.
func kibanaPatternCovers(elem, glob string) bool {
	e, g := collapseStars(elem), collapseStars(glob)
	// memo[i][j]: 0 unknown, 1 true, 2 false.
	memo := make([][]byte, len(e)+1)
	for i := range memo {
		memo[i] = make([]byte, len(g)+1)
	}
	var m func(i, j int) bool
	m = func(i, j int) bool {
		if memo[i][j] != 0 {
			return memo[i][j] == 1
		}
		var r bool
		switch {
		case j == len(g):
			r = i == len(e)
		case g[j] == '*':
			r = m(i, j+1) || (i < len(e) && m(i+1, j))
		default:
			r = i < len(e) && e[i] != '*' && e[i] == g[j] && m(i+1, j+1)
		}
		if r {
			memo[i][j] = 1
		} else {
			memo[i][j] = 2
		}
		return r
	}
	return m(0, 0)
}
