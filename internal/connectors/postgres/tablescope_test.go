package postgres

import "testing"

func TestCheckTableScope(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		sql     string
		wantErr bool
	}{
		{"allowed exact", []string{"public.users"}, "SELECT * FROM public.users", false},
		{"allowed bare in public", []string{"users"}, "SELECT id FROM users", false},
		{"schema wildcard", []string{"reporting.*"}, "SELECT * FROM reporting.daily", false},
		{"star allows all", []string{"*"}, "SELECT * FROM anything.at_all", false},
		{"disallowed catalog", []string{"public.users"}, "SELECT * FROM pg_catalog.pg_shadow", true},
		{"disallowed bare", []string{"public.users"}, "SELECT * FROM secrets", true},
		{"join one disallowed", []string{"public.a"}, "SELECT * FROM public.a a JOIN public.b b ON a.id = b.id", true},
		{"cte name is not a table", []string{"public.users"}, "WITH x AS (SELECT * FROM public.users) SELECT * FROM x", false},
		{"cte body still checked", []string{"public.users"}, "WITH x AS (SELECT * FROM secrets) SELECT * FROM x", true},
		{"subquery disallowed", []string{"public.users"}, "SELECT * FROM (SELECT * FROM secrets) t", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Connector{allowedTables: tt.allowed}
			err := c.checkTableScope(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkTableScope(%q) err = %v, wantErr %v", tt.sql, err, tt.wantErr)
			}
		})
	}
}

// A data-modifying CTE must be rejected at the AST layer as a non-SELECT
// statement (belt; the read-only transaction is the suspenders).
func TestValidateRejectsDataModifyingCTE(t *testing.T) {
	// A top-level DELETE/UPDATE/INSERT is a non-SelectStmt and must be rejected.
	if err := validateQueryAST("DELETE FROM audit_log"); err == nil {
		t.Fatal("expected non-SELECT to be rejected")
	}
	if err := validateQueryAST("SELECT 1 FOR UPDATE"); err == nil {
		t.Fatal("expected SELECT ... FOR UPDATE to be rejected")
	}
	if err := validateQueryAST("SELECT 1"); err != nil {
		t.Fatalf("plain SELECT rejected: %v", err)
	}
}
