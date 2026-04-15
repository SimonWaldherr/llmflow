package input

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type jsonMode int

const (
	jsonModeUnknown jsonMode = iota
	jsonModeArray
	jsonModeObject
	jsonModeJSONL
)

type JSONReader struct {
	f       *os.File
	cfg     config.InputConfig
	dec     *json.Decoder
	mode    jsonMode
	started bool
	ended   bool
	jsonl   *bufio.Scanner
}

func NewJSONReader(cfg config.InputConfig) (*JSONReader, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open json input: %w", err)
	}
	r := &JSONReader{f: f, cfg: cfg}
	if cfg.Type == "jsonl" || cfg.JSON.JSONL {
		r.mode = jsonModeJSONL
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		r.jsonl = s
	}
	return r, nil
}

func (r *JSONReader) Next(ctx context.Context) (Record, error) {
	if r.ended {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.mode == jsonModeJSONL {
		for r.jsonl.Scan() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			line := strings.TrimSpace(r.jsonl.Text())
			if line == "" {
				continue
			}
			var rec Record
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				return nil, fmt.Errorf("decode jsonl line: %w", err)
			}
			return rec, nil
		}
		if err := r.jsonl.Err(); err != nil {
			return nil, fmt.Errorf("scan jsonl: %w", err)
		}
		r.ended = true
		return nil, io.EOF
	}
	if r.dec == nil {
		r.dec = json.NewDecoder(r.f)
	}
	if !r.started {
		tok, err := r.dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.ended = true
				return nil, io.EOF
			}
			return nil, fmt.Errorf("read json token: %w", err)
		}
		r.started = true
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil, fmt.Errorf("unsupported json top-level token %T", tok)
		}
		switch delim {
		case '[':
			r.mode = jsonModeArray
			return r.nextArrayValue(ctx)
		case '{':
			r.mode = jsonModeObject
			value, err := r.decodeJSONValue(tok)
			if err != nil {
				return nil, err
			}
			r.ended = true
			rec, ok := value.(Record)
			if !ok {
				return nil, fmt.Errorf("json top-level object is not a record")
			}
			return rec, nil
		default:
			return nil, fmt.Errorf("unsupported json top-level token %q", delim)
		}
	}
	if r.mode == jsonModeArray {
		return r.nextArrayValue(ctx)
	}
	r.ended = true
	return nil, io.EOF
}

func (r *JSONReader) nextArrayValue(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tok, err := r.dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			r.ended = true
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read json array token: %w", err)
	}
	if delim, ok := tok.(json.Delim); ok && delim == ']' {
		r.ended = true
		return nil, io.EOF
	}
	value, err := r.decodeJSONValue(tok)
	if err != nil {
		return nil, err
	}
	rec, ok := value.(Record)
	if !ok {
		return nil, fmt.Errorf("json array element is not an object")
	}
	return rec, nil
}

func (r *JSONReader) decodeJSONValue(tok json.Token) (any, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := make(Record)
			for {
				nextTok, err := r.dec.Token()
				if err != nil {
					if errors.Is(err, io.EOF) {
						return nil, fmt.Errorf("unexpected EOF in json object")
					}
					return nil, fmt.Errorf("read json object token: %w", err)
				}
				if delim, ok := nextTok.(json.Delim); ok && delim == '}' {
					return obj, nil
				}
				key, ok := nextTok.(string)
				if !ok {
					return nil, fmt.Errorf("expected json object key, got %T", nextTok)
				}
				valTok, err := r.dec.Token()
				if err != nil {
					if errors.Is(err, io.EOF) {
						return nil, fmt.Errorf("unexpected EOF after json object key %q", key)
					}
					return nil, fmt.Errorf("read json object value: %w", err)
				}
				val, err := r.decodeJSONValue(valTok)
				if err != nil {
					return nil, err
				}
				obj[key] = val
			}
		case '[':
			arr := make([]any, 0)
			for {
				nextTok, err := r.dec.Token()
				if err != nil {
					if errors.Is(err, io.EOF) {
						return nil, fmt.Errorf("unexpected EOF in json array")
					}
					return nil, fmt.Errorf("read json array token: %w", err)
				}
				if delim, ok := nextTok.(json.Delim); ok && delim == ']' {
					return arr, nil
				}
				val, err := r.decodeJSONValue(nextTok)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
		default:
			return nil, fmt.Errorf("unsupported json delimiter %q", t)
		}
	default:
		return t, nil
	}
}

func (r *JSONReader) ReadAll(ctx context.Context) ([]Record, error) {
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

func (r *JSONReader) Close() error { return r.f.Close() }
