package email

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

func unreachableCfg() *Config {
	// Port 1 on loopback: if validation is missing, Send fails with a dial error instead.
	return &Config{SmtpHost: "127.0.0.1", SmtpPort: 1, SmtpTLS: false, User: "me@example.com"}
}

func TestSendRejectsHeaderInjection(t *testing.T) {
	c := NewConnector()
	cases := []struct{ name, to, subject string }{
		{"subject CRLF", "a@example.com", "x\r\nBcc: victim@evil.com"},
		{"subject LF", "a@example.com", "x\nBcc: victim@evil.com"},
		{"subject NUL", "a@example.com", "x\x00y"},
		{"to CRLF", "a@example.com\r\nBcc: victim@evil.com", "ok"},
		{"to list", "a@example.com, b@evil.com", "ok"},
		{"to display name", "Evil <b@evil.com>", "ok"},
		{"to garbage", "not-an-address", "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Send(unreachableCfg(), tc.to, tc.subject, "body")
			if err == nil || !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("want validation error containing %q, got %v", "invalid", err)
			}
		})
	}
}

func TestSendAcceptsValidInputReachesDial(t *testing.T) {
	err := NewConnector().Send(unreachableCfg(), "a@example.com", "Zażółć gęślą jaźń", "body")
	if err == nil || strings.Contains(err.Error(), "invalid") {
		t.Fatalf("valid input must pass validation and fail at dial, got %v", err)
	}
}

func TestBuildMessageEncodesNonASCIISubject(t *testing.T) {
	m := buildMessage("me@example.com", "a@example.com", "Zażółć", "b")
	if !strings.Contains(m, "Subject: =?UTF-8?q?Za=C5=BC") {
		t.Fatalf("subject not Q-encoded: %q", m)
	}
}

func TestParseRecipient(t *testing.T) {
	ok := map[string]string{"a@example.com": "a@example.com", "  a@example.com ": "a@example.com"}
	for in, want := range ok {
		if got, err := ParseRecipient(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "<a@example.com>", "A <a@example.com>", "a@example.com\t,b@evil.com", "a@example.com (c)"} {
		if _, err := ParseRecipient(in); err == nil {
			t.Errorf("%q must be rejected", in)
		}
	}
}

func TestSearchEntryNilEnvelope(t *testing.T) {
	if _, ok := searchEntry(&imap.Message{SeqNum: 1}, ""); ok {
		t.Fatal("message without envelope must be skipped")
	}
	if _, ok := searchEntry(&imap.Message{Envelope: &imap.Envelope{Subject: "hello"}}, "HELLO"); !ok {
		t.Fatal("matching message must be returned")
	}
	if _, ok := searchEntry(&imap.Message{Envelope: &imap.Envelope{Subject: "hello"}}, "zzz"); ok {
		t.Fatal("non-matching message must be skipped")
	}
}

func TestReadBodyCapsAndReportsTruncation(t *testing.T) {
	got, trunc, err := readBody(strings.NewReader(strings.Repeat("a", 100)), 10)
	if err != nil || !trunc || got != strings.Repeat("a", 10) {
		t.Fatalf("got %q trunc=%v err=%v", got, trunc, err)
	}
	got, trunc, err = readBody(strings.NewReader("abcde"), 5)
	if err != nil || trunc || got != "abcde" {
		t.Fatalf("exact-limit body: got %q trunc=%v err=%v", got, trunc, err)
	}
}
