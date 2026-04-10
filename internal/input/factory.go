package input

import (
	"fmt"
	"strings"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func New(cfg config.InputConfig) (Reader, error) {
	switch strings.ToLower(cfg.Type) {
	case "csv":
		return NewCSVReader(cfg)
	case "json", "jsonl":
		return NewJSONReader(cfg)
	case "xml":
		return NewXMLReader(cfg)
	case "sqlite":
		return NewSQLReader("sqlite", cfg)
	case "mssql":
		return NewSQLReader("mssql", cfg)
	default:
		return nil, fmt.Errorf("unsupported input type: %s", cfg.Type)
	}
}
