package server

import (
	"fmt"

	"github.com/external_gateway/internal/connectors/postgres"
)

// pgResolveSchema returns the schema a postgres op runs against: the agent's
// schema param, or the database's configured schema when it is omitted.
func pgResolveSchema(dbSchema, schemaParam string) string {
	if schemaParam == "" {
		return dbSchema
	}
	return schemaParam
}

// pgDescribeSchema resolves the schema for postgres.schema and checks
// schema.table against pg_allowed_tables. It needs no connection, so a refused
// table is turned away before the database is reached.
func pgDescribeSchema(allowed []string, dbSchema, schemaParam, table string) (string, error) {
	schema := pgResolveSchema(dbSchema, schemaParam)
	if !postgres.TableAllowed(allowed, schema, table) {
		return "", fmt.Errorf("table %q is not permitted by pg_allowed_tables", table)
	}
	return schema, nil
}

// pgListTables runs postgres.tables: it lists the resolved schema's tables via
// exec and filters them to pg_allowed_tables on that same schema, so the list
// and the allow-list check can never disagree on the schema.
func pgListTables(exec func(sql string) (*postgres.QueryResult, error), allowed []string, dbSchema, schemaParam string) (*postgres.QueryResult, error) {
	schema := pgResolveSchema(dbSchema, schemaParam)
	res, err := exec(listTablesSQL(schema))
	if err != nil {
		return nil, err
	}
	postgres.FilterTableRows(res, allowed, schema)
	return res, nil
}

// listTablesSQL builds the information_schema lookup for postgres.tables; the
// agent-supplied schema goes through QuoteLiteral.
func listTablesSQL(schema string) string {
	return "SELECT table_name, table_type FROM information_schema.tables WHERE table_schema = " +
		postgres.QuoteLiteral(schema) + " ORDER BY table_name"
}

// describeColumnsSQL builds the information_schema lookup for postgres.schema.
// Both values are agent-supplied, so both go through QuoteLiteral.
func describeColumnsSQL(schema, table string) string {
	return "SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = " +
		postgres.QuoteLiteral(schema) + " AND table_name = " + postgres.QuoteLiteral(table) + " ORDER BY ordinal_position"
}
