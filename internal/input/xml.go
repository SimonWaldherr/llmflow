package input

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type XMLReader struct {
	f   *os.File
	cfg config.InputConfig
}

func NewXMLReader(cfg config.InputConfig) (*XMLReader, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open xml input: %w", err)
	}
	return &XMLReader{f: f, cfg: cfg}, nil
}

func (r *XMLReader) ReadAll(ctx context.Context) ([]Record, error) {
	_ = ctx
	dec := xml.NewDecoder(r.f)
	var out []Record
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("read xml token: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if r.cfg.XML.RecordPath != "" && se.Name.Local != r.cfg.XML.RecordPath {
			continue
		}
		var node genericXMLNode
		if err := dec.DecodeElement(&node, &se); err != nil {
			return nil, fmt.Errorf("decode xml element: %w", err)
		}
		out = append(out, node.ToMap())
	}
	return out, nil
}

func (r *XMLReader) Close() error { return r.f.Close() }

type genericXMLNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr       `xml:",any,attr"`
	Nodes   []genericXMLNode `xml:",any"`
	Text    string           `xml:",chardata"`
}

func (n genericXMLNode) ToMap() Record {
	m := Record{}
	for _, a := range n.Attrs {
		m["@"+a.Name.Local] = a.Value
	}
	if len(n.Nodes) == 0 {
		if n.Text != "" {
			m[n.XMLName.Local] = n.Text
		}
		return m
	}
	for _, child := range n.Nodes {
		m[child.XMLName.Local] = child.ToMap()
	}
	if n.Text != "" {
		m["#text"] = n.Text
	}
	return m
}
