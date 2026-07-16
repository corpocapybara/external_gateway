package redact

import (
	"encoding/json"
	"testing"
)

// Regression: `field[].sub` must project across ALL array elements (previously
// collapsed to the first), and must not leak fields outside allow_fields.
func TestShapeProjectsArrays(t *testing.T) {
	raw := `{
		"ok": true,
		"channels": [
			{"id":"C1","name":"one","is_private":false,"num_members":3,"secret":"x"},
			{"id":"C2","name":"two","is_private":true,"num_members":5,"secret":"y"},
			{"id":"C3","name":"three","is_private":false,"num_members":8,"secret":"z"}
		],
		"response_metadata": {"next_cursor":"abc"}
	}`
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}

	out := NewResponseShaper([]string{
		"ok",
		"channels[].id",
		"channels[].name",
		"channels[].is_private",
		"response_metadata.next_cursor",
	}).Shape(data)

	chans, ok := out["channels"].([]interface{})
	if !ok {
		t.Fatalf("channels not projected as array: %T", out["channels"])
	}
	if len(chans) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(chans))
	}
	c0 := chans[0].(map[string]interface{})
	if c0["id"] != "C1" || c0["name"] != "one" || c0["is_private"] != false {
		t.Fatalf("element 0 wrong: %v", c0)
	}
	c2 := chans[2].(map[string]interface{})
	if c2["id"] != "C3" || c2["name"] != "three" {
		t.Fatalf("element 2 wrong: %v", c2)
	}
	if _, leaked := c0["secret"]; leaked {
		t.Fatalf("non-allowed field leaked: %v", c0)
	}
	if _, leaked := c0["num_members"]; leaked {
		t.Fatalf("non-allowed field leaked: %v", c0)
	}
	if out["ok"] != true {
		t.Fatalf("scalar field 'ok' missing")
	}
	rm, ok := out["response_metadata"].(map[string]interface{})
	if !ok || rm["next_cursor"] != "abc" {
		t.Fatalf("nested field missing: %v", out["response_metadata"])
	}
}

// Elements missing an allowed field should still yield the fields they do have.
func TestShapeArraySparseFields(t *testing.T) {
	raw := `{"messages":[
		{"ts":"1","text":"a","reply_count":2},
		{"ts":"2","text":"b"}
	]}`
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	out := NewResponseShaper([]string{"messages[].ts", "messages[].text", "messages[].reply_count"}).Shape(data)
	msgs := out["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	m1 := msgs[1].(map[string]interface{})
	if m1["ts"] != "2" || m1["text"] != "b" {
		t.Fatalf("element 1 wrong: %v", m1)
	}
	if _, present := m1["reply_count"]; present {
		t.Fatalf("reply_count should be absent on element 1: %v", m1)
	}
}
