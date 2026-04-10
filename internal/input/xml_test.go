package input

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

const sampleXML = `<?xml version="1.0"?>
<records>
  <record>
    <name>Alice</name>
    <city>Berlin</city>
  </record>
  <record>
    <name>Bob</name>
    <city>Hamburg</city>
  </record>
</records>`

func writeTempXML(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "data.xml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestXMLReader_NoFilter(t *testing.T) {
	p := writeTempXML(t, sampleXML)
	cfg := config.InputConfig{Type: "xml", Path: p}
	r, err := NewXMLReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// expect at least 1 record (the root <records> element)
	if len(recs) == 0 {
		t.Fatal("expected at least one record")
	}
}

func TestXMLReader_RecordPath(t *testing.T) {
	p := writeTempXML(t, sampleXML)
	cfg := config.InputConfig{
		Type: "xml",
		Path: p,
		XML:  config.XMLConfig{RecordPath: "record"},
	}
	r, err := NewXMLReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	recs, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
}

func TestXMLReader_FileNotFound(t *testing.T) {
	cfg := config.InputConfig{Type: "xml", Path: "/nonexistent/file.xml"}
	_, err := NewXMLReader(cfg)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
