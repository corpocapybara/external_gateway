package postgres

import (
	"strings"
	"testing"
)

func TestValidateQueryAST_Select(t *testing.T) {
	tests := []struct {
		name  string
		query string
		ok    bool
	}{
		{"simple select", "SELECT 1", true},
		{"select with from", "SELECT * FROM users", true},
		{"select with where", "SELECT id FROM users WHERE name = 'bob'", true},
		{"select with join", "SELECT u.id, o.total FROM users u JOIN orders o ON u.id = o.user_id", true},
		{"with clause", "WITH cte AS (SELECT 1) SELECT * FROM cte", true},
		{"with recursive", "WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM t WHERE n < 10) SELECT * FROM t", true},
		{"empty", "", false},
		{"insert", "INSERT INTO users (name) VALUES ('bob')", false},
		{"update", "UPDATE users SET name = 'alice' WHERE id = 1", false},
		{"delete", "DELETE FROM users WHERE id = 1", false},
		{"drop table", "DROP TABLE users", false},
		{"create table", "CREATE TABLE test (id int)", false},
		{"alter table", "ALTER TABLE users ADD COLUMN email text", false},
		{"truncate", "TRUNCATE users", false},
		{"grant", "GRANT SELECT ON users TO bob", false},
		{"revoke", "REVOKE SELECT ON users FROM bob", false},
		{"vacuum", "VACUUM users", false},
		{"copy", "COPY users TO '/tmp/out'", false},
		{"multiple statements", "SELECT 1; DROP TABLE users", false},
		{"for update", "SELECT * FROM users FOR UPDATE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateQueryAST(tt.query)
			if tt.ok && err != nil {
				t.Errorf("expected ok, got: %v", err)
			}
			if !tt.ok && err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestQuoteLiteralNeutralizesInjection(t *testing.T) {
	malicious := "x' UNION SELECT usename, passwd, null, null FROM pg_shadow --"

	query := "SELECT column_name, data_type, is_nullable, column_default " +
		"FROM information_schema.columns WHERE table_name = " +
		QuoteLiteral(malicious) + " ORDER BY ordinal_position"

	if strings.Contains(query, "x' UNION") {
		t.Fatalf("injection not neutralized (literal terminated early): %s", query)
	}
	if !strings.Contains(query, "x'' UNION") {
		t.Fatalf("expected doubled single quote in escaped literal: %s", query)
	}

	if err := validateQueryAST(query); err != nil {
		t.Fatalf("escaped query should validate, got: %v", err)
	}
}

func TestQuoteLiteralPlain(t *testing.T) {
	if got := QuoteLiteral("orders"); got != "'orders'" {
		t.Errorf("QuoteLiteral(orders) = %q, want 'orders'", got)
	}
}
