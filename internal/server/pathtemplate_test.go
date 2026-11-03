package server

import (
	"strings"
	"testing"

	"github.com/external_gateway/internal/policy"
)

func TestExpandPathTemplateRejectsInjection(t *testing.T) {
	bad := []string{"%2e%2e%2f", "%2F", "a%2fb", "a/b", "/etc", "..", "a..b", "a?b", "a#b", `a\b`, "a\x00b", "%"}
	for _, v := range bad {
		_, err := expandPathTemplate("/issue/{key}/comment", nil, map[string]interface{}{"key": v}, nil)
		if err == nil {
			t.Errorf("value %q should be rejected", v)
		}
	}
	for _, v := range []string{"ABC-123", "feature-x", "a.b", "v1.2"} {
		got, err := expandPathTemplate("/issue/{key}/comment", nil, map[string]interface{}{"key": v}, nil)
		if err != nil || got != "/issue/"+v+"/comment" {
			t.Errorf("value %q: got %q, %v", v, got, err)
		}
	}
}

func TestExpandPathTemplateMultiSegmentOptIn(t *testing.T) {
	defs := map[string]policy.ParamDef{"path": {MultiSegment: true}}
	got, err := expandPathTemplate("/contents/{path}", nil, map[string]interface{}{"path": "src/a/b.go"}, defs)
	if err != nil || got != "/contents/src/a/b.go" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, v := range []string{"src/../x", "src/%2e%2e/x", "a%2Fb", "a?x"} {
		if _, err := expandPathTemplate("/contents/{path}", nil, map[string]interface{}{"path": v}, defs); err == nil {
			t.Errorf("multi_segment value %q should still be rejected", v)
		}
	}
}

func TestExpandPathTemplateURLEncode(t *testing.T) {
	got, err := expandPathTemplate("/projects/{p|urlencode}/x", nil, map[string]interface{}{"p": "group/sub proj"}, nil)
	if err != nil || got != "/projects/group%2Fsub%20proj/x" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := expandPathTemplate("/projects/{p|urlencode}/x", nil, map[string]interface{}{"p": ".."}, nil); err == nil {
		t.Error(".. must be rejected even with urlencode")
	}
	got, err = expandPathTemplate("/f?path={p}&x=1", nil, map[string]interface{}{"p": "a/b c"}, nil)
	if err != nil || got != "/f?path=a%2Fb+c&x=1" {
		t.Fatalf("query path: got %q, %v", got, err)
	}
}

func TestExpandPathTemplateSinglePass(t *testing.T) {
	// A value containing another placeholder must stay literal, whatever the
	// (random) map iteration order. "{" and "}" are legal path characters, so the
	// value is accepted but inert.
	for i := 0; i < 50; i++ {
		got, err := expandPathTemplate("/a/{x}/b/{y}", nil, map[string]interface{}{"x": "{y}", "y": "SECRET"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "/a/{y}/b/SECRET" {
			t.Fatalf("value was re-expanded: %q", got)
		}
	}
	got, _ := expandPathTemplate("/o/{site.org}/{r}", map[string]string{"site.org": "acme"}, map[string]interface{}{"r": "{site.org}"}, nil)
	if got != "/o/acme/{site.org}" || strings.Contains(got, "acme/acme") {
		t.Fatalf("got %q", got)
	}
}
