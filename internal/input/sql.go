package input

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type SQLReader struct {
	db    *sql.DB
	query string
}

func NewSQLReader(kind string, cfg config.InputConfig) (*SQLReader, error) {
	var dsn string
	switch kind {
	case "sqlite":
		dsn = config.ResolveSecret(cfg.SQLite.DSN, cfg.SQLite.DSNEnv)
		if dsn == "" {
			dsn = cfg.Path
		}
	case "mssql":
		dsn = config.ResolveSecret(cfg.MSSQL.DSN, cfg.MSSQL.DSNEnv)
	}
	if dsn == "" {
		return nil, fmt.Errorf("missing DSN for %s", kind)
	}
	db, err := sql.Open(kind, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", kind, err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping %s: %w", kind, err)
	}
	if cfg.Query == "" {
		return nil, fmt.Errorf("input.query is required for %s", kind)
	}
	return &SQLReader{db: db, query: cfg.Query}, nil
}

func (r *SQLReader) ReadAll(ctx context.Context) ([]Record, error) {
	rows, err := r.db.QueryContext(ctx, r.query)
	if err != nil {
		return nil, fmt.Errorf("query sql input: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}
	var out []Record
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		rec := Record{}
		for i, c := range cols {
			rec[c] = vals[i]
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil
}

func (r *SQLReader) Close() error { return r.db.Close() }
