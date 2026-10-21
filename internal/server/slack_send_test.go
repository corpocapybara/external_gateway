package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

func TestSlackPostSendsParamsAsFormBody(t *testing.T) {
	var gotQuery url.Values
	var gotContentType string
	var gotForm url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(raw))
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	ws := &config.Workspace{Sites: map[string]config.Site{"slack": {Environments: map[string]config.Environment{
		"prod": {BaseURL: upstream.URL},
	}}}}
	op := &policy.Operation{
		ID:        "slack.send",
		Connector: "http-rest",
		Site:      "slack",
		Method:    "POST",
		Path:      "/chat.postMessage",
	}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}
	text := "Hi — \"quoted\" <https://example.com|link>\n• second line"
	params := map[string]interface{}{"env": "prod", "channel": "D0123", "text": text}

	if _, _, err := s.executeHttpConnector(ws, op, params, "slacktest"); err != nil {
		t.Fatal(err)
	}
	if len(gotQuery) != 0 {
		t.Errorf("params leaked into the query: %v", gotQuery)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("content type = %q", gotContentType)
	}
	if got := gotForm.Get("channel"); got != "D0123" {
		t.Errorf("channel = %q", got)
	}
	if got := gotForm.Get("text"); got != text {
		t.Errorf("text = %q, want %q", got, text)
	}
}
