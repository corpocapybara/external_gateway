package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/rs/zerolog"
)

// End to end through executeHttpConnector: the request that reaches the
// upstream must never carry an injected path.
func TestExecuteHTTPPathInjectionEndToEnd(t *testing.T) {
	var gotURI string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		w.Write([]byte("{}"))
	}))
	defer up.Close()
	secrets.SetStore(fakeSecretStore{"pathtest/tok": []byte("x")})
	ws := &config.Workspace{Sites: map[string]config.Site{"s": {Environments: map[string]config.Environment{
		"dev": {BaseURL: up.URL, Auth: config.AuthConfig{Type: "bearer"}, SecretRef: "secret://pathtest/tok"},
	}}}}
	op := &policy.Operation{ID: "x", Connector: "http-rest", Site: "s", Method: "GET", Path: "/issue/{key}/other/{k2}"}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}
	run := func(p map[string]interface{}) error {
		gotURI = ""
		p["env"] = "dev"
		_, _, err := s.executeHttpConnector(ws, op, p, "pathtest")
		return err
	}
	for _, v := range []string{"%2e%2e%2f", "%2F", "a/b"} {
		if err := run(map[string]interface{}{"key": v, "k2": "z"}); err == nil || gotURI != "" {
			t.Errorf("value %q reached upstream as %q (err=%v)", v, gotURI, err)
		}
	}
	for i := 0; i < 30; i++ {
		if err := run(map[string]interface{}{"key": "{k2}", "k2": "SECRET"}); err != nil {
			t.Fatal(err)
		}
		if gotURI != "/issue/%7Bk2%7D/other/SECRET" && gotURI != "/issue/{k2}/other/SECRET" {
			t.Fatalf("value re-expanded: %q", gotURI)
		}
	}
}
