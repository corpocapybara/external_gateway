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

func TestExpandPathTemplateQueryAndMatrixInjection(t *testing.T) {
	for _, v := range []string{"1&admin=true", "a=b", "a+b", "a;x=1"} {
		if _, err := expandPathTemplate("/x?id={id}", nil, map[string]interface{}{"id": v}, nil); err == nil {
			t.Errorf("query value %q should be rejected", v)
		}
	}
	if _, err := expandPathTemplate("/x/{id}", nil, map[string]interface{}{"id": "a;x=1"}, nil); err == nil {
		t.Error("; should be rejected in a path value")
	}
}

func TestExpandPathTemplateMultiSegmentOptIn(t *testing.T) {
	defs := map[string]policy.ParamDef{"path": {MultiSegment: true}}
	got, err := expandPathTemplate("/contents/{path}", nil, map[string]interface{}{"path": "src/a/b.go"}, defs)
	if err != nil || got != "/contents/src/a/b.go" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, v := range []string{"src/../x", "src/%2e%2e/x", "a%2Fb", "a?x", "a/./b", "a//b", "/a", "a/", "./a",
		"a%2fb", "a%5Cb", "a%2Eb", "a%3Fb", "a%23b", "a%00b", "a%0Ab", "a%7Fb", "a%3Bb", "a;b", "a%", "a%2", "a%zzb"} {
		if _, err := expandPathTemplate("/contents/{path}", nil, map[string]interface{}{"path": v}, defs); err == nil {
			t.Errorf("multi_segment value %q should still be rejected", v)
		}
	}
}

// A Jenkins multibranch job for branch release/9.19 is addressed as
// job/<repo>/job/release%252F9.19; that escape must pass in a multi_segment param.
func TestExpandPathTemplateMultiSegmentSafeEscapes(t *testing.T) {
	defs := map[string]policy.ParamDef{"job": {MultiSegment: true}}
	for _, v := range []string{"job/thp-backend/job/release%252F9.19", "job/x/job/feat%20y", "job/a/job/b%2Dc"} {
		got, err := expandPathTemplate("/{job}/api/json", nil, map[string]interface{}{"job": v}, defs)
		if err != nil || got != "/"+v+"/api/json" {
			t.Errorf("%q: got %q, %v", v, got, err)
		}
	}
	// a single-segment param still refuses any "%"
	if _, err := expandPathTemplate("/issue/{key}", nil, map[string]interface{}{"key": "a%2Db"}, nil); err == nil {
		t.Error("single-segment value with % should be rejected")
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
