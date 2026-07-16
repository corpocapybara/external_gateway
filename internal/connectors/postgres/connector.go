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
	readOnly bool
	db       *sql.DB
	connStr  string
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

func (c *Connector) Connect(host string, port int, user, password, dbname, schema string) error {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   dbname,
	}
	q := u.Query()
	q.Set("sslmode", "disable")
	q.Set("connect_timeout", "10")
	if schema != "" {
		q.Set("search_path", schema)
	}
	u.RawQuery = q.Encode()

	connStr := u.String()
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("opening connection: %w", err)
	}

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

	if err := c.db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connection lost: %w", err)
	}

	wrappedQuery := fmt.Sprintf(
		"SET LOCAL statement_timeout = '5000'; BEGIN TRANSACTION READ ONLY; %s",
		sanitized,
	)

	rows, err := c.db.QueryContext(ctx, wrappedQuery)
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
		switch s := item.(type) {
		case *pgparsernodes.SelectStmt:
			if s.LockingClause != nil && s.LockingClause.Len() > 0 {
				return fmt.Errorf("SELECT FOR UPDATE/SHARE is not allowed")
			}
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
