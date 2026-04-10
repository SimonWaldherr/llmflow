package input

import "context"

type Record = map[string]any

type Reader interface {
	ReadAll(ctx context.Context) ([]Record, error)
	Close() error
}
