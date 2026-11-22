package server

import (
	"errors"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// exec shapes a shape entry's success body by allow_fields and leaves errors,
// status >= 400, non-JSON bodies and non-shape entries alone (EG0051).
func TestDispatchShapesBody(t *testing.T) {
	pgBody := `{"columns":["id","secret"],"rows":[{"id":1,"secret":"x"}],"count":1}`
	stub := func(status int, body string, err error) func(*Server, *config.Workspace, *policy.Operation, map[string]interface{}, string) (int, []byte, error) {
		return func(*Server, *config.Workspace, *policy.Operation, map[string]interface{}, string) (int, []byte, error) {
			return status, []byte(body), err
		}
	}
	allow := []string{"rows[].id", "count"}
	boom := errors.New("query failed")
	cases := []struct {
		name    string
		shape   bool
		status  int
		body    string
		err     error
		allow   []string
		want    string
		wantErr bool
	}{
		{"postgres rows shaped", true, 200, pgBody, nil, allow, `{"count":1,"rows":[{"id":1}]}`, false},
		{"no allow_fields", true, 200, pgBody, nil, nil, pgBody, false},
		{"status 500 unshaped", true, 500, pgBody, nil, allow, pgBody, false},
		{"error unshaped", true, 200, pgBody, boom, allow, pgBody, true},
		{"non-JSON unshaped", true, 200, "plain text", nil, allow, "plain text", false},
		{"shape false", false, 200, pgBody, nil, allow, pgBody, false},
	}
	for _, c := range cases {
		d := connectorDispatch{shape: c.shape, run: stub(c.status, c.body, c.err)}
		op := &policy.Operation{ID: "pg.query", Response: policy.ResponsePolicy{AllowFields: c.allow}}
		status, body, err := d.exec(&Server{}, nil, op, nil, "w")
		if status != c.status || string(body) != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: got (%d, %s, %v), want (%d, %s, err=%v)", c.name, status, body, err, c.status, c.want, c.wantErr)
		}
		if c.wantErr && !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want the connector's error", c.name, err)
		}
	}
}

// Every connector shapes exactly once: in dispatch (shape) or inside itself.
func TestConnectorDispatchShapeFlags(t *testing.T) {
	selfShaping := map[string]bool{"http-rest": true, "form-session": true, "email": true}
	for _, name := range policy.KnownConnectors() {
		d, ok := connectorDispatches[name]
		if !ok {
			t.Errorf("%s: no dispatch entry", name)
			continue
		}
		if d.shape == selfShaping[name] {
			t.Errorf("%s: shape = %v, self-shaping = %v: exactly one of them must hold", name, d.shape, selfShaping[name])
		}
	}
}
