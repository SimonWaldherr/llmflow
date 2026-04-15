package input

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/SimonWaldherr/llmflow/internal/config"
)

type CSVReader struct {
	f       *os.File
	cfg     config.InputConfig
	cr      *csv.Reader
	headers []string
	started bool
	ended   bool
}

func NewCSVReader(cfg config.InputConfig) (*CSVReader, error) {
	f, err := os.Open(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	return &CSVReader{f: f, cfg: cfg}, nil
}

// sniffDelimiter reads the first few lines of r and returns the most likely
// field separator among comma, semicolon, tab, and pipe.
// It counts how evenly each candidate splits the first lines and picks the
// one with the highest, most-consistent field count.
func sniffDelimiter(r io.ReadSeeker) rune {
	const sniffBytes = 8192
	buf := make([]byte, sniffBytes)
	n, _ := r.Read(buf)
	buf = buf[:n]
	r.Seek(0, io.SeekStart) // rewind for normal reading

	candidates := []rune{',', ';', '\t', '|'}
	best := ','
	bestScore := -1

	scanner := bufio.NewScanner(bytes.NewReader(buf))
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= 10 {
			break
		}
	}
	if len(lines) == 0 {
		return best
	}

	for _, delim := range candidates {
		counts := make([]int, len(lines))
		for i, line := range lines {
			counts[i] = strings.Count(line, string(delim))
		}
		if counts[0] == 0 {
			continue // delimiter not present
		}
		// Prefer delimiters where the count is consistent across lines.
		allSame := true
		for _, c := range counts[1:] {
			if c != counts[0] {
				allSame = false
				break
			}
		}
		score := counts[0] * 10
		if allSame {
			score += 100
		}
		if score > bestScore {
			bestScore = score
			best = delim
		}
	}
	return best
}

func (r *CSVReader) init() {
	if r.cr != nil {
		return
	}
	r.cr = csv.NewReader(r.f)
	r.cr.FieldsPerRecord = -1

	delim, _ := utf8.DecodeRuneInString(r.cfg.CSV.Delimiter)
	if delim != utf8.RuneError {
		r.cr.Comma = delim
	} else {
		// No delimiter configured — auto-detect from file content.
		r.cr.Comma = sniffDelimiter(r.f)
	}
}

func (r *CSVReader) Next(ctx context.Context) (Record, error) {
	if r.ended {
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.init()

	if !r.started {
		r.started = true
		if r.cfg.CSV.HasHeader {
			headers, err := r.cr.Read()
			if err != nil {
				if err == io.EOF {
					r.ended = true
					return nil, io.EOF
				}
				return nil, fmt.Errorf("read csv header: %w", err)
			}
			r.headers = append([]string(nil), headers...)
		} else {
			row, err := r.cr.Read()
			if err != nil {
				if err == io.EOF {
					r.ended = true
					return nil, io.EOF
				}
				return nil, fmt.Errorf("read csv row: %w", err)
			}
			r.headers = make([]string, len(row))
			for i := range row {
				r.headers[i] = fmt.Sprintf("col_%d", i+1)
			}
			rec := make(Record, len(r.headers))
			for i, h := range r.headers {
				rec[h] = row[i]
			}
			return rec, nil
		}
	}

	row, err := r.cr.Read()
	if err != nil {
		if err == io.EOF {
			r.ended = true
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read csv row: %w", err)
	}
	rec := make(Record, len(r.headers))
	for i, h := range r.headers {
		if i < len(row) {
			rec[h] = row[i]
		} else {
			rec[h] = ""
		}
	}
	return rec, nil
}

func (r *CSVReader) ReadAll(ctx context.Context) ([]Record, error) {
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

func (r *CSVReader) Close() error { return r.f.Close() }
