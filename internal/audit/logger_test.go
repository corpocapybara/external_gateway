package audit

import (
	"reflect"
	"testing"

	"github.com/external_gateway/internal/secrets"
)

func TestSanitizeParamsNestedAndExtendedKeys(t *testing.T) {
	in := map[string]interface{}{
		"issue":         "THP-1",
		"token_count":   5,
		"Access_Token":  "a",
		"refresh_token": "b",
		"client_secret": "c",
		"X-Api-Key":     "d",
		"private_key":   "e",
		"body": map[string]interface{}{
			"user":     "bob",
			"Password": "p",
			"items": []interface{}{
				map[string]interface{}{"apiKey": "k", "name": "n"},
			},
		},
	}
	got := sanitizeParams(in)
	want := map[string]interface{}{
		"issue":         "THP-1",
		"token_count":   5,
		"Access_Token":  "***",
		"refresh_token": "***",
		"client_secret": "***",
		"X-Api-Key":     "***",
		"private_key":   "***",
		"body": map[string]interface{}{
			"user":     "bob",
			"Password": "***",
			"items": []interface{}{
				map[string]interface{}{"apiKey": "***", "name": "n"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	if in["body"].(map[string]interface{})["Password"] != "p" {
		t.Fatal("input mutated")
	}
}

func TestSanitizeParamsTypedContainersAndExactKeys(t *testing.T) {
	got := sanitizeParams(map[string]interface{}{
		"h":     map[string]string{"Authorization": "Bearer x", "Accept": "json"},
		"l":     []string{"a"},
		"pwd":   "x",
		"value": "y",
	})
	h := got["h"].(map[string]interface{})
	if h["Authorization"] != "***" || h["Accept"] != "json" || got["pwd"] != "***" || got["value"] != "***" {
		t.Fatalf("got %#v", got)
	}
	if sanitizeParams(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestSanitizeParamsRedactsTaintedValues(t *testing.T) {
	secrets.GetTaintRegistry().Taint([]byte("tainted-value-12345"))
	got := sanitizeParams(map[string]interface{}{
		"note": "see tainted-value-12345 here",
		"list": []interface{}{"x tainted-value-12345"},
	})
	if got["note"] != "see *** here" || got["list"].([]interface{})[0] != "x ***" {
		t.Fatalf("got %#v", got)
	}
}
