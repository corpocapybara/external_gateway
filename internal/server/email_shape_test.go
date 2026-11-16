package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/external_gateway/internal/connectors/email/emailtest"
)

// emailShapeOps declares hotmail.list_mailboxes and hotmail.read; with allow
// set, each gets the allow_fields of the shipped hotmail ops.
func emailShapeOps(allow bool) string {
	env := "    params:\n      env:\n        type: string\n        required: true\n"
	list := "  hotmail.list_mailboxes:\n    connector: email\n    site: hotmail\n" + env
	read := "  hotmail.read:\n    connector: email\n    site: hotmail\n" + env +
		"      mailbox:\n        type: string\n        default: INBOX\n      uid:\n        type: int\n        required: true\n"
	if allow {
		list += "    response:\n      allow_fields:\n        - mailboxes[].name\n"
		read += "    response:\n      allow_fields: [uid, subject, from, to, date, body, flags]\n"
	}
	return "operations:\n" + list + read
}

// emailShapeHarness serves both ops against the emailtest IMAP fake (memory
// backend: INBOX holding message uid 6), authenticated by XOAUTH2 token "tok".
func emailShapeHarness(t *testing.T, allow bool) *credHarness {
	t.Helper()
	imap := emailtest.StartIMAP(t, emailtest.IMAPOptions{TLS: testEmailTLS(t).Server, ValidToken: "tok"})
	env := "            host: 127.0.0.1\n            imap_port: " + strconv.Itoa(imap.Port) + "\n" +
		"            smtp_port: " + strconv.Itoa(emailtest.ClosedPort(t)) + "\n" +
		"            user: me@example.com\n            auth:\n              type: oauth2\n" +
		"            secret_ref: secret://w/hotmail\n"
	h := newCredHarness(t, map[string]string{"w": "      hotmail:\n        environments:\n          prod:\n" + env},
		[]string{"hotmail.list_mailboxes", "hotmail.read"}, emailShapeOps(allow))
	h.s.emailRootCAs = testEmailTLS(t).Roots
	h.store.Set("w", "hotmail", []byte("tok"))
	return h
}

// emailOp posts body to op through the HTTP handler and decodes the 200 reply,
// without the handler's envelope fields.
func emailOp(t *testing.T, h *credHarness, op, body string) map[string]interface{} {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/w/op/"+op, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+credAgentTok)
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", op, w.Code, w.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v in %s", op, err, w.Body.String())
	}
	if out["operation"] != op || out["workspace"] != "w" || out["upstream_status"] != float64(200) || out["request_id"] == nil {
		t.Fatalf("%s: envelope fields missing: %v", op, out)
	}
	for _, k := range []string{"request_id", "operation", "workspace", "upstream_status"} {
		delete(out, k) // the handler's envelope, added after shaping
	}
	return out
}

func keys(m map[string]interface{}) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// inbox returns the INBOX entry of a list_mailboxes reply.
func inbox(t *testing.T, out map[string]interface{}) map[string]interface{} {
	t.Helper()
	list, _ := out["mailboxes"].([]interface{})
	for _, m := range list {
		if mb, ok := m.(map[string]interface{}); ok && mb["name"] == "INBOX" {
			return mb
		}
	}
	t.Fatalf("no INBOX in %v", out)
	return nil
}

func TestEmailResponseShaped(t *testing.T) {
	h := emailShapeHarness(t, true)

	mb := inbox(t, emailOp(t, h, "hotmail.list_mailboxes", `{"env":"prod"}`))
	if len(mb) != 1 {
		t.Errorf("list_mailboxes entry keys = %v, want only name", keys(mb))
	}

	msg := emailOp(t, h, "hotmail.read", `{"env":"prod","uid":6}`)
	for _, k := range []string{"cc", "seq"} { // leakcheck:allow (email carbon-copy field)
		if _, ok := msg[k]; ok {
			t.Errorf("read returned %q although allow_fields omit it: %v", k, keys(msg))
		}
	}
	if msg["uid"] != float64(6) || msg["subject"] != "A little message, just for you" ||
		msg["body"] == nil || msg["from"] == nil || msg["to"] == nil || msg["date"] == nil || msg["flags"] == nil {
		t.Errorf("read lost an allowed field: %v", msg)
	}
	if len(msg) != 7 {
		t.Errorf("read keys = %v, want the 7 allowed", keys(msg))
	}
}

func TestEmailResponseNoAllowFieldsUnchanged(t *testing.T) {
	h := emailShapeHarness(t, false)

	mb := inbox(t, emailOp(t, h, "hotmail.list_mailboxes", `{"env":"prod"}`))
	if _, ok := mb["attrs"]; !ok {
		t.Errorf("list_mailboxes entry lost attrs without allow_fields: %v", mb)
	}

	msg := emailOp(t, h, "hotmail.read", `{"env":"prod","uid":6}`)
	for _, k := range []string{"uid", "seq", "subject", "from", "to", "cc", "date", "flags", "body"} { // leakcheck:allow (email carbon-copy field)
		if _, ok := msg[k]; !ok {
			t.Errorf("read lost %q without allow_fields: %v", k, keys(msg))
		}
	}
	if len(msg) != 9 {
		t.Errorf("read keys = %v, want the connector's 9", keys(msg))
	}
}
