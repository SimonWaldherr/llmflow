package input

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type SQLReader struct {
	db       *sql.DB
	query    string
	rows     *sql.Rows
	cols     []string
	started  bool
	finished bool
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

func (r *SQLReader) Next(ctx context.Context) (Record, error) {
	if r.finished {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !r.started {
		rows, err := r.db.QueryContext(ctx, r.query)
		if err != nil {
			return nil, fmt.Errorf("query sql input: %w", err)
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("columns: %w", err)
		}
		r.rows = rows
		r.cols = cols
		r.started = true
	}
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			r.finished = true
			return nil, fmt.Errorf("iterate rows: %w", err)
		}
		r.finished = true
		_ = r.rows.Close()
		return nil, io.EOF
	}
	vals := make([]any, len(r.cols))
	ptrs := make([]any, len(r.cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := r.rows.Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("scan row: %w", err)
	}
	rec := Record{}
	for i, c := range r.cols {
		rec[c] = vals[i]
	}
	return rec, nil
}

func (r *SQLReader) ReadAll(ctx context.Context) ([]Record, error) {
	var out []Record
	for {
		rec, err := r.Next(ctx)
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
		out = append(out, rec)
	}
}

func (r *SQLReader) Close() error {
	if r.rows != nil {
		_ = r.rows.Close()
	}
	return r.db.Close()
}
