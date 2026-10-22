package server

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

func TestMultipartUploadWithOpHeaders(t *testing.T) {
	var gotQuery, gotToken, gotFilename, gotPartType string
	var gotContent []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotToken = r.Header.Get("X-Atlassian-Token")
		_, mp, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, mp["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Errorf("first part: %v", err)
			return
		}
		if part.FormName() != "file" {
			t.Errorf("form field = %q, want file", part.FormName())
		}
		gotFilename = part.FileName()
		gotPartType = part.Header.Get("Content-Type")
		gotContent, _ = io.ReadAll(part)
		w.Write([]byte(`[{"id":"1","filename":"x"}]`))
	}))
	defer upstream.Close()

	ws := &config.Workspace{Sites: map[string]config.Site{"corp": {Environments: map[string]config.Environment{
		"prod": {BaseURL: upstream.URL},
	}}}}
	op := &policy.Operation{
		ID:        "corp.attach",
		Connector: "http-rest",
		Site:      "corp",
		Method:    "POST",
		Path:      "/rest/api/2/issue/{key}/attachments",
		Headers:   map[string]string{"X-Atlassian-Token": "no-check"},
		Multipart: &policy.MultipartSpec{Field: "file", FilenameParam: "filename", ContentParam: "content_base64"},
	}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}
	content := []byte("round 42: x25\n\x00binary")
	params := map[string]interface{}{
		"key":            "DESK-1",
		"filename":       `round "42".txt`,
		"content_base64": base64.StdEncoding.EncodeToString(content),
	}
	if _, _, err := s.executeHttpConnector(ws, op, params, "multiparttest"); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "" {
		t.Errorf("file params leaked into the query: %q", gotQuery)
	}
	if gotToken != "no-check" {
		t.Errorf("X-Atlassian-Token = %q", gotToken)
	}
	if gotFilename != `round "42".txt` || string(gotContent) != string(content) {
		t.Errorf("part filename %q content %q", gotFilename, gotContent)
	}
	if !strings.HasPrefix(gotPartType, "text/plain") {
		t.Errorf("part content type = %q, want text/plain", gotPartType)
	}

	params["content_base64"] = "not base64!"
	if _, _, err := s.executeHttpConnector(ws, op, params, "multiparttest"); err == nil {
		t.Fatal("expected invalid base64 to be rejected")
	}
	params["content_base64"] = base64.StdEncoding.EncodeToString(content)
	params["filename"] = "a.txt\r\nContent-Type: text/html"
	if _, _, err := s.executeHttpConnector(ws, op, params, "multiparttest"); err == nil {
		t.Fatal("expected a filename with CR/LF to be rejected")
	}
}

func TestAuditParamsRedactsUploadContent(t *testing.T) {
	op := &policy.Operation{Multipart: &policy.MultipartSpec{Field: "file", FilenameParam: "filename", ContentParam: "content_base64"}}
	params := map[string]interface{}{"key": "DESK-1", "filename": "a.txt", "content_base64": "c2VjcmV0IGZpbGU="}
	got := auditParams(op, params)
	if got["content_base64"] != "<16 base64 chars>" || got["filename"] != "a.txt" || got["key"] != "DESK-1" {
		t.Errorf("audit params = %v", got)
	}
	if params["content_base64"] != "c2VjcmV0IGZpbGU=" {
		t.Error("auditParams must not modify the params sent upstream")
	}
	plain := map[string]interface{}{"query": strings.Repeat("x", 10000)}
	if got := auditParams(&policy.Operation{}, plain); got["query"] != plain["query"] {
		t.Error("params of an op without multipart must be audited unchanged")
	}
}
