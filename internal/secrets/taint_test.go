package secrets

import (
	"bytes"
	"testing"
)

func TestTaintRedact(t *testing.T) {
	tr := &TaintRegistry{seen: map[string]bool{}}
	tr.Taint([]byte("supersecret-token-value"))
	tr.Taint([]byte("abcd")) // below the length floor, must be ignored

	in := []byte(`{"authorization":"Bearer supersecret-token-value","x":"abcd"}`)
	out := tr.Redact(in)

	if bytes.Contains(out, []byte("supersecret-token-value")) {
		t.Fatalf("secret value not redacted: %s", out)
	}
	if !bytes.Contains(out, []byte("***")) {
		t.Fatalf("expected redaction placeholder in: %s", out)
	}
	if !bytes.Contains(out, []byte("abcd")) {
		t.Fatalf("short value should be preserved, got: %s", out)
	}
}

func TestTaintRedactNoValues(t *testing.T) {
	tr := &TaintRegistry{seen: map[string]bool{}}
	in := []byte("nothing to redact here")
	if got := tr.Redact(in); !bytes.Equal(got, in) {
		t.Fatalf("expected passthrough, got: %s", got)
	}
}

func TestTaintDedup(t *testing.T) {
	tr := &TaintRegistry{seen: map[string]bool{}}
	tr.Taint([]byte("repeated-secret-abc"))
	tr.Taint([]byte("repeated-secret-abc"))
	if len(tr.values) != 1 {
		t.Fatalf("expected 1 stored value after dedup, got %d", len(tr.values))
	}
}
