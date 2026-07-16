package redact

import (
	"regexp"
	"sync"

	"github.com/external_gateway/internal/secrets"
)

type Hook struct {
	mu         sync.RWMutex
	patterns   []*regexp.Regexp
}

var globalHook *Hook

func GetHook() *Hook {
	if globalHook == nil {
		globalHook = &Hook{}
	}
	return globalHook
}

func (h *Hook) RedactString(data string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := data
	for _, pattern := range h.patterns {
		result = pattern.ReplaceAllString(result, "***")
	}
	return result
}

func (h *Hook) RedactBytes(data []byte) []byte {
	return []byte(h.RedactString(string(data)))
}

func (h *Hook) ScanAndRedact(data []byte, taintRegistry *secrets.TaintRegistry) []byte {
	if taintRegistry == nil {
		taintRegistry = secrets.GetTaintRegistry()
	}
	return taintRegistry.Redact(data)
}

func (h *Hook) AddPattern(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.patterns = append(h.patterns, re)
	return nil
}

type ResponseShaper struct {
	allowFields []string
}

func NewResponseShaper(allowFields []string) *ResponseShaper {
	return &ResponseShaper{allowFields: allowFields}
}

func (s *ResponseShaper) Shape(data map[string]interface{}) map[string]interface{} {
	if s.allowFields == nil {
		return data
	}

	result := make(map[string]interface{})
	for _, field := range s.allowFields {
		if projected, ok := project(data, splitPath(field)).(map[string]interface{}); ok {
			deepMerge(result, projected)
		}
	}
	return result
}

// project extracts the value at `parts` from src, preserving object and array
// shape. A "[]" segment maps the remaining path over EVERY element of an array
// (index-aligned), so `channels[].id` yields every channel's id — not just the
// first. Returns nil when the path is absent.
func project(src interface{}, parts []string) interface{} {
	if len(parts) == 0 {
		return src
	}
	if parts[0] == "[]" {
		arr, ok := src.([]interface{})
		if !ok {
			return nil
		}
		out := make([]interface{}, len(arr))
		any := false
		for i, item := range arr {
			v := project(item, parts[1:])
			out[i] = v
			if v != nil {
				any = true
			}
		}
		if !any {
			return nil
		}
		return out
	}
	m, ok := src.(map[string]interface{})
	if !ok {
		return nil
	}
	child, ok := m[parts[0]]
	if !ok {
		return nil
	}
	v := project(child, parts[1:])
	if v == nil {
		return nil
	}
	return map[string]interface{}{parts[0]: v}
}

// deepMerge merges src into dst, combining nested maps and index-aligning arrays
// so multiple allow_fields on the same array (channels[].id + channels[].name)
// collect onto the same elements instead of overwriting each other.
func deepMerge(dst map[string]interface{}, src map[string]interface{}) {
	for k, v := range src {
		if existing, ok := dst[k]; ok {
			dst[k] = mergeValues(existing, v)
		} else {
			dst[k] = v
		}
	}
}

func mergeValues(a, b interface{}) interface{} {
	if am, ok := a.(map[string]interface{}); ok {
		if bm, ok := b.(map[string]interface{}); ok {
			deepMerge(am, bm)
			return am
		}
	}
	if aa, ok := a.([]interface{}); ok {
		if ba, ok := b.([]interface{}); ok {
			n := len(aa)
			if len(ba) > n {
				n = len(ba)
			}
			out := make([]interface{}, n)
			for i := 0; i < n; i++ {
				var ai, bi interface{}
				if i < len(aa) {
					ai = aa[i]
				}
				if i < len(ba) {
					bi = ba[i]
				}
				switch {
				case ai == nil:
					out[i] = bi
				case bi == nil:
					out[i] = ai
				default:
					out[i] = mergeValues(ai, bi)
				}
			}
			return out
		}
	}
	if b != nil {
		return b
	}
	return a
}

// splitPath turns "hits.hits[]._source.message" into
// ["hits","hits","[]","_source","message"] — the "[]" marker is preserved so
// project can map across arrays.
func splitPath(path string) []string {
	var parts []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, string(cur))
			cur = nil
		}
	}
	runes := []rune(path)
	for i := 0; i < len(runes); i++ {
		switch {
		case runes[i] == '.':
			flush()
		case runes[i] == '[' && i+1 < len(runes) && runes[i+1] == ']':
			flush()
			parts = append(parts, "[]")
			i++ // skip the ']'
		default:
			cur = append(cur, runes[i])
		}
	}
	flush()
	return parts
}
