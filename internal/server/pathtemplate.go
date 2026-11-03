package server

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/external_gateway/internal/policy"
)

var pathPlaceholder = regexp.MustCompile(`\{([^{}]+)\}`)

// hasPathInjection reports whether a raw path-parameter value would break out of
// its path segment or inject a query/fragment when substituted unescaped.
// "/" is only allowed when the param opts in (multiSegment); "%" is never allowed
// raw, so no value can smuggle %2e%2e / %2F past the checks.
func hasPathInjection(s string, multiSegment bool) bool {
	if strings.Contains(s, "..") || strings.ContainsAny(s, "?#\\%") {
		return true
	}
	if !multiSegment && strings.Contains(s, "/") {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// expandPathTemplate substitutes every {placeholder} of an op path in ONE pass
// over the template, so a substituted value is never re-scanned (a value that
// contains "{other}" stays literal). Placeholders: {site.x} (trusted config),
// {key} (raw, validated), {key|urlencode} (escaped). A "?path={key}" or
// "&path={key}" placeholder is query-escaped. Unknown placeholders are kept as is.
func expandPathTemplate(tmpl string, site map[string]string, params map[string]interface{}, defs map[string]policy.ParamDef) (string, error) {
	locs := pathPlaceholder.FindAllStringSubmatchIndex(tmpl, -1)
	var b strings.Builder
	last := 0
	for _, m := range locs {
		b.WriteString(tmpl[last:m[0]])
		last = m[1]
		raw := tmpl[m[0]:m[1]]
		name := tmpl[m[2]:m[3]]
		if v, ok := site[name]; ok {
			b.WriteString(v)
			continue
		}
		encode := false
		key := name
		if strings.HasSuffix(name, "|urlencode") {
			encode = true
			key = strings.TrimSuffix(name, "|urlencode")
		}
		val, ok := params[key]
		if !ok {
			b.WriteString(raw)
			continue
		}
		str := fmt.Sprintf("%v", val)
		before := tmpl[:m[0]]
		inQuery := strings.HasSuffix(before, "?path=") || strings.HasSuffix(before, "&path=")
		bad := fmt.Errorf("parameter %q contains characters not allowed in a path value", key)
		switch {
		case inQuery:
			str = url.QueryEscape(str)
		case encode:
			if str == "." || str == ".." {
				return "", bad
			}
			str = url.PathEscape(str)
		default:
			if hasPathInjection(str, defs[key].MultiSegment) {
				return "", bad
			}
		}
		b.WriteString(str)
	}
	b.WriteString(tmpl[last:])
	return b.String(), nil
}
