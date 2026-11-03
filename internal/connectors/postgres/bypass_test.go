package postgres

import "testing"

// Allow-list bypass regressions: every "deny" case below used to pass because
// walkTables ignored all node types except FROM/JOIN/subquery/CTE structure.
func TestCheckTableScopeBypasses(t *testing.T) {
	allowed := []string{"allowed", "other"}
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		// --- bypasses that must be denied ---
		{"scalar subquery in target list", "SELECT (SELECT * FROM secret_tbl)", true},
		{"exists in where", "SELECT 1 FROM allowed a WHERE a.x = 1 AND EXISTS (SELECT 1 FROM secret_tbl)", true},
		{"query_to_xml", "SELECT * FROM query_to_xml('select * from secret_tbl', true, true, '')", true},
		{"query_to_xml qualified", "SELECT pg_catalog.query_to_xml('select 1', true, true, '')", true},
		{"table_to_xml", "SELECT table_to_xml('secret_tbl'::regclass, true, true, '')", true},
		{"cursor_to_xml", "SELECT cursor_to_xml('c'::refcursor, 1, true, true, '')", true},
		{"dblink", "SELECT * FROM dblink('host=x', 'select 1') AS t(a int)", true},
		{"pg_read_file", "SELECT pg_read_file('/etc/passwd')", true},
		{"pg_ls_dir", "SELECT pg_ls_dir('.')", true},
		{"lo_import", "SELECT lo_import('/etc/passwd')", true},
		{"pg_sleep", "SELECT pg_sleep(1)", true},
		{"pg_terminate_backend", "SELECT pg_terminate_backend(1)", true},
		{"set_config", "SELECT set_config('search_path', 'x', false)", true},
		{"denied func inside allowed table query", "SELECT pg_sleep(1) FROM allowed", true},
		{"subquery in function arg", "SELECT coalesce((SELECT x FROM secret_tbl LIMIT 1), 0)", true},
		{"subquery in case", "SELECT CASE WHEN true THEN (SELECT x FROM secret_tbl) ELSE 0 END", true},
		{"subquery in null test", "SELECT 1 FROM allowed WHERE (SELECT x FROM secret_tbl LIMIT 1) IS NULL", true},
		{"subquery in cast", "SELECT ((SELECT x FROM secret_tbl LIMIT 1))::text", true},
		{"subquery in order by", "SELECT 1 FROM allowed ORDER BY (SELECT x FROM secret_tbl LIMIT 1)", true},
		{"subquery in values", "SELECT * FROM (VALUES ((SELECT x FROM secret_tbl))) v", true},
		{"subquery in group by", "SELECT 1 FROM allowed GROUP BY (SELECT x FROM secret_tbl LIMIT 1)", true},
		{"subquery in limit", "SELECT 1 FROM allowed LIMIT (SELECT x FROM secret_tbl LIMIT 1)", true},
		{"subquery in join condition expr", "SELECT 1 FROM allowed a JOIN other o ON a.id = (SELECT id FROM secret_tbl LIMIT 1)", true},
		{"subquery in lateral func arg", "SELECT * FROM allowed a, LATERAL generate_series(1, (SELECT count(*) FROM secret_tbl)) g", true},
		{"subquery in window", "SELECT sum(x) OVER (ORDER BY (SELECT y FROM secret_tbl LIMIT 1)) FROM allowed", true},
		{"subquery in having", "SELECT x FROM allowed GROUP BY x HAVING count(*) > (SELECT count(*) FROM secret_tbl)", true},
		{"subquery in in-list", "SELECT 1 FROM allowed WHERE x IN (SELECT x FROM secret_tbl)", true},
		{"subquery in cte body expression", "WITH c AS (SELECT (SELECT x FROM secret_tbl) AS v) SELECT * FROM c", true},
		{"table via TABLE command", "TABLE secret_tbl", true},
		{"union branch", "SELECT 1 FROM allowed UNION SELECT 1 FROM secret_tbl", true},
		// CTE scoping
		{"cte name in one branch does not exempt other", "WITH secret_tbl AS (SELECT 1) SELECT * FROM secret_tbl UNION ALL SELECT * FROM (SELECT * FROM public.secret_tbl) z", true},
		{"cte defined in subquery does not leak out", "SELECT * FROM (WITH secret_tbl AS (SELECT 1) SELECT * FROM secret_tbl) a, secret_tbl", true},
		{"cte defined in set-op branch does not leak", "(WITH secret_tbl AS (SELECT 1) SELECT * FROM secret_tbl) UNION ALL SELECT * FROM secret_tbl", true},
		{"non-recursive cte self reference is a real table", "WITH secret_tbl AS (SELECT * FROM secret_tbl) SELECT * FROM secret_tbl", true},
		{"cte shadow in scalar subquery does not leak", "SELECT (WITH secret_tbl AS (SELECT 1) SELECT * FROM secret_tbl), (SELECT * FROM secret_tbl)", true},

		// --- legitimate queries that must still pass ---
		{"plain", "SELECT * FROM allowed", false},
		{"join", "SELECT a.x, o.y FROM allowed a JOIN other o ON a.id = o.id WHERE a.x = 1 AND o.y IS NOT NULL", false},
		{"left join + where subquery", "SELECT a.x FROM allowed a LEFT JOIN other o ON o.id = a.id WHERE a.id IN (SELECT id FROM other)", false},
		{"scalar subquery on allowed", "SELECT (SELECT count(*) FROM allowed)", false},
		{"exists on allowed", "SELECT 1 FROM allowed a WHERE EXISTS (SELECT 1 FROM other o WHERE o.id = a.id)", false},
		{"cte", "WITH c AS (SELECT * FROM allowed) SELECT * FROM c", false},
		{"two ctes chained", "WITH a AS (SELECT * FROM allowed), b AS (SELECT * FROM a) SELECT * FROM b JOIN other o ON o.id = b.id", false},
		{"recursive cte", "WITH RECURSIVE r AS (SELECT 1 AS n UNION ALL SELECT n + 1 FROM r WHERE n < 5) SELECT * FROM r", false},
		{"nested cte in subquery", "SELECT * FROM (WITH c AS (SELECT * FROM allowed) SELECT * FROM c) s", false},
		{"cte used in scalar subquery", "WITH c AS (SELECT 1 AS v) SELECT (SELECT v FROM c) FROM allowed", false},
		{"union of allowed", "SELECT x FROM allowed UNION ALL SELECT x FROM other", false},
		{"safe funcs", "SELECT count(*), max(x), coalesce(x, 0), upper(y), current_setting('search_path') FROM allowed GROUP BY y, x ORDER BY 1 LIMIT 10", false},
		{"generate_series", "SELECT * FROM generate_series(1, 3)", false},
		{"case + cast + window", "SELECT CASE WHEN x > 1 THEN 'a' ELSE 'b' END, x::text, row_number() OVER (ORDER BY x) FROM allowed", false},
		{"values", "SELECT * FROM (VALUES (1), (2)) v(n)", false},
		{"explain allowed", "EXPLAIN SELECT * FROM allowed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Connector{allowedTables: allowed}
			err := c.checkTableScope(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkTableScope(%q) err = %v, wantErr %v", tt.sql, err, tt.wantErr)
			}
		})
	}
}

func TestTableAllowedHelper(t *testing.T) {
	al := []string{"public.users", "reporting.*"}
	cases := []struct {
		schema, name string
		want         bool
	}{
		{"public", "users", true},
		{"", "users", true},
		{"public", "secrets", false},
		{"reporting", "anything", true},
		{"other", "users", false},
	}
	for _, c := range cases {
		if got := TableAllowed(al, c.schema, c.name); got != c.want {
			t.Errorf("TableAllowed(%q,%q) = %v, want %v", c.schema, c.name, got, c.want)
		}
	}
	if !TableAllowed(nil, "x", "y") {
		t.Error("empty allow-list must allow everything")
	}
}
