package output

import (
	"fmt"
	"strings"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

func New(cfg config.OutputConfig) (Writer, error) {
	switch strings.ToLower(cfg.Type) {
	case "csv":
		return NewCSVWriter(cfg)
	case "xlsx":
		return NewXLSXWriter(cfg)
	case "jsonl":
		return NewJSONLWriter(cfg)
	case "xml":
		return NewXMLWriter(cfg)
	case "sqlite":
		return NewSQLWriter("sqlite", cfg)
	case "mssql":
		return NewSQLWriter("mssql", cfg)
	default:
		return nil, fmt.Errorf("unsupported output type: %s", cfg.Type)
	}
}
