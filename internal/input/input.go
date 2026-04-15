package input

import "context"

type Record = map[string]any

type Reader interface {
	Next(ctx context.Context) (Record, error)
	ReadAll(ctx context.Context) ([]Record, error)
	Close() error
}
