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
// "/" is only allowed when the param opts in (multiSegment). "%" is refused in a
// single-segment value; a multi-segment value may carry %XX escapes (Jenkins
// branch jobs need release%252F9.19) except ones that decode to a path or query
// separator, a dot, or a control character, so %2e%2e / %2F stay refused.
func hasPathInjection(s string, multiSegment bool) bool {
	if strings.Contains(s, "..") || strings.ContainsAny(s, "?#\\") {
		return true
	}
	if strings.Contains(s, "%") && (!multiSegment || hasUnsafeEscape(s)) {
		return true
	}
	if !multiSegment && strings.Contains(s, "/") {
		return true
	}
	if multiSegment {
		for _, seg := range strings.Split(s, "/") {
			if seg == "" || seg == "." {
				return true
			}
		}
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// hasUnsafeEscape reports whether s has a "%" that does not start a two-hex-digit
// escape, or an escape decoding to "/", "\", ".", "?", "#", ";", or a control character.
func hasUnsafeEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
			return true
		}
		b := unhex(s[i+1])<<4 | unhex(s[i+2])
		if b < 0x20 || b == 0x7f || strings.IndexByte("/\\.?#;", b) >= 0 {
			return true
		}
		i += 2
	}
	return false
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
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
			if hasPathInjection(str, defs[key].MultiSegment) ||
				strings.Contains(str, ";") ||
				(strings.Contains(before, "?") && strings.ContainsAny(str, "&=+")) {
				return "", bad
			}
		}
		b.WriteString(str)
	}
	b.WriteString(tmpl[last:])
	return b.String(), nil
}
