package output

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type XLSXWriter struct {
	f        *os.File
	headers  []string
	records  []Record
	prepared bool
}

func NewXLSXWriter(cfg config.OutputConfig) (*XLSXWriter, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o750); err != nil {
		return nil, fmt.Errorf("create xlsx output dir: %w", err)
	}
	f, err := os.Create(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("create xlsx output: %w", err)
	}
	return &XLSXWriter{f: f}, nil
}

func (w *XLSXWriter) Prepare(_ context.Context, columns []string) error {
	if w.prepared {
		return nil
	}
	w.headers = append([]string(nil), columns...)
	w.prepared = true
	return nil
}

func (w *XLSXWriter) WriteRecord(ctx context.Context, record Record) error {
	if !w.prepared {
		if err := w.Prepare(ctx, sortedRecordKeys(record)); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	rec := cloneRecord(record)
	w.headers, _ = mergeHeaders(w.headers, rec)
	w.records = append(w.records, rec)
	return nil
}

func (w *XLSXWriter) WriteAll(ctx context.Context, records []Record) error {
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

func (w *XLSXWriter) Close() error {
	if w.f == nil {
		return nil
	}
	if _, err := w.f.Seek(0, 0); err != nil {
		return err
	}
	if err := w.f.Truncate(0); err != nil {
		return err
	}
	zw := zip.NewWriter(w.f)
	if err := w.writePackage(zw); err != nil {
		_ = zw.Close()
		_ = w.f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = w.f.Close()
		return fmt.Errorf("close xlsx archive: %w", err)
	}
	return w.f.Close()
}

func (w *XLSXWriter) writePackage(zw *zip.Writer) error {
	files := map[string]string{
		"[Content_Types].xml":        contentTypesXML,
		"_rels/.rels":                rootRelsXML,
		"xl/workbook.xml":            workbookXML,
		"xl/_rels/workbook.xml.rels": workbookRelsXML,
		"xl/worksheets/sheet1.xml":   w.sheetXML(),
	}
	order := []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"xl/workbook.xml",
		"xl/_rels/workbook.xml.rels",
		"xl/worksheets/sheet1.xml",
	}
	for _, name := range order {
		fw, err := zw.Create(name)
		if err != nil {
			return fmt.Errorf("create xlsx part %s: %w", name, err)
		}
		if _, err := fw.Write([]byte(files[name])); err != nil {
			return fmt.Errorf("write xlsx part %s: %w", name, err)
		}
	}
	return nil
}

func (w *XLSXWriter) sheetXML() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	if len(w.headers) > 0 {
		writeXLSXRow(&b, 1, w.headers)
	}
	for i, rec := range w.records {
		values := make([]string, len(w.headers))
		for j, h := range w.headers {
			values[j] = stringifyCell(rec[h])
		}
		writeXLSXRow(&b, i+2, values)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func writeXLSXRow(b *strings.Builder, row int, values []string) {
	b.WriteString(fmt.Sprintf(`<row r="%d">`, row))
	for i, value := range values {
		ref := fmt.Sprintf("%s%d", xlsxColumnName(i+1), row)
		b.WriteString(fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, escapeXMLText(value)))
	}
	b.WriteString(`</row>`)
}

func xlsxColumnName(n int) string {
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func escapeXMLText(s string) string {
	repl := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return repl.Replace(s)
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
</Types>`

const rootRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

const workbookXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Output" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`

const workbookRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>`
