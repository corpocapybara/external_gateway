package server

import (
	"encoding/json"
	"testing"
)

func TestFormatParamValue(t *testing.T) {
	cases := []struct {
		name string
		val  interface{}
		want string
	}{
		{"small int as float64", float64(5), "5"},
		{"large int as float64 (no sci notation)", float64(12345678), "12345678"},
		{"very large int as float64", float64(1500000000), "1500000000"},
		{"negative int as float64", float64(-42), "-42"},
		{"non-integral float64", float64(3.14), "3.14"},
		{"float32 integral", float32(7), "7"},
		{"string passthrough", "hello", "hello"},
		{"bool passthrough", true, "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatParamValue(c.val); got != c.want {
				t.Errorf("formatParamValue(%v) = %q, want %q", c.val, got, c.want)
			}
		})
	}
}

// TestBuildBodyIntFieldFromJSON reproduces the original bug: a large integer
// arriving via JSON (and thus decoded as float64) must render as a plain
// integer in the body, not scientific notation.
func TestBuildBodyIntFieldFromJSON(t *testing.T) {
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(`{"issueId": 12345678}`), &params); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	body, err := s.buildBody(`{"id": {{issueId}}}`, params)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id": 12345678}`
	if string(body) != want {
		t.Errorf("buildBody = %q, want %q", string(body), want)
	}
	// The result must be valid JSON with an integer, not 1.2345678e+07.
	var out struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body is not valid JSON: %v (%s)", err, body)
	}
}

func TestBuildBodyDoesNotRescanValues(t *testing.T) {
	s := &Server{}
	params := map[string]interface{}{"a": "x{{", "b": "}}y", "c": "{{b}}"}
	body, err := s.buildBody(`{"a":"{{a}}","b":"{{b}}","c":"{{c}}","d":"{{missing}}"}`, params)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"x{{","b":"}}y","c":"{{b}}","d":""}`
	if string(body) != want {
		t.Errorf("buildBody = %s, want %s", body, want)
	}
}

func TestBuildBodyObjectParamRendersJSON(t *testing.T) {
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(`{"fields": {"summary": "x \"quoted\"", "customfield_1": {"id": "7"}}}`), &params); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	body, err := s.buildBody(`{"serviceDeskId":"1","requestFieldValues":{{fields}}}`, params)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ServiceDeskID string `json:"serviceDeskId"`
		Fields        struct {
			Summary string `json:"summary"`
			CF1     struct {
				ID string `json:"id"`
			} `json:"customfield_1"`
		} `json:"requestFieldValues"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body is not valid JSON: %v (%s)", err, body)
	}
	if out.ServiceDeskID != "1" || out.Fields.Summary != `x "quoted"` || out.Fields.CF1.ID != "7" {
		t.Errorf("unexpected body %s", body)
	}
}
