package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"simonwaldherr.de/go/nanogo/interp"
)

// CodeExecConfig configures sandboxed code execution via nanoGo.
type CodeExecConfig struct {
	Timeout         time.Duration
	MaxSourceBytes  int
	ReadOnlyFS      bool
	ReadWhitelist   []string
	HTTPGet         bool
	HTTPTimeout     time.Duration
	HTTPMinInterval time.Duration
}

func (c CodeExecConfig) withDefaults() CodeExecConfig {
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.MaxSourceBytes <= 0 {
		c.MaxSourceBytes = 64 * 1024
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 5 * time.Second
	}
	if c.HTTPMinInterval <= 0 {
		c.HTTPMinInterval = 200 * time.Millisecond
	}
	if len(c.ReadWhitelist) == 0 {
		c.ReadWhitelist = []string{"examples", "README.md", "LICENSE"}
	}
	return c
}

// NewCodeExecTool returns a Tool that executes sandboxed Go source using nanoGo.
func NewCodeExecTool(cfg CodeExecConfig) Tool {
	cfg = cfg.withDefaults()

	return Tool{
		Name:        "code_execute",
		Description: "Executes sandboxed Go source code via nanoGo. Source must be a complete Go file with package main and main(). Optional natives: HostReadFile(path), HTTPGetText(url).",
		Parameters: []byte(`{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "Complete Go source code (package main + main function)"
    },
    "timeout_seconds": {
      "type": "integer",
      "description": "Optional timeout override in seconds (capped by server config)"
    }
  },
  "required": ["source"]
}`),
		Execute: func(ctx context.Context, argsJSON string) (string, error) {
			return codeExecute(ctx, cfg, argsJSON)
		},
	}
}

func codeExecute(ctx context.Context, cfg CodeExecConfig, argsJSON string) (string, error) {
	var args struct {
		Source         string `json:"source"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	source := strings.TrimSpace(args.Source)
	if source == "" {
		return "", fmt.Errorf("source is required")
	}
	if len(source) > cfg.MaxSourceBytes {
		return "", fmt.Errorf("source exceeds max_source_bytes (%d > %d)", len(source), cfg.MaxSourceBytes)
	}

	timeout := cfg.Timeout
	if args.TimeoutSeconds > 0 {
		requested := time.Duration(args.TimeoutSeconds) * time.Second
		if requested < time.Second {
			requested = time.Second
		}
		if requested < timeout {
			timeout = requested
		}
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	return runCodeExecute(ctx, cfg, source, timeout, baseDir)
}

func runCodeExecute(ctx context.Context, cfg CodeExecConfig, source string, timeout time.Duration, baseDir string) (retOut string, retErr error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		out string
		err error
	}
	resultCh := make(chan result, 1)

	go func() {
		out, err := runInterpreted(execCtx, cfg, source, baseDir)
		resultCh <- result{out: out, err: err}
	}()

	select {
	case res := <-resultCh:
		if strings.TrimSpace(res.out) == "" && res.err == nil {
			return "(execution completed with no output)", nil
		}
		return strings.TrimSpace(res.out), res.err
	case <-execCtx.Done():
		if execCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("execution timed out after %s", timeout)
		}
		return "", execCtx.Err()
	}
}

func runInterpreted(ctx context.Context, cfg CodeExecConfig, source, baseDir string) (retOut string, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic recovered: %v", r)
		}
	}()

	collector := newOutputCollector(24 * 1024)
	vm := interp.NewInterpreter()
	registerSafeNatives(ctx, vm, cfg, collector, baseDir)
	interp.RegisterBuiltinPackages(vm)

	if err := vm.Run(source); err != nil {
		if s := strings.TrimSpace(collector.String()); s != "" {
			return s, err
		}
		return "", err
	}
	return collector.String(), nil
}

func registerSafeNatives(ctx context.Context, vm *interp.Interpreter, cfg CodeExecConfig, out *outputCollector, baseDir string) {
	vm.RegisterNative("ConsoleLog", func(args []any) (any, error) {
		if len(args) > 0 {
			out.Append("", interp.ToString(args[0]))
		}
		return nil, nil
	})

	vm.RegisterNative("ConsoleWarn", func(args []any) (any, error) {
		if len(args) > 0 {
			out.Append("[warn] ", interp.ToString(args[0]))
		}
		return nil, nil
	})

	vm.RegisterNative("ConsoleError", func(args []any) (any, error) {
		if len(args) > 0 {
			out.Append("[error] ", interp.ToString(args[0]))
		}
		return nil, nil
	})

	vm.RegisterNative("__hostSprintf", func(args []any) (any, error) {
		if len(args) == 0 {
			return "", nil
		}
		format := interp.ToString(args[0])
		fmtArgs := make([]any, 0, len(args)-1)
		for _, a := range args[1:] {
			fmtArgs = append(fmtArgs, a)
		}
		return fmt.Sprintf(format, fmtArgs...), nil
	})

	if cfg.ReadOnlyFS {
		vm.RegisterNative("HostReadFile", func(args []any) (any, error) {
			if len(args) == 0 {
				return "", nil
			}
			full, err := resolveReadablePath(baseDir, interp.ToString(args[0]), cfg.ReadWhitelist)
			if err != nil {
				return nil, err
			}
			f, err := os.Open(full)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, 1<<20))
			if err != nil {
				return nil, err
			}
			return string(b), nil
		})
	}

	if cfg.HTTPGet {
		var httpMu sync.Mutex
		var lastReq time.Time

		vm.RegisterNative("HTTPGetText", func(args []any) (any, error) {
			if len(args) == 0 {
				return "", nil
			}
			rawURL := strings.TrimSpace(interp.ToString(args[0]))
			u, err := url.Parse(rawURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return nil, fmt.Errorf("invalid URL")
			}

			httpMu.Lock()
			now := time.Now()
			if !lastReq.IsZero() {
				wait := cfg.HTTPMinInterval - now.Sub(lastReq)
				if wait > 0 {
					httpMu.Unlock()
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(wait):
					}
					httpMu.Lock()
				}
			}
			lastReq = time.Now()
			httpMu.Unlock()

			reqCtx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", "llmflow-code-exec/1.0")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
			}

			data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return nil, err
			}
			return string(data), nil
		})
	}
}

func resolveReadablePath(baseDir, requested string, whitelist []string) (string, error) {
	cleanReq := filepath.Clean(requested)
	if cleanReq == "." || cleanReq == string(filepath.Separator) {
		return "", fmt.Errorf("access denied: invalid path")
	}
	if filepath.IsAbs(cleanReq) || strings.HasPrefix(cleanReq, "..") || strings.Contains(cleanReq, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: absolute or parent paths not allowed")
	}

	full := filepath.Join(baseDir, cleanReq)
	rel, err := filepath.Rel(baseDir, full)
	if err != nil {
		return "", fmt.Errorf("access denied")
	}
	rel = filepath.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied")
	}

	if !pathAllowed(rel, whitelist) {
		return "", fmt.Errorf("access denied: path not in whitelist")
	}
	return full, nil
}

func pathAllowed(rel string, whitelist []string) bool {
	for _, item := range whitelist {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		clean := filepath.Clean(item)
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			continue
		}
		if rel == clean || strings.HasPrefix(rel, clean+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type outputCollector struct {
	mu      sync.Mutex
	maxSize int
	trunc   bool
	sb      strings.Builder
}

func newOutputCollector(maxSize int) *outputCollector {
	return &outputCollector{maxSize: maxSize}
}

func (o *outputCollector) Append(prefix, msg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.trunc {
		return
	}
	line := prefix + msg
	if o.sb.Len()+len(line)+1 > o.maxSize {
		remaining := o.maxSize - o.sb.Len()
		if remaining > 0 {
			if remaining > len(line) {
				remaining = len(line)
			}
			o.sb.WriteString(line[:remaining])
		}
		o.sb.WriteString("\n... [output truncated]")
		o.trunc = true
		return
	}
	o.sb.WriteString(line)
	o.sb.WriteByte('\n')
}

func (o *outputCollector) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.TrimSpace(o.sb.String())
}
