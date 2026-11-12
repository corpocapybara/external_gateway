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

// describeColumnsSQL builds the information_schema lookup for postgres.schema.
// Both values are agent-supplied, so both go through QuoteLiteral.
func describeColumnsSQL(schema, table string) string {
	return "SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = " +
		postgres.QuoteLiteral(schema) + " AND table_name = " + postgres.QuoteLiteral(table) + " ORDER BY ordinal_position"
}
