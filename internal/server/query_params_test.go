package server

import (
	"encoding/json"
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

func TestBodyTemplateParamsAreNotCopiedToQuery(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.Query()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer upstream.Close()

	ws := &config.Workspace{Sites: map[string]config.Site{"gitlab": {Environments: map[string]config.Environment{
		"prod": {BaseURL: upstream.URL},
	}}}}
	op := &policy.Operation{
		ID:           "gitlab.mr_note",
		Connector:    "http-rest",
		Site:         "gitlab",
		Method:       "POST",
		Path:         "/projects/{project_id|urlencode}/merge_requests/{mr_iid}/discussions/{discussion_id|urlencode}/notes",
		BodyTemplate: `{"body": "{{body}}"}`,
	}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}
	note := "Done <b>\"quoted\"</b>\nsecond line"
	params := map[string]interface{}{
		"env":           "prod",
		"project_id":    "group/project",
		"mr_iid":        float64(3227),
		"discussion_id": "abc123",
		"body":          note,
		"per_page":      float64(20),
	}

	status, body, err := s.executeHttpConnector(ws, op, params, "querytest")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if want := "/projects/group%2Fproject/merge_requests/3227/discussions/abc123/notes"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if _, inQuery := gotQuery["body"]; inQuery {
		t.Errorf("body param was copied to the query: %v", gotQuery)
	}
	if got := gotQuery.Get("per_page"); got != "20" {
		t.Errorf("per_page query = %q, want %q (query = %v)", got, "20", gotQuery)
	}
	var sent struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("upstream body is not valid JSON: %v (%s)", err, gotBody)
	}
	if sent.Body != note {
		t.Errorf("body = %q, want %q", sent.Body, note)
	}
}

func TestQueryStringValuesAreNotJSONEscaped(t *testing.T) {
	var gotQuery url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	ws := &config.Workspace{Sites: map[string]config.Site{"corp": {Environments: map[string]config.Environment{
		"prod": {BaseURL: upstream.URL},
	}}}}
	op := &policy.Operation{ID: "corp.search", Connector: "http-rest", Site: "corp", Method: "GET", Path: "/rest/api/2/search"}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}
	jql := `status = "Assigned to DEV" AND summary ~ "a\b"`
	if _, _, err := s.executeHttpConnector(ws, op, map[string]interface{}{"jql": jql, "maxResults": float64(50)}, "querytest"); err != nil {
		t.Fatal(err)
	}
	if got := gotQuery.Get("jql"); got != jql {
		t.Errorf("jql = %q, want %q", got, jql)
	}
	if got := gotQuery.Get("maxResults"); got != "50" {
		t.Errorf("maxResults = %q, want 50", got)
	}
}
