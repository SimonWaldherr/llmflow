package input

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type XMLReader struct {
	f       *os.File
	cfg     config.InputConfig
	dec     *xml.Decoder
	ended   bool
}

func NewXMLReader(cfg config.InputConfig) (*XMLReader, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open xml input: %w", err)
	}
	return &XMLReader{f: f, cfg: cfg}, nil
}

func (r *XMLReader) Next(ctx context.Context) (Record, error) {
	if r.ended {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.dec == nil {
		r.dec = xml.NewDecoder(r.f)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		tok, err := r.dec.Token()
		if err != nil {
			if err == io.EOF {
				r.ended = true
				return nil, io.EOF
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
		if err := r.dec.DecodeElement(&node, &se); err != nil {
			return nil, fmt.Errorf("decode xml element: %w", err)
		}
		return node.ToMap(), nil
	}
}

func (r *XMLReader) ReadAll(ctx context.Context) ([]Record, error) {
	var out []Record
	for {
		rec, err := r.Next(ctx)
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
		out = append(out, rec)
	}
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
