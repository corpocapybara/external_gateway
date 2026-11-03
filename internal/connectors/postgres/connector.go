package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lib/pq"
	pgparserparser "github.com/pgplex/pgparser/parser"
	pgparsernodes "github.com/pgplex/pgparser/nodes"
)

// QuoteLiteral escapes a string for safe inclusion as a SQL string literal
// (returns the value wrapped in single quotes with internal quotes doubled).
// Use this for any agent-supplied value that must be interpolated into a query
// string, since ExecuteQuery takes a raw statement and cannot bind parameters
// (the read-only wrapper uses multi-statement simple-protocol execution).
func QuoteLiteral(literal string) string {
	return pq.QuoteLiteral(literal)
}

type Connector struct {
	readOnly      bool
	db            *sql.DB
	connStr       string
	allowedTables []string
}

// SetAllowedTables restricts which tables a SELECT may read (pg_allowed_tables).
// Entries may be "*" (all), "schema.*", "schema.table", or a bare "table" name.
// An empty list means no table restriction.
func (c *Connector) SetAllowedTables(tables []string) {
	c.allowedTables = tables
}

// TableAllowed reports whether schema.name may be read under the given
// pg_allowed_tables list (always true when the list is empty). An empty schema
// means "public".
func TableAllowed(allowed []string, schema, name string) bool {
	if len(allowed) == 0 {
		return true
	}
	if schema == "" {
		schema = "public"
	}
	return tableAllowed(tableRef{schema: strings.ToLower(schema), name: strings.ToLower(name)}, allowed)
}

type ColumnDef struct {
	Name string
	Type string
}

func NewConnector() *Connector {
	return &Connector{
		readOnly: true,
	}
}

func (c *Connector) Name() string {
	return "postgres"
}

// Connect opens the connection. sslmode is a libpq sslmode value; empty means
// "prefer" (TLS when the server offers it, plaintext otherwise).
func (c *Connector) Connect(host string, port int, user, password, dbname, schema, sslmode string) error {
	connStr, err := buildConnStr(host, port, user, password, dbname, schema, sslmode)
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("opening connection: %w", err)
	}
	return c.finishConnect(db, connStr)
}

var validSSLModes = map[string]bool{"disable": true, "allow": true, "prefer": true, "require": true, "verify-ca": true, "verify-full": true}

func buildConnStr(host string, port int, user, password, dbname, schema, sslmode string) (string, error) {
	if sslmode == "" {
		sslmode = "prefer"
	}
	if !validSSLModes[sslmode] {
		return "", fmt.Errorf("invalid sslmode %q", sslmode)
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   dbname,
	}
	q := u.Query()
	q.Set("sslmode", sslmode)
	q.Set("connect_timeout", "10")
	if schema != "" {
		q.Set("search_path", schema)
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// FilterTableRows keeps only rows whose "table_name" is readable under the
// pg_allowed_tables list (fail closed: a missing/non-string name is dropped).
func FilterTableRows(res *QueryResult, allowed []string, schema string) {
	if len(allowed) == 0 {
		return
	}
	kept := make([]map[string]interface{}, 0, len(res.Rows))
	for _, row := range res.Rows {
		if name, ok := row["table_name"].(string); ok && TableAllowed(allowed, schema, name) {
			kept = append(kept, row)
		}
	}
	res.Rows = kept
	res.Count = len(kept)
}

func (c *Connector) finishConnect(db *sql.DB, connStr string) error {

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return fmt.Errorf("connecting: %w", err)
	}

	if c.db != nil {
		c.db.Close()
	}
	c.db = db
	c.connStr = connStr
	return nil
}

func (c *Connector) Close() {
	if c.db != nil {
		c.db.Close()
	}
}

func (c *Connector) ExecuteQuery(ctx context.Context, query string) (*QueryResult, error) {
	if c.db == nil {
		return nil, fmt.Errorf("not connected")
	}

	sanitized := strings.TrimSpace(query)
	if err := validateQueryAST(sanitized); err != nil {
		return nil, err
	}
	if len(c.allowedTables) > 0 {
		if err := c.checkTableScope(sanitized); err != nil {
			return nil, err
		}
	}

	// Run inside a genuine READ ONLY transaction. lib/pq issues `BEGIN ... READ
	// ONLY` for TxOptions.ReadOnly, so Postgres itself rejects any write — including
	// a data-modifying CTE (WITH x AS (DELETE ...) SELECT ...) that the statement-
	// level AST check does not descend into. We never commit (defer Rollback).
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("beginning read-only transaction: %w", err)
	}
	defer tx.Rollback()

	// Bound server-side execution time. SET LOCAL takes effect here because we are
	// now inside a real transaction block (the previous mid-batch BEGIN did not).
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = 5000"); err != nil {
		return nil, fmt.Errorf("setting statement timeout: %w", err)
	}

	rows, err := tx.QueryContext(ctx, sanitized)
	if err != nil {
		return nil, fmt.Errorf("query execution: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("reading columns: %w", err)
	}

	var resultRows []map[string]interface{}
	count := 0
	for rows.Next() && count < 1000 {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		resultRows = append(resultRows, row)
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading rows: %w", err)
	}

	return &QueryResult{
		Columns: columns,
		Rows:    resultRows,
		Count:   len(resultRows),
	}, nil
}

type QueryResult struct {
	Columns []string
	Rows    []map[string]interface{}
	Count   int
}

func validateQueryAST(query string) error {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return fmt.Errorf("empty query")
	}

	stmts, err := pgparserparser.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("sql parse error: %w", err)
	}
	if stmts == nil || stmts.Len() == 0 {
		return fmt.Errorf("no valid SQL statement found")
	}

	for i, item := range stmts.Items {
		if explain, ok := item.(*pgparsernodes.ExplainStmt); ok {
			item = explain.Query
		}
		switch s := item.(type) {
		case *pgparsernodes.SelectStmt:
			if s.LockingClause != nil && s.LockingClause.Len() > 0 {
				return fmt.Errorf("SELECT FOR UPDATE/SHARE is not allowed")
			}
		case nil:
			return fmt.Errorf("only SELECT queries allowed (got empty EXPLAIN at stmt %d)", i+1)
		default:
			tag := item.Tag()
			name := pgparsernodes.NodeTagName(tag)
			if name == "" {
				name = fmt.Sprintf("Tag%d", tag)
			}
			return fmt.Errorf("only SELECT queries allowed (got %s at stmt %d)", name, i+1)
		}
	}
	return nil
}

type tableRef struct {
	schema string
	name   string
}

// checkTableScope enforces pg_allowed_tables. It walks every node of the parsed
// statement (generic descent, so scalar subqueries, function arguments, CASE,
// casts etc. are covered), rejects any table not covered by the allow-list and
// rejects functions that execute SQL text or reach the server (deniedFuncPrefixes).
// The read-only transaction remains the hard write barrier.
func (c *Connector) checkTableScope(query string) error {
	stmts, err := pgparserparser.Parse(strings.TrimSpace(query))
	if err != nil {
		return fmt.Errorf("sql parse error: %w", err)
	}
	var refs []tableRef
	for _, item := range stmts.Items {
		if err := walkTables(item, map[string]bool{}, &refs); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		if !tableAllowed(ref, c.allowedTables) {
			full := ref.name
			if ref.schema != "" {
				full = ref.schema + "." + ref.name
			}
			return fmt.Errorf("table %q is not permitted by pg_allowed_tables", full)
		}
	}
	return nil
}

func tableAllowed(ref tableRef, allowed []string) bool {
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case a == "":
			continue
		case a == "*" || a == "*.*":
			return true
		case strings.HasSuffix(a, ".*"):
			if ref.schema == strings.TrimSuffix(a, ".*") {
				return true
			}
		case strings.Contains(a, "."):
			if a == ref.schema+"."+ref.name {
				return true
			}
		default:
			if a == ref.name && (ref.schema == "" || ref.schema == "public") {
				return true
			}
		}
		if ref.schema == "" && strings.HasPrefix(ref.name, "pg_") &&
			(a == "pg_catalog.*" || a == "pg_catalog."+ref.name) {
			return true
		}
	}
	return false
}

