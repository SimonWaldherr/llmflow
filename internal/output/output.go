package output

import "context"

type Record = map[string]any

type Writer interface {
	WriteAll(ctx context.Context, records []Record) error
	Close() error
}
