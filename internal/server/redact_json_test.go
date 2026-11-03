package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/secrets"
	"github.com/rs/zerolog"
)

func TestWriteRedactedJSONEscapedSecrets(t *testing.T) {
	secrets.GetTaintRegistry().Taint([]byte("q\"u<o>te&secret-é\n"))
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writeRedactedJSON(rec, map[string]interface{}{"echo": "q\"u<o>te&secret-é\n", "x": "hello"})
	body := rec.Body.String()
	if strings.Contains(body, "secret") || strings.Contains(body, "u003c") {
		t.Fatalf("secret leaked: %s", body)
	}
	if !strings.Contains(body, "hello") {
		t.Fatalf("unrelated content lost: %s", body)
	}
}

func TestWriteRedactedJSONTrailingNewlineSecret(t *testing.T) {
	secrets.GetTaintRegistry().Taint([]byte("trailing-newline-secret\n"))
	s := &Server{}
	rec := httptest.NewRecorder()
	// Header code sends the trimmed value, so an upstream echoes it without the newline.
	s.writeRedactedJSON(rec, map[string]interface{}{"echo": "Bearer trailing-newline-secret"})
	if strings.Contains(rec.Body.String(), "trailing-newline-secret") {
		t.Fatalf("leaked: %s", rec.Body.String())
	}
}

// A secret that cannot be resolved must fail the request, not send it unauthenticated.
func TestWorklogClientFailsWhenSecretUnresolvable(t *testing.T) {
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}
	site := config.Site{DefaultEnv: "prod", Environments: map[string]config.Environment{
		"prod": {BaseURL: "http://jira.invalid", SecretRef: "secret://other-ws/jira", User: "u", Auth: config.AuthConfig{Type: "basic"}},
	}}
	// workspace "thp" asking for another tenant's secret is refused by the resolver.
	c, err := s.newRestClient("jira", "jira", site, nil, "thp")
	if err == nil {
		t.Fatalf("expected error, got client with headers %v", c.headers)
	}
}
