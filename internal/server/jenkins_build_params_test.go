package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// jenkins.build_with_params declares each *ContainerImageTag param of the acme
// regression job as an optional pattern-checked string with no default, and pins
// PUBLISH to "false" (EG0011). A chosen tag must reach Jenkins as a query param,
// an omitted tag must not be sent at all (Jenkins then leaves it empty: Active
// Choices defaults run only in the web form), and
// a malformed tag or PUBLISH=true must be refused before Jenkins is called.
func TestJenkinsBuildWithParamsSendsOnlyChosenImageTags(t *testing.T) {
	var mu sync.Mutex
	var builds []url.Values
	jenkins := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/crumbIssuer/api/json":
			w.Write([]byte(`{"crumb":"c","crumbRequestField":"Jenkins-Crumb"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/job/Regression/job/develop/buildWithParameters":
			mu.Lock()
			builds = append(builds, r.URL.Query())
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer jenkins.Close()

	const tagPattern = `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`
	f := newEnvFixture(t)
	f.sites["jenkins"] = config.Site{Environments: map[string]config.Environment{"dev": {BaseURL: jenkins.URL}}}
	f.ops["jenkins.build_with_params"] = &policy.Operation{ID: "jenkins.build_with_params", Connector: "http-rest", Site: "jenkins",
		Method: "POST", Path: "/{job}/buildWithParameters",
		Params: map[string]policy.ParamDef{
			"env":                      {Type: "string", Default: "dev"},
			"job":                      {Type: "string", Required: true, MultiSegment: true},
			"PUBLISH":                  {Type: "enum", Values: []string{"false"}, Default: "false"},
			"apiContainerImageTag":     {Type: "string", Pattern: tagPattern},
			"walletsContainerImageTag": {Type: "string", Pattern: tagPattern},
		}}
	job := "job/Regression/job/develop"

	code, out := f.call(t, "jenkins.build_with_params", map[string]interface{}{"job": job, "apiContainerImageTag": "9.21.0-ci0167"})
	if code/100 != 2 {
		t.Fatalf("one tag: status %d, body %v; want 2xx", code, out)
	}
	if len(builds) != 1 {
		t.Fatalf("Jenkins got %d builds, want 1", len(builds))
	}
	want := url.Values{"apiContainerImageTag": {"9.21.0-ci0167"}, "PUBLISH": {"false"}}
	if got := builds[0]; got.Encode() != want.Encode() {
		t.Fatalf("build query = %q, want %q (an omitted tag must not be sent)", got.Encode(), want.Encode())
	}

	refused := []map[string]interface{}{
		{"job": job, "apiContainerImageTag": "9.21.0 ci"},
		{"job": job, "apiContainerImageTag": "9.21/0"},
		{"job": job, "apiContainerImageTag": ""},
		{"job": job, "apiContainerImageTag": "-leading-dash"},
		// A tag the op does not declare (a new or misspelled Jenkinsfile param) is refused, not forwarded.
		{"job": job, "ApiContainerImageTag": "9.21.0"},
		{"job": job, "PUBLISH": "true"},
		{"job": job, "PUBLISH": true},
	}
	for _, params := range refused {
		code, out := f.call(t, "jenkins.build_with_params", params)
		if code != http.StatusForbidden || out["error"] != "denied" || out["rule"] != "validation" {
			t.Fatalf("%v: status %d, body %v; want 403 rule validation", params, code, out)
		}
	}
	if len(builds) != 1 {
		t.Fatalf("Jenkins got %d builds after refused calls, want still 1", len(builds))
	}
}
