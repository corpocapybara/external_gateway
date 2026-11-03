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

func TestTaintVariants(t *testing.T) {
	secret := "pa\"ss<w>&or\\d-é\n" // trailing newline, quote, HTML chars, backslash, non-ASCII
	cases := map[string]string{
		"json html-escaped": "{\"a\":\"pa\\\"ss\\u003cw\\u003e\\u0026or\\\\d-é\"}",
		"json plain":        "{\"a\":\"pa\\\"ss<w>&or\\\\d-é\"}",
		"json untrimmed":    "{\"a\":\"pa\\\"ss<w>&or\\\\d-é\\n\"}",
		"raw trimmed":       "x pa\"ss<w>&or\\d-é y",
	}
	want := map[string]string{
		"json html-escaped": `{"a":"***"}`,
		"json plain":        `{"a":"***"}`,
		"json untrimmed":    `{"a":"***"}`,
		"raw trimmed":       "x *** y",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			tr := &TaintRegistry{seen: map[string]bool{}}
			tr.Taint([]byte(secret))
			if got := string(tr.Redact([]byte(in))); got != want[name] {
				t.Fatalf("got %q want %q", got, want[name])
			}
		})
	}
}

func TestTaintURLEncoded(t *testing.T) {
	tr := &TaintRegistry{seen: map[string]bool{}}
	tr.Taint([]byte("p@ss word!"))
	if got := string(tr.Redact([]byte("?k=p%40ss+word%21&j=p%40ss%20word%21"))); got != "?k=***&j=***" {
		t.Fatalf("got %q", got)
	}
}

func TestTaintShortAfterTrim(t *testing.T) {
	tr := &TaintRegistry{seen: map[string]bool{}}
	tr.Taint([]byte("ab\n\n\n")) // 5 bytes raw, 2 trimmed
	tr.Taint([]byte("abc"))
	if got := string(tr.Redact([]byte("ab abc"))); got != "ab abc" {
		t.Fatalf("short value must not redact unrelated text, got %q", got)
	}
}
