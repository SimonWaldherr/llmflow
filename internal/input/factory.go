package input

import (
	"context"
	"fmt"
	"strings"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

// New creates an input Reader based on cfg.Type and applies excluded_columns filtering.
func New(cfg config.InputConfig) (Reader, error) {
	var r Reader
	var err error
	switch strings.ToLower(cfg.Type) {
	case "csv":
		r, err = NewCSVReader(cfg)
	case "json", "jsonl":
		r, err = NewJSONReader(cfg)
	case "xml":
		r, err = NewXMLReader(cfg)
	case "sqlite":
		r, err = NewSQLReader("sqlite", cfg)
	case "mssql":
		r, err = NewSQLReader("mssql", cfg)
	default:
		return nil, fmt.Errorf("unsupported input type: %s", cfg.Type)
	}
	if err != nil {
		return nil, err
	}
	if len(cfg.ExcludedColumns) > 0 {
		r = &filteredReader{inner: r, excluded: buildExcludedSet(cfg.ExcludedColumns)}
	}
	return r, nil
}

// buildExcludedSet turns configured excluded column names into a lookup set.
func buildExcludedSet(cols []string) map[string]struct{} {
	set := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		set[c] = struct{}{}
	}
	return set
}

// filteredReader wraps another Reader and removes excluded columns from every record.
type filteredReader struct {
	inner    Reader
	excluded map[string]struct{}
}

func (f *filteredReader) Next(ctx context.Context) (Record, error) {
	rec, err := f.inner.Next(ctx)
	if err != nil {
		return nil, err
	}
	return f.filterRecord(rec), nil
}

func (f *filteredReader) ReadAll(ctx context.Context) ([]Record, error) {
	all, err := f.inner.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	for i, rec := range all {
		all[i] = f.filterRecord(rec)
	}
	return all, nil
}

func (f *filteredReader) Close() error { return f.inner.Close() }

func (f *filteredReader) filterRecord(rec Record) Record {
	out := make(Record, len(rec))
	for k, v := range rec {
		if _, excluded := f.excluded[k]; !excluded {
			out[k] = v
		}
	}
	return out
}
