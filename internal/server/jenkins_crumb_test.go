package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/rs/zerolog"
)

type fakeSecretStore map[string][]byte

func (f fakeSecretStore) Resolve(workspace, name string) ([]byte, error) {
	if v, ok := f[workspace+"/"+name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("secret %s/%s not found", workspace, name)
}

func (f fakeSecretStore) Set(workspace, name string, value []byte) error { return nil }

func (f fakeSecretStore) Delete(workspace, name string) error { return nil }

func TestJenkinsPostSendsCrumbWithItsSessionCookie(t *testing.T) {
	jenkins := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/crumbIssuer/api/json":
			http.SetCookie(w, &http.Cookie{Name: "JSESSIONID.node0", Value: "session-1", Path: "/", HttpOnly: true})
			w.Write([]byte(`{"crumb":"crumb-1","crumbRequestField":"Jenkins-Crumb"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/job/MR-1/buildWithParameters":
			session, err := r.Cookie("JSESSIONID.node0")
			if err != nil || session.Value != "session-1" || r.Header.Get("Jenkins-Crumb") != "crumb-1" {
				http.Error(w, "No valid crumb was included in the request", http.StatusForbidden)
				return
			}
			if r.URL.Query().Get("PUBLISH") != "false" {
				http.Error(w, "PUBLISH missing", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jenkins.Close()

	secrets.SetStore(fakeSecretStore{"crumbtest/jenkins": []byte("password")})
	ws := &config.Workspace{Sites: map[string]config.Site{"jenkins": {Environments: map[string]config.Environment{
		"dev": {BaseURL: jenkins.URL, Auth: config.AuthConfig{Type: "basic"}, User: "bot", SecretRef: "secret://crumbtest/jenkins"},
	}}}}
	op := &policy.Operation{ID: "jenkins.build_with_params", Connector: "http-rest", Site: "jenkins", Method: "POST", Path: "/{job}/buildWithParameters"}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}

	status, body, err := s.executeHttpConnector(ws, op, map[string]interface{}{"env": "dev", "job": "job/MR-1", "PUBLISH": "false"}, "crumbtest")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", status, body)
	}
}
