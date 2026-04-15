package output

import "context"

type Record = map[string]any

type Writer interface {
	Prepare(ctx context.Context, columns []string) error
	WriteRecord(ctx context.Context, record Record) error
	WriteAll(ctx context.Context, records []Record) error
	Close() error
}
