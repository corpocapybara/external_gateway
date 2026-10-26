package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

const testFileID = "F0FILE123"

func TestValidateSlackFileURL(t *testing.T) {
	base := "https://files.slack.com"
	good := "https://files.slack.com/files-pri/T0TEAM1-" + testFileID + "/untitled"
	if _, err := validateSlackFileURL(good, testFileID, base); err != nil {
		t.Fatalf("valid url rejected: %v", err)
	}
	bad := []string{
		"https://evil.example/files-pri/T0TEAM1-" + testFileID + "/untitled",
		"http://files.slack.com/files-pri/T0TEAM1-" + testFileID + "/untitled",
		"https://files.slack.com.evil.example/files-pri/T0TEAM1-" + testFileID + "/untitled",
		"https://user:pw@files.slack.com/files-pri/T0TEAM1-" + testFileID + "/untitled",
		"https://files.slack.com/files-pri/T0TEAM1-FOTHER12345/untitled",
		"https://files.slack.com/files-pri/T0TEAM1-" + testFileID + "/untitled?x=1",
		"https://files.slack.com/files-pri/T0TEAM1-" + testFileID + "/../../x",
		"https://files.slack.com/files-pri/T0TEAM1-" + testFileID + "/a/b",
		"https://files.slack.com/files/U1/" + testFileID + "/untitled",
		"",
	}
	for _, raw := range bad {
		if _, err := validateSlackFileURL(raw, testFileID, base); err == nil {
			t.Errorf("url %q should be rejected", raw)
		}
	}
}

func TestIsTextMimetype(t *testing.T) {
	for _, m := range []string{"text/plain", "text/plain; charset=utf-8", "application/json", "text/x-python"} {
		if !isTextMimetype(m) {
			t.Errorf("%q should be allowed", m)
		}
	}
	for _, m := range []string{"", "image/png", "application/pdf", "application/octet-stream", "text/html", "text/html; charset=utf-8"} {
		if isTextMimetype(m) {
			t.Errorf("%q should be rejected", m)
		}
	}
}

type slackFileFixture struct {
	mimetype    string
	size        int64
	urlPrivate  func(base string) string
	fileBody    string
	fileStatus  int
	contentType string
	gotFileAuth bool
	infoFile    string
}

func (f *slackFileFixture) run(t *testing.T) (map[string]interface{}, error) {
	t.Helper()
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/files.info":
			f.infoFile = r.URL.Query().Get("file")
			w.Write([]byte(fmt.Sprintf(`{"ok":true,"file":{"id":%q,"name":"untitled","title":"Untitled","mimetype":%q,"filetype":"text","size":%d,"url_private":%q}}`,
				testFileID, f.mimetype, f.size, f.urlPrivate(upstream.URL))))
		case strings.HasPrefix(r.URL.Path, "/files-pri/"):
			f.gotFileAuth = true
			if f.contentType != "" {
				w.Header().Set("Content-Type", f.contentType)
			}
			if f.fileStatus != 0 {
				w.WriteHeader(f.fileStatus)
			}
			w.Write([]byte(f.fileBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	ws := &config.Workspace{Sites: map[string]config.Site{"slack": {Environments: map[string]config.Environment{
		"prod": {BaseURL: upstream.URL + "/api", Auth: config.AuthConfig{Type: "slack-session"}},
	}}}}
	op := &policy.Operation{ID: "slack.file_content", Connector: "http-rest", Site: "slack", SlackFileContent: true}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector(), slackFilesBase: upstream.URL}
	status, body, err := s.executeSlackFileContent(ws, op, map[string]interface{}{"file": testFileID}, "slacktest")
	if err != nil {
		return nil, err
	}
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func goodURL(base string) string { return base + "/files-pri/T0TEAM1-" + testFileID + "/untitled" }

func TestSlackFileContentReturnsText(t *testing.T) {
	f := &slackFileFixture{mimetype: "text/plain", size: 11, urlPrivate: goodURL, fileBody: "hello\nworld", contentType: "text/plain"}
	out, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if out["content"] != "hello\nworld" || out["ok"] != true {
		t.Fatalf("unexpected result: %v", out)
	}
	if f.infoFile != testFileID || !f.gotFileAuth {
		t.Fatalf("files.info file=%q, file fetched=%v", f.infoFile, f.gotFileAuth)
	}
}

func TestSlackFileContentRefusals(t *testing.T) {
	cases := map[string]*slackFileFixture{
		"image mimetype":     {mimetype: "image/png", size: 10, urlPrivate: goodURL, fileBody: "x"},
		"metadata too large": {mimetype: "text/plain", size: slackFileMaxBytes + 1, urlPrivate: goodURL, fileBody: "x"},
		"foreign host":       {mimetype: "text/plain", size: 1, urlPrivate: func(string) string { return "https://evil.example/files-pri/T1-" + testFileID + "/x" }, fileBody: "x"},
		"other file path":    {mimetype: "text/plain", size: 1, urlPrivate: func(b string) string { return b + "/files-pri/T1-FOTHER00000/x" }, fileBody: "x"},
		"download not 200":   {mimetype: "text/plain", size: 1, urlPrivate: goodURL, fileBody: "denied", fileStatus: 403},
		"html login page":    {mimetype: "text/plain", size: 1, urlPrivate: goodURL, fileBody: "<html>", contentType: "text/html"},
		"body over cap":      {mimetype: "text/plain", size: 1, urlPrivate: goodURL, fileBody: strings.Repeat("a", slackFileMaxBytes+1), contentType: "text/plain"},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := f.run(t); err == nil {
				t.Fatal("expected an error")
			}
			if name == "foreign host" && f.gotFileAuth {
				t.Fatal("credentials were sent to the file download for a foreign host")
			}
		})
	}
}

func TestSlackFileContentAtCapIsAllowed(t *testing.T) {
	f := &slackFileFixture{mimetype: "text/plain", size: slackFileMaxBytes, urlPrivate: goodURL, fileBody: strings.Repeat("a", slackFileMaxBytes), contentType: "text/plain"}
	out, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out["content"].(string)); got != slackFileMaxBytes {
		t.Fatalf("content length = %d", got)
	}
}
