package output

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type SQLWriter struct {
	db       *sql.DB
	kind     string
	table    string
	cols     []string
	prepared bool
}

func NewSQLWriter(kind string, cfg config.OutputConfig) (*SQLWriter, error) {
	var dsn, table string
	switch kind {
	case "sqlite":
		dsn = config.ResolveSecret(cfg.SQLite.DSN, cfg.SQLite.DSNEnv)
		if dsn == "" {
			dsn = cfg.Path
		}
		table = firstNonEmpty(cfg.Table, cfg.SQLite.Table)
	case "mssql":
		dsn = config.ResolveSecret(cfg.MSSQL.DSN, cfg.MSSQL.DSNEnv)
		table = firstNonEmpty(cfg.Table, cfg.MSSQL.Table)
	}
	if dsn == "" {
		return nil, fmt.Errorf("missing DSN for %s output", kind)
	}
	if table == "" {
		return nil, fmt.Errorf("missing table for %s output", kind)
	}
	db, err := sql.Open(kind, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s output: %w", kind, err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping %s output: %w", kind, err)
	}
	return &SQLWriter{db: db, kind: kind, table: table}, nil
}

func (w *SQLWriter) Prepare(ctx context.Context, columns []string) error {
	if w.prepared {
		return nil
	}
	w.cols = append([]string(nil), columns...)
	if len(w.cols) == 0 {
		w.prepared = true
		return nil
	}
	if err := w.ensureTable(ctx, w.cols); err != nil {
		return err
	}
	w.prepared = true
	return nil
}

func (w *SQLWriter) WriteRecord(ctx context.Context, record Record) error {
	if !w.prepared {
		if err := w.Prepare(ctx, slices.Sorted(maps.Keys(record))); err != nil {
			return err
		}
	}
	if len(w.cols) == 0 {
		return nil
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	placeholders := make([]string, len(w.cols))
	for i := range w.cols {
		if w.kind == "mssql" {
			placeholders[i] = fmt.Sprintf("@p%d", i+1)
		} else {
			placeholders[i] = "?"
		}
	}
	quotedCols := make([]string, len(w.cols))
	for i, c := range w.cols {
		quotedCols[i] = quoteIdentifier(w.kind, c)
	}
	stmtText := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteIdentifier(w.kind, w.table), strings.Join(quotedCols, ", "), strings.Join(placeholders, ", "))
	stmt, err := tx.PrepareContext(ctx, stmtText)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	args := make([]any, len(w.cols))
	for i, c := range w.cols {
		args[i] = normalizeSQLValue(record[c])
	}
	if _, err := stmt.ExecContext(ctx, args...); err != nil {
		return fmt.Errorf("insert row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (w *SQLWriter) WriteAll(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	cols := unionColumns(records)
	if err := w.Prepare(ctx, cols); err != nil {
		return err
	}
	for _, rec := range records {
		if err := w.WriteRecord(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (w *SQLWriter) ensureTable(ctx context.Context, cols []string) error {
	defs := make([]string, 0, len(cols))
	for _, c := range cols {
		defs = append(defs, fmt.Sprintf("%s %s NULL", quoteIdentifier(w.kind, c), columnType(w.kind)))
	}
	stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", quoteIdentifier(w.kind, w.table), strings.Join(defs, ", "))
	if w.kind == "mssql" {
		stmt = fmt.Sprintf("IF OBJECT_ID('%s', 'U') IS NULL CREATE TABLE %s (%s)", w.table, quoteIdentifier(w.kind, w.table), strings.Join(defs, ", "))
	}
	if _, err := w.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("ensure table: %w", err)
	}
	return nil
}

func (w *SQLWriter) Close() error { return w.db.Close() }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func normalizeSQLValue(v any) any {
	switch t := v.(type) {
	case nil, string, []byte, int64, int32, int, float64, float32, bool:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func unionColumns(records []Record) []string {
	set := map[string]struct{}{}
	for _, r := range records {
		for k := range r {
			set[k] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

func quoteIdentifier(kind, name string) string {
	if kind == "mssql" {
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func columnType(kind string) string {
	if kind == "mssql" {
		return "NVARCHAR(MAX)"
	}
	return "TEXT"
}
