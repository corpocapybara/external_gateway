package server

import (
	"strings"
	"testing"

	"github.com/external_gateway/internal/policy"
)

func TestDescribeColumnsSQL(t *testing.T) {
	got := describeColumnsSQL("reporting", "orders")
	want := "SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = 'reporting' AND table_name = 'orders' ORDER BY ordinal_position"
	if got != want {
		t.Errorf("describeColumnsSQL = %q, want %q", got, want)
	}
	// Both values are quoted, so a quote cannot end the literal.
	inj := describeColumnsSQL("a'b", "c'd")
	if !strings.Contains(inj, "table_schema = 'a''b'") || !strings.Contains(inj, "table_name = 'c''d'") {
		t.Errorf("describeColumnsSQL did not quote both values: %q", inj)
	}
}

func TestPgDescribeSchema(t *testing.T) {
	const refused = `table "orders" is not permitted by pg_allowed_tables`
	cases := []struct {
		name        string
		allowed     []string
		schemaParam string
		wantSchema  string
		wantErr     string
	}{
		{"public.* refuses reporting", []string{"public.*"}, "reporting", "", refused},
		{"reporting.* allows reporting", []string{"reporting.*"}, "reporting", "reporting", ""},
		{"bare entry is public only", []string{"orders"}, "reporting", "", refused},
		{"omitted schema is db schema", []string{"orders"}, "", "public", ""},
		{"omitted schema refused by db schema", []string{"reporting.*"}, "", "", refused},
		{"empty allow-list allows any", nil, "reporting", "reporting", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pgDescribeSchema(c.allowed, "public", c.schemaParam, "orders")
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.wantSchema {
				t.Errorf("schema = %q, want %q", got, c.wantSchema)
			}
		})
	}
}

// TestExamplePostgresSchemaParam checks the example op's optional schema param:
// omitted is allowed, an empty or uppercase value is refused by the pattern.
func TestExamplePostgresSchemaParam(t *testing.T) {
	reg, err := policy.NewRegistry("../../config.example/operations.globex.yaml")
	if err != nil {
		t.Fatal(err)
	}
	op, ok := reg.Get("postgres.schema")
	if !ok {
		t.Fatal("no postgres.schema")
	}
	def := op.Params["schema"]
	if def.Type != "string" || def.Required || def.Default != nil {
		t.Fatalf("schema param = %+v, want optional string without default", def)
	}
	base := func(extra map[string]interface{}) map[string]interface{} {
		p := map[string]interface{}{"env": "local", "db": "app", "table": "orders"}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	for _, p := range []map[string]interface{}{base(nil), base(map[string]interface{}{"schema": "reporting"})} {
		if err := reg.ValidateParams(op, p, nil); err != nil {
			t.Errorf("ValidateParams(%v) = %v, want nil", p, err)
		}
	}
	for _, s := range []string{"", "Reporting", "a'b"} {
		err := reg.ValidateParams(op, base(map[string]interface{}{"schema": s}), nil)
		if err == nil || !strings.Contains(err.Error(), "must match") {
			t.Errorf("schema=%q: err = %v, want pattern refusal", s, err)
		}
	}
	if err := reg.ValidateParams(op, base(map[string]interface{}{"tbl": "x"}), nil); err == nil {
		t.Error("unknown param tbl accepted")
	}
}
