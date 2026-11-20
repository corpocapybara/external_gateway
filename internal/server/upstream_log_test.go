package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/rs/zerolog"
)

// TestUpstreamRequestLogOmitsBody: the "upstream request" line logs the body's
// size, never its content (EG0055, review R10), even at debug level.
func TestUpstreamRequestLogOmitsBody(t *testing.T) {
	const marker = "agent-comment-marker-7f3a"
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		b.ReadFrom(r.Body)
		gotBody = b.String()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	ws := &config.Workspace{Sites: map[string]config.Site{"corp": {Environments: map[string]config.Environment{
		"prod": {BaseURL: upstream.URL},
	}}}}
	op := &policy.Operation{
		ID:           "corp.comment",
		Connector:    "http-rest",
		Site:         "corp",
		Method:       "POST",
		Path:         "/comment",
		BodyTemplate: `{"text":"{{text}}"}`,
	}
	var logBuf bytes.Buffer
	s := &Server{logger: zerolog.New(&logBuf).Level(zerolog.DebugLevel), httpClient: httprest.NewConnector()}
	if _, _, err := s.executeHttpConnector(ws, op, map[string]interface{}{"text": marker}, "logtest"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, marker) {
		t.Fatalf("upstream body %q lacks the marker: the test sent nothing to log", gotBody)
	}
	if strings.Contains(logBuf.String(), marker) {
		t.Fatalf("log carries the request body: %s", logBuf.String())
	}
	var line map[string]interface{}
	for _, l := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
		var m map[string]interface{}
		if json.Unmarshal([]byte(l), &m) == nil && m["message"] == "upstream request" {
			line = m
		}
	}
	if line == nil {
		t.Fatalf("no upstream request line in log: %s", logBuf.String())
	}
	if _, ok := line["body"]; ok {
		t.Errorf("upstream request line has a body field: %v", line)
	}
	if got, want := line["body_bytes"], float64(len(gotBody)); got != want {
		t.Errorf("body_bytes = %v, want %v", got, want)
	}
}

// TestCapabilitiesFeaturesLacksScheduler: the scheduler stub is gone, so
// /capabilities no longer reports it (EG0055, review R9).
func TestCapabilitiesFeaturesLacksScheduler(t *testing.T) {
	s := accessServer(t)
	w := get(s, "/capabilities?workspace=acme", "alice-tok")
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body)
	}
	var out struct {
		Features map[string]string `json:"features"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Features) == 0 {
		t.Fatalf("no features map in %s", w.Body)
	}
	if v, ok := out.Features["clockify_scheduler"]; ok {
		t.Errorf("features still has clockify_scheduler=%q", v)
	}
}
