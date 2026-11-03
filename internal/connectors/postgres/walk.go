package postgres

import (
	"fmt"
	"reflect"
	"strings"

	pgparsernodes "github.com/pgplex/pgparser/nodes"
)

// deniedFuncPrefixes lists functions (matched on the unqualified, lower-cased
// name) that execute SQL text, read server files, or otherwise reach outside the
// SELECT's visible table references, so they could dodge the table allow-list.
var deniedFuncPrefixes = []string{
	"query_to_xml", "table_to_xml", "schema_to_xml", "database_to_xml", "cursor_to_xml",
	"dblink", "pg_read_", "pg_ls_", "pg_stat_file", "pg_file_", "lo_",
	"pg_terminate_backend", "pg_cancel_backend", "pg_sleep", "set_config",
	"ts_stat", "pg_reload_conf", "pg_rotate_logfile", "pg_switch_wal", "pg_notify",
	"pg_advisory_", "pg_logical_", "pg_create_", "pg_drop_", "pg_replication_",
	"pg_export_snapshot", "nextval", "setval", "currval", "pg_backup_",
	"pg_promote", "pg_wal_replay_", "pg_import_system_collations",
}

func funcDenied(name string) bool {
	name = strings.ToLower(name)
	for _, p := range deniedFuncPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

const maxWalkDepth = 500

// tableWalker collects every table reference in a parsed statement and records
// the first policy violation (denied function, excessive depth). It descends
// generically (reflection) through every node field, so table references hidden
// in scalar subqueries, function arguments, CASE/COALESCE, casts, ORDER BY,
// VALUES lists etc. are all reached; only the node types that need special
// handling (CTE scoping, RangeVar, FuncCall) are matched explicitly.
type tableWalker struct {
	refs []tableRef
	err  error
}

// walkTables appends every table referenced under n to out. It returns an error
// for a denied function or a tree too deep to verify (fail closed).
func walkTables(n pgparsernodes.Node, cte map[string]bool, out *[]tableRef) error {
	w := &tableWalker{}
	w.walkValue(reflect.ValueOf(n), cte, 0)
	*out = append(*out, w.refs...)
	return w.err
}

func (w *tableWalker) fail(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *tableWalker) walkValue(v reflect.Value, cte map[string]bool, depth int) {
	if w.err != nil || !v.IsValid() {
		return
	}
	if depth > maxWalkDepth {
		w.fail(fmt.Errorf("query is nested too deeply to verify against pg_allowed_tables"))
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		w.walkValue(v.Elem(), cte, depth+1)
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		if n, ok := v.Interface().(pgparsernodes.Node); ok {
			w.walkNode(n, v, cte, depth)
			return
		}
		w.walkValue(v.Elem(), cte, depth+1)
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			w.walkValue(v.Index(i), cte, depth+1)
		}
	case reflect.Struct:
		w.walkFields(v, cte, depth, "")
	}
}

func (w *tableWalker) walkFields(v reflect.Value, cte map[string]bool, depth int, skip string) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" || f.Name == skip {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Array, reflect.Struct:
			w.walkValue(v.Field(i), cte, depth+1)
		}
	}
}

func (w *tableWalker) walkNode(n pgparsernodes.Node, v reflect.Value, cte map[string]bool, depth int) {
	switch x := n.(type) {
	case *pgparsernodes.RangeVar:
		name := strings.ToLower(x.Relname)
		if x.Schemaname == "" && cte[name] {
			return // reference to a CTE in scope, not a real table
		}
		w.refs = append(w.refs, tableRef{schema: strings.ToLower(x.Schemaname), name: name})
	case *pgparsernodes.FuncCall:
		if x.Funcname != nil && len(x.Funcname.Items) > 0 {
			if s, ok := x.Funcname.Items[len(x.Funcname.Items)-1].(*pgparsernodes.String); ok && funcDenied(s.Str) {
				w.fail(fmt.Errorf("function %q is not permitted when pg_allowed_tables is set", s.Str))
				return
			}
		}
		w.walkFields(v.Elem(), cte, depth, "")
	case *pgparsernodes.SelectStmt:
		w.walkSelect(x, v, cte, depth)
	default:
		w.walkFields(v.Elem(), cte, depth, "")
	}
}

// walkSelect scopes CTE names to the SELECT that defines them: they are visible
// in that SELECT's body and in sibling CTE queries (later ones only, or all under
// RECURSIVE), never in an unrelated branch of the statement.
func (w *tableWalker) walkSelect(s *pgparsernodes.SelectStmt, v reflect.Value, cte map[string]bool, depth int) {
	scope := cte
	if s.WithClause != nil && s.WithClause.Ctes != nil {
		scope = make(map[string]bool, len(cte)+len(s.WithClause.Ctes.Items))
		for k := range cte {
			scope[k] = true
		}
		var defs []*pgparsernodes.CommonTableExpr
		for _, it := range s.WithClause.Ctes.Items {
			if c, ok := it.(*pgparsernodes.CommonTableExpr); ok {
				defs = append(defs, c)
			}
		}
		if s.WithClause.Recursive {
			for _, c := range defs {
				scope[strings.ToLower(c.Ctename)] = true
			}
			for _, c := range defs {
				w.walkValue(reflect.ValueOf(c.Ctequery), scope, depth+1)
			}
		} else {
			for _, c := range defs {
				// Non-recursive: a CTE's own query sees only earlier CTEs.
				w.walkValue(reflect.ValueOf(c.Ctequery), scope, depth+1)
				scope[strings.ToLower(c.Ctename)] = true
			}
		}
	}
	w.walkFields(v.Elem(), scope, depth, "WithClause")
}
