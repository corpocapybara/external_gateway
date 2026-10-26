package postgres
import "testing"
func TestZZ(t *testing.T){
 for _,q:=range []string{
 "EXPLAIN ANALYZE SELECT 1 INTO newt",
 "SELECT 1 INTO newt",
 "EXPLAIN ANALYZE WITH d AS (DELETE FROM public.x RETURNING 1) SELECT * FROM d",
 "EXPLAIN ANALYZE SELECT pg_terminate_backend(1)",
 "EXPLAIN (ANALYZE) SELECT * FROM pg_secret",
 }{ t.Log(q, "=>", validateQueryAST(q)) }
 c:=&Connector{allowedTables:[]string{"pg_catalog.*"}}
 t.Log("scope pg_secret:", c.checkTableScope("SELECT * FROM pg_secret"))
 t.Log("scope public.pg_secret:", c.checkTableScope("SELECT * FROM public.pg_secret"))
}
