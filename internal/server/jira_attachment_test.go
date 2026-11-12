package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// T0008: jira_attachment_content. Design docs/design/T0008-jira-attachment-content.md §Tests.

const attOps = "operations:\n" +
	"  jira.att:\n    connector: http-rest\n    site: jira\n    method: GET\n    path: /x\n" +
	"    jira_attachment_content: true\n" +
	"    params:\n      key: {type: string, required: true}\n      id: {type: string, required: true}\n"

type attEntry struct {
	ID, Filename, MimeType string
	Size                   interface{} // nil = absent
	Content                string      // upstream "content" URL, must be ignored
}

// mockJira serves the issue call and the download and records every request's
// escaped RequestURI and Authorization header.
type mockJira struct {
	srv         *httptest.Server
	mu          sync.Mutex
	uris, auths []string

	issueStatus int
	issueBody   string
	dlStatus    int
	dlLocation  string
	dlCT        string // "" = no Content-Type header at all
	dlBody      []byte
}

func newMockJira(t *testing.T, tls bool) *mockJira {
	m := &mockJira{issueStatus: 200, dlStatus: 200}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.uris = append(m.uris, r.RequestURI)
		m.auths = append(m.auths, r.Header.Get("Authorization"))
		m.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(m.issueStatus)
			w.Write([]byte(m.issueBody))
			return
		}
		if m.dlCT == "" {
			w.Header()["Content-Type"] = nil // stop net/http sniffing one
		} else {
			w.Header().Set("Content-Type", m.dlCT)
		}
		if m.dlLocation != "" {
			w.Header().Set("Location", m.dlLocation)
		}
		w.WriteHeader(m.dlStatus)
		w.Write(m.dlBody)
	})
	if tls {
		m.srv = httptest.NewTLSServer(h)
	} else {
		m.srv = httptest.NewServer(h)
	}
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockJira) requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.uris...)
}

func issueJSON(key string, atts ...attEntry) string {
	list := make([]map[string]interface{}, 0, len(atts))
	for _, a := range atts {
		e := map[string]interface{}{"id": a.ID, "filename": a.Filename, "mimeType": a.MimeType}
		if a.Size != nil {
			e["size"] = a.Size
		}
		if a.Content != "" {
			e["content"] = a.Content
		}
		list = append(list, e)
	}
	b, _ := json.Marshal(map[string]interface{}{"key": key, "fields": map[string]interface{}{"attachment": list}})
	return string(b)
}

// attEnv returns the sites block for workspace ws1 with the given env line body.
func attSite(env string) string {
	return "      jira:\n        environments:\n          prod: {" + env + "}\n"
}

func bearerEnv(base string) string {
	return "base_url: " + base + ", auth: {type: bearer}, secret_ref: secret://ws1/jira"
}

func newAttHarness(t *testing.T, env string) *credHarness {
	t.Helper()
	h := newCredHarness(t, map[string]string{"ws1": attSite(env)}, []string{"jira.att"}, attOps)
	h.store.Set("ws1", "jira", []byte(" s3cr3t\n"))
	return h
}

func attCall(t *testing.T, h *credHarness, key, id string) (int, map[string]interface{}) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"key": key, "id": id})
	r := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws1/op/jira.att", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+credAgentTok)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("reply not JSON (%d): %s", w.Code, w.Body.String())
	}
	return w.Code, out
}

const issueURI = "/rest/api/2/issue/SRTHP-1?fields=attachment"

func wantRequests(t *testing.T, m *mockJira, want ...string) {
	t.Helper()
	got := m.requests()
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream requests = %q, want %q", got, want)
	}
}

func wantRefusal(t *testing.T, code int, out map[string]interface{}, detail string) {
	t.Helper()
	if code != 403 || out["error"] != "denied" || out["rule"] != "constraint" {
		t.Fatalf("want 403 denied/constraint, got %d %v", code, out)
	}
	if d, _ := out["detail"].(string); d == "" || !strings.Contains(d, detail) {
		t.Fatalf("detail %q does not contain %q", out["detail"], detail)
	}
}

func wantError(t *testing.T, code int, out map[string]interface{}, text string) {
	t.Helper()
	if code != 200 || out["status"] != "error" {
		t.Fatalf("want status-error reply, got %d %v", code, out)
	}
	if e, _ := out["error"].(string); !strings.Contains(e, text) {
		t.Fatalf("error %q does not contain %q", out["error"], text)
	}
}

var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x00, 0xFF, 0x10, 0x00, 0xFE}

func TestJiraAttachmentPNGRoundTripAndHeaders(t *testing.T) {
	m := newMockJira(t, false)
	m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "a b/ü.png", MimeType: "image/png", Size: len(pngBytes)})
	m.dlCT, m.dlBody = "image/png", pngBytes
	h := newAttHarness(t, bearerEnv(m.srv.URL+"/"))
	code, out := attCall(t, h, "SRTHP-1", "10")
	if code != 200 {
		t.Fatalf("status %d %v", code, out)
	}
	wantRequests(t, m, issueURI, "/secure/attachment/10/a%20b%2F%C3%BC.png")
	for i, a := range m.auths {
		if a != "Bearer s3cr3t" {
			t.Fatalf("call %d Authorization = %q", i, a)
		}
	}
	want := map[string]interface{}{"id": "10", "filename": "a b/ü.png", "mimeType": "image/png", "size": float64(len(pngBytes))}
	if !reflect.DeepEqual(out["attachment"], want) || out["encoding"] != "base64" {
		t.Fatalf("reply = %v", out)
	}
	got, err := base64.StdEncoding.DecodeString(out["content"].(string))
	if err != nil || !bytes.Equal(got, pngBytes) {
		t.Fatalf("content = %v (%v), want %v", got, err, pngBytes)
	}
}

func TestJiraAttachmentBasicAuth(t *testing.T) {
	m := newMockJira(t, false)
	m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "a.txt", MimeType: "text/plain"})
	m.dlCT, m.dlBody = "text/plain", []byte("hi")
	h := newAttHarness(t, "base_url: "+m.srv.URL+", auth: {type: basic}, user: u, secret_ref: secret://ws1/jira")
	if code, out := attCall(t, h, "SRTHP-1", "10"); code != 200 || out["content"] != "hi" {
		t.Fatalf("%d %v", code, out)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:s3cr3t"))
	if len(m.auths) != 2 || m.auths[0] != want || m.auths[1] != want {
		t.Fatalf("Authorization = %q, want %q twice", m.auths, want)
	}
}

func TestJiraAttachmentTypesEndToEnd(t *testing.T) {
	cases := []struct {
		name, listed, served string
		body                 []byte
		enc, content         string
	}{
		{"pdf", "application/pdf", "application/pdf", []byte("%PDF\x00\xff"), "base64", base64.StdEncoding.EncodeToString([]byte("%PDF\x00\xff"))},
		{"invalid utf8 text", "text/plain", "text/plain; charset=utf-8", []byte("a\xffb"), "text", "a�b"},
		{"listed with case and params", "IMAGE/PNG; name=x", "image/png", pngBytes, "base64", base64.StdEncoding.EncodeToString(pngBytes)},
		{"jpeg served for listed png", "image/png", "image/jpeg", pngBytes, "base64", base64.StdEncoding.EncodeToString(pngBytes)},
		{"no content type", "image/png", "", pngBytes, "base64", base64.StdEncoding.EncodeToString(pngBytes)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMockJira(t, false)
			m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f", MimeType: c.listed})
			m.dlCT, m.dlBody = c.served, c.body
			h := newAttHarness(t, bearerEnv(m.srv.URL))
			code, out := attCall(t, h, "SRTHP-1", "10")
			if code != 200 || out["encoding"] != c.enc || out["content"] != c.content {
				t.Fatalf("%d %v", code, out)
			}
		})
	}
}

func TestAttachmentClass(t *testing.T) {
	for m, want := range map[string]string{
		"image/png": "image", "image/jpeg": "image", "image/gif": "image", "image/webp": "image",
		"application/pdf": "pdf", "text/plain": "text", "text/csv": "text", "application/json": "text",
		"application/xml": "text", "application/yaml": "text", "application/x-yaml": "text",
		"text/html": "", "application/zip": "", "application/octet-stream": "", "": "", "image/svg+xml": "",
	} {
		if got := attachmentClass(normMime(m)); got != want {
			t.Errorf("attachmentClass(%q) = %q, want %q", m, got, want)
		}
	}
	if got := normMime(" TEXT/HTML; charset=utf-8"); got != "text/html" {
		t.Errorf("normMime = %q", got)
	}
}

func TestJiraAttachmentScopeAndRefusals(t *testing.T) {
	other := newMockJira(t, false)
	cases := []struct {
		name, body, detail string
	}{
		{"id not listed", issueJSON("SRTHP-1", attEntry{ID: "11", Filename: "f", MimeType: "image/png"}), "attachment 10 is not on issue SRTHP-1"},
		{"prefix and suffix ids", issueJSON("SRTHP-1", attEntry{ID: "101", Filename: "f", MimeType: "image/png"}, attEntry{ID: "1", Filename: "f", MimeType: "image/png"}), "not on issue"},
		{"leading zero id", issueJSON("SRTHP-1", attEntry{ID: "010", Filename: "f", MimeType: "image/png"}), "not on issue"},
		{"attachment absent", `{"key":"SRTHP-1","fields":{}}`, "not on issue"},
		{"attachment empty", issueJSON("SRTHP-1"), "not on issue"},
		{"moved issue", issueJSON("SRTHP-2", attEntry{ID: "10", Filename: "f", MimeType: "image/png"}), `answered as "SRTHP-2"`},
		{"zip", issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f", MimeType: "application/zip"}), `"application/zip" is not served`},
		{"html with params", issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f", MimeType: "TEXT/HTML; charset=utf-8"}), "is not served"},
		{"over size", issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f", MimeType: "image/png", Size: 1048577}), "1048577 bytes; the limit is 1048576"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMockJira(t, false)
			m.issueBody = c.body
			h := newAttHarness(t, bearerEnv(m.srv.URL))
			code, out := attCall(t, h, "SRTHP-1", "10")
			wantRefusal(t, code, out, c.detail)
			wantRequests(t, m, issueURI)
		})
	}
	// The upstream content URL is never used, and the first matching entry wins.
	m := newMockJira(t, false)
	m.issueBody = issueJSON("SRTHP-1",
		attEntry{ID: "10", Filename: "first.png", MimeType: "image/png", Size: 1048576, Content: other.srv.URL + "/steal"},
		attEntry{ID: "10", Filename: "second.png", MimeType: "image/png"})
	m.dlCT, m.dlBody = "image/png", pngBytes
	h := newAttHarness(t, bearerEnv(m.srv.URL))
	if code, out := attCall(t, h, "SRTHP-1", "10"); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	wantRequests(t, m, issueURI, "/secure/attachment/10/first.png")
	wantRequests(t, other)
}

func TestJiraAttachmentPreRequestErrors(t *testing.T) {
	m := newMockJira(t, false)
	base := m.srv.URL
	cases := []struct{ name, env, key, id, text string }{
		{"api-key auth", "base_url: " + base + ", auth: {type: api-key, header: X-K}, secret_ref: secret://ws1/jira", "SRTHP-1", "10", `site uses "api-key"`},
		{"empty secret_ref", "base_url: " + base + ", auth: {type: bearer}", "SRTHP-1", "10", "needs a secret_ref"},
		{"secret missing", "base_url: " + base + ", auth: {type: bearer}, secret_ref: secret://ws1/nope", "SRTHP-1", "10", "resolving credentials"},
		{"secret in other workspace", "base_url: " + base + ", auth: {type: bearer}, secret_ref: secret://other/jira", "SRTHP-1", "10", "resolving credentials"},
		{"basic without user", "base_url: " + base + ", auth: {type: basic}, secret_ref: secret://ws1/jira", "SRTHP-1", "10", "needs a user"},
		{"proxy_host", bearerEnv(base) + ", proxy_host: 127.0.0.1", "SRTHP-1", "10", "proxy_host"},
		{"placeholder base_url", "base_url: '-', auth: {type: bearer}, secret_ref: secret://ws1/jira", "SRTHP-1", "10", "no base_url"},
		{"bad key", bearerEnv(base), "x", "10", "key must be a Jira issue key"},
		{"bad id", bearerEnv(base), "SRTHP-1", "1/../2", "id must match"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAttHarness(t, c.env)
			h.store.Set("other", "jira", []byte("x"))
			code, out := attCall(t, h, c.key, c.id)
			wantError(t, code, out, c.text)
			wantRequests(t, m)
		})
	}
}

func TestJiraAttachmentUpstreamErrors(t *testing.T) {
	big := bytes.Repeat([]byte{'a'}, 1048577)
	ok := issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f.png", MimeType: "image/png"})
	dl := "/secure/attachment/10/f.png"
	cases := []struct {
		name        string
		setup       func(m *mockJira)
		text        string
		wantDL      bool
	}{
		{"issue 404", func(m *mockJira) { m.issueStatus, m.issueBody = 404, `{}` }, "returned status 404", false},
		{"issue 302", func(m *mockJira) { m.issueStatus, m.issueBody = 302, `` }, "returned status 302", false},
		{"issue over cap", func(m *mockJira) { m.issueBody = string(big) }, "response exceeds 1048576 bytes", false},
		{"issue bad json", func(m *mockJira) { m.issueBody = `{"key":` }, "unreadable response", false},
		{"filename dotdot", func(m *mockJira) {
			m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "..", MimeType: "image/png"})
		}, "unusable filename", false},
		{"filename empty", func(m *mockJira) {
			m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "", MimeType: "image/png"})
		}, "unusable filename", false},
		{"download 404", func(m *mockJira) { m.issueBody, m.dlStatus = ok, 404 }, "download returned status 404", true},
		{"download over cap", func(m *mockJira) { m.issueBody, m.dlCT, m.dlBody = ok, "image/png", big }, "response exceeds 1048576 bytes", true},
		{"png listed, html served", func(m *mockJira) { m.issueBody, m.dlCT, m.dlBody = ok, "text/html", []byte("<x>") }, `content type "text/html", expected image`, true},
		{"pdf listed, png served", func(m *mockJira) {
			m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f.png", MimeType: "application/pdf"})
			m.dlCT, m.dlBody = "image/png", pngBytes
		}, "expected pdf", true},
		{"text listed, png served", func(m *mockJira) {
			m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f.png", MimeType: "text/plain"})
			m.dlCT, m.dlBody = "image/png", pngBytes
		}, "expected text", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMockJira(t, false)
			c.setup(m)
			h := newAttHarness(t, bearerEnv(m.srv.URL))
			code, out := attCall(t, h, "SRTHP-1", "10")
			wantError(t, code, out, c.text)
			if c.wantDL {
				wantRequests(t, m, issueURI, dl)
			} else {
				wantRequests(t, m, issueURI)
			}
		})
	}
}

func TestJiraAttachmentRedirectNotFollowed(t *testing.T) {
	elsewhere := newMockJira(t, false)
	m := newMockJira(t, false)
	m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f.png", MimeType: "image/png"})
	m.dlStatus, m.dlLocation = 302, elsewhere.srv.URL+"/f.png"
	h := newAttHarness(t, bearerEnv(m.srv.URL))
	code, out := attCall(t, h, "SRTHP-1", "10")
	wantError(t, code, out, "download returned status 302")
	wantRequests(t, m, issueURI, "/secure/attachment/10/f.png")
	wantRequests(t, elsewhere)
}

// The secret is passed as Credentials, so the connector taints it and the reply's
// redaction rewrites it even inside attachment text (design §Security).
func TestJiraAttachmentSecretTaintedInReply(t *testing.T) {
	m := newMockJira(t, false)
	m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f.txt", MimeType: "text/plain"})
	m.dlCT, m.dlBody = "text/plain", []byte("token=tAInt3dS3cretVal ok")
	h := newCredHarness(t, map[string]string{"ws1": attSite(bearerEnv(m.srv.URL))}, []string{"jira.att"}, attOps)
	h.store.Set("ws1", "jira", []byte("tAInt3dS3cretVal"))
	code, out := attCall(t, h, "SRTHP-1", "10")
	c, _ := out["content"].(string)
	if code != 200 || strings.Contains(c, "tAInt3dS3cretVal") || !strings.HasSuffix(c, " ok") {
		t.Fatalf("secret not redacted: %d %q", code, c)
	}
}

func TestJiraAttachmentEmptySecret(t *testing.T) {
	m := newMockJira(t, false)
	h := newAttHarness(t, bearerEnv(m.srv.URL))
	h.store.Set("ws1", "jira", []byte(" \n"))
	code, out := attCall(t, h, "SRTHP-1", "10")
	wantError(t, code, out, "is empty")
	wantRequests(t, m)
}

func TestJiraAttachmentBodyAtCapAccepted(t *testing.T) {
	m := newMockJira(t, false)
	m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f.txt", MimeType: "text/plain"})
	m.dlCT, m.dlBody = "text/plain", bytes.Repeat([]byte{'a'}, 1048576)
	h := newAttHarness(t, bearerEnv(m.srv.URL))
	if code, out := attCall(t, h, "SRTHP-1", "10"); code != 200 || len(out["content"].(string)) != 1048576 {
		t.Fatalf("%d status=%v", code, out["status"])
	}
}

func TestJiraAttachmentInsecureTLS(t *testing.T) {
	for _, insecure := range []bool{true, false} {
		t.Run(fmt.Sprint(insecure), func(t *testing.T) {
			m := newMockJira(t, true)
			m.issueBody = issueJSON("SRTHP-1", attEntry{ID: "10", Filename: "f.png", MimeType: "image/png"})
			m.dlCT, m.dlBody = "image/png", pngBytes
			h := newAttHarness(t, bearerEnv(m.srv.URL)+fmt.Sprintf(", insecure_skip_verify: %v", insecure))
			code, out := attCall(t, h, "SRTHP-1", "10")
			if insecure && (code != 200 || out["encoding"] != "base64") {
				t.Fatalf("insecure: %d %v", code, out)
			}
			if !insecure {
				wantError(t, code, out, "jira issue SRTHP-1")
			}
		})
	}
}
