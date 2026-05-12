package output

import (
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

// XMLWriter writes output records as an XML document.
// The structure is:
//
//	<records>
//	  <record>
//	    <fieldname>value</fieldname>
//	    …
//	  </record>
//	  …
//	</records>
type XMLWriter struct {
	f        *os.File
	enc      *xml.Encoder
	headers  []string
	prepared bool
}

func NewXMLWriter(cfg config.OutputConfig) (*XMLWriter, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o750); err != nil {
		return nil, fmt.Errorf("create xml output dir: %w", err)
	}
	f, err := os.Create(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("create xml output file: %w", err)
	}
	enc := xml.NewEncoder(f)
	enc.Indent("", "  ")
	return &XMLWriter{f: f, enc: enc}, nil
}

func (w *XMLWriter) Prepare(_ context.Context, columns []string) error {
	if w.prepared {
		return nil
	}
	w.headers = append([]string(nil), columns...)

	// Write XML declaration and open root element.
	if _, err := fmt.Fprint(w.f, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"); err != nil {
		return fmt.Errorf("write xml declaration: %w", err)
	}
	if err := w.enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "records"}}); err != nil {
		return fmt.Errorf("write xml root open: %w", err)
	}
	if err := w.enc.Flush(); err != nil {
		return fmt.Errorf("flush xml root open: %w", err)
	}
	w.prepared = true
	return nil
}

func (w *XMLWriter) WriteRecord(ctx context.Context, record Record) error {
	if !w.prepared {
		// Derive column order from the record itself when Prepare was not called.
		if err := w.Prepare(ctx, slices.Sorted(maps.Keys(record))); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	recStart := xml.StartElement{Name: xml.Name{Local: "record"}}
	if err := w.enc.EncodeToken(recStart); err != nil {
		return fmt.Errorf("write record open: %w", err)
	}

	// Use the prepared header order when available; otherwise sort the keys.
	keys := w.headers
	if len(keys) == 0 {
		keys = slices.Sorted(maps.Keys(record))
	}

	for _, k := range keys {
		v, ok := record[k]
		if !ok {
			continue
		}
		elem := xml.StartElement{Name: xml.Name{Local: sanitizeXMLName(k)}}
		if err := w.enc.EncodeToken(elem); err != nil {
			return fmt.Errorf("write field open %q: %w", k, err)
		}
		if err := w.enc.EncodeToken(xml.CharData(fmt.Sprint(v))); err != nil {
			return fmt.Errorf("write field value %q: %w", k, err)
		}
		if err := w.enc.EncodeToken(elem.End()); err != nil {
			return fmt.Errorf("write field close %q: %w", k, err)
		}
	}

	if err := w.enc.EncodeToken(recStart.End()); err != nil {
		return fmt.Errorf("write record close: %w", err)
	}
	if err := w.enc.Flush(); err != nil {
		return fmt.Errorf("flush record: %w", err)
	}
	return w.f.Sync()
}

func (w *XMLWriter) WriteAll(ctx context.Context, records []Record) error {
	if err := w.Prepare(ctx, unionHeaders(records)); err != nil {
		return err
	}
	for _, rec := range records {
		if err := w.WriteRecord(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (w *XMLWriter) Close() error {
	if w.enc != nil {
		// Close root element and flush.
		_ = w.enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "records"}})
		_ = w.enc.Flush()
	}
	return w.f.Close()
}

// sanitizeXMLName replaces characters that are invalid as XML element names
// with underscores. The first character must be a letter or underscore.
func sanitizeXMLName(s string) string {
	if s == "" {
		return "_"
	}
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
			// always valid
		case i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '.'):
			// valid after first character
		default:
			b[i] = '_'
		}
	}
	if b[0] >= '0' && b[0] <= '9' {
		b[0] = '_'
	}
	return string(b)
}
