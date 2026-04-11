package tools

// sql.go implements a read-only SQL query tool that supports SQLite and MSSQL.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	// Register drivers used elsewhere in the project.
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)

// NewSQLQueryTool returns a Tool that executes read-only SQL SELECT queries
// against the given database. driver must be "sqlite" or "sqlserver".
func NewSQLQueryTool(driver, dsn string) Tool {
	return Tool{
		Name:        "sql_query",
		Description: "Executes a read-only SQL SELECT query against the configured database and returns the results as a JSON array. Only SELECT and WITH (CTE) statements are permitted.",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "A SQL SELECT (or WITH … SELECT) statement to execute"
    }
  },
  "required": ["query"]
}`),
		Execute: func(ctx context.Context, argsJSON string) (string, error) {
			return runSQLQuery(ctx, driver, dsn, argsJSON)
		},
	}
}

func runSQLQuery(ctx context.Context, driver, dsn, argsJSON string) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	// Safety: only allow read-only statements.
	upper := strings.ToUpper(query)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return "", fmt.Errorf("only SELECT or WITH … SELECT queries are allowed")
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return "", fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("execute query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("get columns: %w", err)
	}

	var results []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", fmt.Errorf("scan row: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = vals[i]
		}
		results = append(results, row)
		if len(results) >= 100 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("rows error: %w", err)
	}

	b, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("marshal results: %w", err)
	}
	return string(b), nil
}
