package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression test for the jira.status masking bug: allow_fields must NOT strip
// error responses, or a rejected write (HTTP 400) becomes indistinguishable from
// a success. Success responses are still shaped to the allow-list.
func TestShapeResponse(t *testing.T) {
	allow := []string{"id", "status"}
	jiraError := []byte(`{"errorMessages":["Transition id '99999' is not valid"],"errors":{}}`)

	t.Run("error body (>=400) passes through unshaped", func(t *testing.T) {
		got := shapeResponse(400, jiraError, allow)
		if string(got) != string(jiraError) {
			t.Fatalf("400 body was altered:\n got:  %s\n want: %s", got, jiraError)
		}
		if !strings.Contains(string(got), "not valid") {
			t.Fatalf("upstream error detail was stripped: %s", got)
		}
	})

	t.Run("success body (2xx) is shaped to allow-list", func(t *testing.T) {
		body := []byte(`{"id":"10001","status":"ok","secret":"leak"}`)
		got := shapeResponse(200, body, allow)
		var m map[string]interface{}
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("shaped body not JSON: %v", err)
		}
		if _, ok := m["secret"]; ok {
			t.Fatalf("non-allowed field survived shaping: %s", got)
		}
		if m["id"] != "10001" {
			t.Fatalf("allowed field missing/wrong: %s", got)
		}
	})

	t.Run("empty 204 body passes through", func(t *testing.T) {
		if got := shapeResponse(204, []byte(""), allow); string(got) != "" {
			t.Fatalf("empty body altered: %q", got)
		}
	})

	t.Run("top-level array is shaped per element", func(t *testing.T) {
		body := []byte(`[{"id":1,"status":"a","secret":"x"},{"id":2,"secret":"y"}]`)
		got := shapeResponse(200, body, allow)
		var arr []map[string]interface{}
		if err := json.Unmarshal(got, &arr); err != nil {
			t.Fatalf("shaped body not a JSON array: %v (%s)", err, got)
		}
		if len(arr) != 2 || arr[1]["id"] != float64(2) {
			t.Fatalf("elements lost or wrong: %s", got)
		}
		for _, m := range arr {
			if _, ok := m["secret"]; ok {
				t.Fatalf("non-allowed field survived shaping: %s", got)
			}
		}
	})

	t.Run("empty and null projected fields stay present", func(t *testing.T) {
		jiraAllow := []string{"fields.labels", "fields.fixVersions[].name", "fields.customfield_10340.value"}
		cases := []struct{ name, body, want string }{
			{"empty array and null object",
				`{"fields":{"labels":[],"fixVersions":[],"customfield_10340":null,"other":1}}`,
				`{"fields":{"customfield_10340":null,"fixVersions":[],"labels":[]}}`},
			{"null array",
				`{"fields":{"fixVersions":null}}`,
				`{"fields":{"fixVersions":null}}`},
			{"non-empty values unchanged",
				`{"fields":{"fixVersions":[{"id":"1","name":"v1"}],"customfield_10340":{"id":"9","value":"svc"}}}`,
				`{"fields":{"customfield_10340":{"value":"svc"},"fixVersions":[{"name":"v1"}]}}`},
			{"absent upstream key stays absent",
				`{"fields":{"labels":["a"]}}`,
				`{"fields":{"labels":["a"]}}`},
		}
		for _, c := range cases {
			if got := string(shapeResponse(200, []byte(c.body), jiraAllow)); got != c.want {
				t.Fatalf("%s:\n got:  %s\n want: %s", c.name, got, c.want)
			}
		}
	})

	t.Run("no allow_fields is a no-op", func(t *testing.T) {
		body := []byte(`{"a":1,"b":2}`)
		if got := shapeResponse(200, body, nil); string(got) != string(body) {
			t.Fatalf("body altered with no allow_fields: %s", got)
		}
	})
}
