package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/enrich"
	"github.com/SimonWaldherr/llmflow/internal/input"
	"github.com/SimonWaldherr/llmflow/internal/llm"
	"github.com/SimonWaldherr/llmflow/internal/output"
	"github.com/SimonWaldherr/llmflow/internal/prompt"
	"github.com/SimonWaldherr/llmflow/internal/tools"
)

// Generator is the interface used to call an LLM, allowing injection of test fakes.
type Generator interface {
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// App coordinates input, prompt building, LLM calls, and output writing for one job run.
type App struct {
	cfg           config.Config
	logger        *slog.Logger
	dryRun        bool
	progressFunc  func(current, total int)
	resultFunc    func(index, total int, record map[string]any)
	workerLimiter WorkerLimiter
}

const MaxWorkersEnv = "LLMFLOW_MAX_WORKERS"

// WorkerLimiter limits concurrently active record workers across one process.
type WorkerLimiter interface {
	Acquire(ctx context.Context) (func(), error)
	Max() int
}

type semaphoreWorkerLimiter struct {
	slots chan struct{}
}

func NewWorkerLimiter(max int) WorkerLimiter {
	if max <= 0 {
		return nil
	}
	return &semaphoreWorkerLimiter{slots: make(chan struct{}, max)}
}

func (l *semaphoreWorkerLimiter) Acquire(ctx context.Context) (func(), error) {
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *semaphoreWorkerLimiter) Max() int {
	if l == nil {
		return 0
	}
	return cap(l.slots)
}

func MaxWorkersFromEnv() (int, error) {
	raw := strings.TrimSpace(os.Getenv(MaxWorkersEnv))
	if raw == "" {
		return 0, nil
	}
	max, err := strconv.Atoi(raw)
	if err != nil || max < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", MaxWorkersEnv)
	}
	return max, nil
}

func ClampWorkers(requested, max int) int {
	if requested <= 0 {
		requested = 1
	}
	if max > 0 && requested > max {
		return max
	}
	return requested
}

// WithProgressFunc sets a callback invoked after each record is processed.
func (a *App) WithProgressFunc(f func(current, total int)) *App { a.progressFunc = f; return a }

// WithResultFunc sets a callback invoked for each successful output record.
func (a *App) WithResultFunc(f func(index, total int, record map[string]any)) *App {
	a.resultFunc = f
	return a
}

// WithWorkerLimiter applies a shared process-wide worker limit.
func (a *App) WithWorkerLimiter(l WorkerLimiter) *App {
	a.workerLimiter = l
	return a
}

// New constructs an App with defaults applied to the supplied configuration.
func New(cfg config.Config, logger *slog.Logger) *App {
	cfg.ApplyDefaults()
	return &App{cfg: cfg, logger: logger, dryRun: cfg.Processing.DryRun}
}

// WithDryRun overrides the dry-run flag set in the config (useful for CLI flag).
func (a *App) WithDryRun(v bool) *App { a.dryRun = v; return a }

// WithDebug overrides the debug flag set in the config (useful for CLI flag).
func (a *App) WithDebug(v bool) *App { a.cfg.Processing.Debug = v; return a }

// Run executes the configured job from input reading through output writing.
func (a *App) Run(ctx context.Context) error {
	var gen Generator

	if a.dryRun {
		a.logger.Info("dry-run mode enabled — no LLM calls will be made")
		gen = &dryRunGenerator{
			responseField: a.cfg.Processing.ResponseField,
			schema:        a.cfg.Processing.EffectiveLLMResponseSchema(),
		}
	} else {
		apiKey, err := a.cfg.APIKey()
		if err != nil {
			return err
		}
		gen = llm.New(a.cfg.API, apiKey)
	}

	if a.cfg.Processing.Debug {
		a.logger.Info("debug mode enabled — all LLM prompts and responses will be logged")
		gen = llm.NewDebuggingGenerator(gen, a.logger)
	}

	reader, err := input.New(a.cfg.Input)
	if err != nil {
		return err
	}
	defer reader.Close()

	writer, err := output.New(a.cfg.Output)
	if err != nil {
		return err
	}
	defer writer.Close()

	pb, err := prompt.New(a.cfg.Prompt)
	if err != nil {
		return err
	}

	// Build the list of enabled tools (only relevant when tools.enabled = true).
	activeTools := a.buildTools()

	maxWorkers := 0
	if a.workerLimiter != nil {
		maxWorkers = a.workerLimiter.Max()
	} else if envMax, envErr := MaxWorkersFromEnv(); envErr != nil {
		a.logger.Warn("invalid max workers setting ignored", "env", MaxWorkersEnv, "error", envErr)
	} else {
		maxWorkers = envMax
	}
	workers := ClampWorkers(a.cfg.Processing.Workers, maxWorkers)
	if maxWorkers > 0 && a.cfg.Processing.Workers > maxWorkers {
		a.logger.Warn("worker count capped by system limit", "requested", a.cfg.Processing.Workers, "effective", workers, "max_workers", maxWorkers)
	}

	var rateLimiter <-chan time.Time
	if rpm := a.cfg.API.RateLimitRPM; rpm > 0 && !a.dryRun {
		ticker := time.NewTicker(time.Minute / time.Duration(rpm))
		defer ticker.Stop()
		rateLimiter = ticker.C
	}

	var writerPrepared bool
	emit := func(_ int, record map[string]any) error {
		if !writerPrepared {
			columns := append([]string(nil), a.cfg.Processing.OutputFields...)
			if len(columns) == 0 {
				columns = buildOutputColumns([]input.Record{record}, a.cfg.Processing.ResponseField, a.cfg.Processing.IncludeInputInOutput, a.cfg.Processing.KeyColumn)
			}
			if err := writer.Prepare(ctx, columns); err != nil {
				return err
			}
			writerPrepared = true
		}
		return writer.WriteRecord(ctx, record)
	}

	results, err := a.processRecordStream(ctx, gen, pb, activeTools, reader, workers, rateLimiter, emit)
	if err != nil {
		return err
	}
	a.logger.Info("wrote output records", "count", len(results))
	return nil
}

func (a *App) acquireWorkerSlot(ctx context.Context) (func(), error) {
	if a.workerLimiter == nil {
		return func() {}, nil
	}
	return a.workerLimiter.Acquire(ctx)
}

// PreviewRecords reads up to n records from the given reader without any LLM
// interaction. Used by the web UI to show a data preview before starting a job.
func PreviewRecords(r input.Reader, n int) ([]input.Record, error) {
	if n <= 0 {
		n = 10
	}
	ctx := context.Background()
	var out []input.Record
	for i := 0; i < n; i++ {
		rec, err := r.Next(ctx)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func buildOutputColumns(records []input.Record, responseField string, includeInput bool, keyColumn string) []string {
	set := map[string]struct{}{}
	for _, rec := range records {
		for key := range rec {
			set[key] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// buildTools constructs the slice of active Tool objects from the config.
func (a *App) buildTools() []tools.Tool {
	if !a.cfg.Tools.Enabled {
		return nil
	}
	var ts []tools.Tool
	addTool := func(enabled bool, name string, tool tools.Tool) {
		if !enabled {
			return
		}
		ts = append(ts, tool)
		a.logger.Info("tool enabled", "tool", name)
	}
	addTool(a.cfg.Tools.WebFetch, "web_fetch", tools.NewWebFetchTool())
	addTool(a.cfg.Tools.WebSearch, "web_search", tools.NewWebSearchTool())
	addTool(a.cfg.Tools.WebExtractLinks, "web_extract_links", tools.NewWebExtractLinksTool())
	addTool(a.cfg.Tools.TextStats, "text_stats", tools.NewTextStatsTool())
	addTool(a.cfg.Tools.RegexExtract, "regex_extract", tools.NewRegexExtractTool())
	addTool(a.cfg.Tools.JSONExtract, "json_extract", tools.NewJSONExtractTool())
	if a.cfg.Tools.CodeExecute {
		addTool(true, "code_execute", tools.NewCodeExecTool(tools.CodeExecConfig{
			Timeout:         a.cfg.Tools.Code.Timeout,
			MaxSourceBytes:  a.cfg.Tools.Code.MaxSourceBytes,
			ReadOnlyFS:      a.cfg.Tools.Code.ReadOnlyFS,
			ReadWhitelist:   a.cfg.Tools.Code.ReadWhitelist,
			HTTPGet:         a.cfg.Tools.Code.HTTPGet,
			HTTPTimeout:     a.cfg.Tools.Code.HTTPTimeout,
			HTTPMinInterval: a.cfg.Tools.Code.HTTPMinInterval,
		}))
	}
	if a.cfg.Tools.SQLQuery {
		dsn := config.ResolveSecret(a.cfg.Tools.SQL.DSN, a.cfg.Tools.SQL.DSNEnv)
		driver := a.cfg.Tools.SQL.Driver
		if driver == "" {
			driver = "sqlite"
		}
		if strings.TrimSpace(dsn) == "" && strings.EqualFold(driver, "sqlite") {
			switch {
			case strings.EqualFold(a.cfg.Input.Type, "sqlite") && strings.TrimSpace(a.cfg.Input.Path) != "":
				dsn = a.cfg.Input.Path
			case strings.EqualFold(a.cfg.Output.Type, "sqlite") && strings.TrimSpace(a.cfg.Output.Path) != "":
				dsn = a.cfg.Output.Path
			}
		}
		if strings.TrimSpace(dsn) == "" {
			a.logger.Warn("sql_query tool enabled with empty DSN; set tools.sql.dsn or tools.sql.dsn_env", "driver", driver)
		}
		addTool(true, "sql_query", tools.NewSQLQueryTool(driver, dsn))
		a.logger.Info("tool enabled", "tool", "sql_query", "driver", driver)
	}
	return ts
}

type indexedResult struct {
	idx int
	rec map[string]any
}

type recordJob struct {
	idx int
	rec input.Record
}

func formatLLMErrorWithIO(prefix string, err error, systemPrompt, userPrompt, responseText string) error {
	return fmt.Errorf(
		"%s: %w\n----- LLM SYSTEM PROMPT -----\n%s\n----- LLM USER PROMPT -----\n%s\n----- LLM RESPONSE -----\n%s",
		prefix,
		err,
		systemPrompt,
		userPrompt,
		responseText,
	)
}

func (a *App) logLLMErrorWithIO(msg string, index int, err error, systemPrompt, userPrompt, responseText string) {
	a.logger.Error(
		msg,
		"index",
		index,
		"error",
		err,
		"llm_system_prompt",
		systemPrompt,
		"llm_user_prompt",
		userPrompt,
		"llm_response",
		responseText,
	)
}

func (a *App) processRecords(
	ctx context.Context,
	gen Generator,
	pb *prompt.Builder,
	activeTools []tools.Tool,
	records []input.Record,
	workers int,
	rateLimiter <-chan time.Time,
) ([]map[string]any, error) {
	return a.processRecordsWithSink(ctx, gen, pb, activeTools, records, workers, rateLimiter, nil)
}

func (a *App) processRecordsWithSink(
	ctx context.Context,
	gen Generator,
	pb *prompt.Builder,
	activeTools []tools.Tool,
	records []input.Record,
	workers int,
	rateLimiter <-chan time.Time,
	emit func(index int, record map[string]any) error,
) ([]map[string]any, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var processed int64

	type job struct {
		idx int
		rec input.Record
	}

	jobs := make(chan job, len(records))
	for i, r := range records {
		jobs <- job{idx: i, rec: r}
	}
	close(jobs)

	resultCh := make(chan indexedResult, len(records))

	var mu sync.Mutex
	var firstErr error

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releaseWorker, err := a.acquireWorkerSlot(ctx)
			if err != nil {
				mu.Lock()
				if firstErr == nil && !errors.Is(err, context.Canceled) {
					firstErr = fmt.Errorf("acquire worker slot: %w", err)
				}
				mu.Unlock()
				return
			}
			defer releaseWorker()
			for j := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if rateLimiter != nil {
					select {
					case <-ctx.Done():
						return
					case <-rateLimiter:
					}
				}

				// Enrichment step (non-agentic web crawl) before prompt building.
				rec := j.rec
				if a.cfg.Enrich.Enabled && a.cfg.Enrich.Column != "" {
					enricher := enrich.New(a.cfg.Enrich)
					if enriched, enrichErr := enricher.Enrich(ctx, rec); enrichErr != nil {
						a.logger.Warn("enrich step failed", "index", j.idx, "error", enrichErr)
					} else {
						rec = enriched
					}
				}

				userPrompt, err := pb.Build(rec)
				if err != nil {
					if !a.cfg.Processing.ContinueOnError {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("build prompt for record %d: %w", j.idx, err)
							cancel()
						}
						mu.Unlock()
						return
					}
					a.logger.Error("build prompt failed", "index", j.idx, "error", err)
					continue
				}
				if instr := prompt.FormatInstructions(a.cfg.Processing); instr != "" {
					userPrompt = userPrompt + "\n\n" + instr
				}
				systemPrompt := pb.SystemPrompt()
				if sysNote := prompt.FormatSystemNote(a.cfg.Processing); sysNote != "" {
					if strings.TrimSpace(systemPrompt) != "" {
						systemPrompt = strings.TrimSpace(systemPrompt) + "\n\n" + sysNote
					} else {
						systemPrompt = sysNote
					}
				}

				var responseText string
				if len(activeTools) > 0 {
					responseText, err = a.runAgentic(ctx, gen, systemPrompt, userPrompt, activeTools, j.idx)
				} else {
					responseText, err = a.generateWithRetry(ctx, gen, systemPrompt, userPrompt)
				}
				if err != nil {
					errWithIO := formatLLMErrorWithIO(
						fmt.Sprintf("llm call for record %d", j.idx),
						err,
						systemPrompt,
						userPrompt,
						responseText,
					)
					if !a.cfg.Processing.ContinueOnError {
						mu.Lock()
						if firstErr == nil {
							firstErr = errWithIO
							cancel()
						}
						mu.Unlock()
						return
					}
					a.logLLMErrorWithIO("llm call failed", j.idx, err, systemPrompt, userPrompt, responseText)
					continue
				}

				outRec := map[string]any{}
				if a.cfg.Processing.IncludeInputInOutput {
					if a.cfg.Processing.KeyColumn != "" {
						if v, ok := rec[a.cfg.Processing.KeyColumn]; ok {
							outRec[a.cfg.Processing.KeyColumn] = v
						}
					} else {
						for k, v := range rec {
							outRec[k] = v
						}
					}
				}
				var parsed map[string]any
				if requiresJSONOutput(a.cfg.Processing) {
					var parseErr error
					parsed, parseErr = parseJSONResponseFields(responseText, a.cfg.Processing)
					if parseErr != nil && strictOutputEnabled(a.cfg.Processing) {
						if repaired, repairErr := a.repairStructuredResponse(ctx, gen, responseText); repairErr == nil {
							if reparsed, reparsedErr := parseJSONResponseFields(repaired, a.cfg.Processing); reparsedErr == nil {
								responseText = repaired
								parsed = reparsed
								parseErr = nil
								a.logger.Warn("structured response repaired", "index", j.idx)
							}
						}
					}
					if parseErr != nil {
						errWithIO := formatLLMErrorWithIO(
							fmt.Sprintf("invalid structured response for record %d", j.idx),
							parseErr,
							systemPrompt,
							userPrompt,
							responseText,
						)
						if !a.cfg.Processing.ContinueOnError {
							mu.Lock()
							if firstErr == nil {
								firstErr = errWithIO
								cancel()
							}
							mu.Unlock()
							return
						}
						a.logLLMErrorWithIO("invalid structured response", j.idx, parseErr, systemPrompt, userPrompt, responseText)
						continue
					}
					for k, v := range parsed {
						if _, exists := outRec[k]; exists && k != a.cfg.Processing.ResponseField {
							a.logger.Warn("parse_json_response: JSON key conflicts with existing field, overwriting", "key", k, "index", j.idx)
						}
						outRec[k] = v
					}
				}
				storeResponseField(outRec, a.cfg.Processing, responseText, parsed)
				resultCh <- indexedResult{idx: j.idx, rec: applyOutputFields(outRec, a.cfg.Processing.OutputFields)}
				cur := int(atomic.AddInt64(&processed, 1))
				if a.progressFunc != nil {
					a.progressFunc(cur, len(records))
				}
				if a.resultFunc != nil {
					preview := make(map[string]any, len(outRec))
					for k, v := range outRec {
						preview[k] = v
					}
					a.resultFunc(j.idx, len(records), preview)
				}
				if cur == 1 || cur == len(records) || cur%10 == 0 {
					a.logger.Info("processing progress", "current", cur, "total", len(records))
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	ordered := make([]map[string]any, len(records))
	nextEmit := 0
	deliverErr := error(nil)
	emitReady := func() error {
		if emit == nil {
			return nil
		}
		for nextEmit < len(ordered) && ordered[nextEmit] != nil {
			if err := emit(nextEmit, ordered[nextEmit]); err != nil {
				return err
			}
			nextEmit++
		}
		return nil
	}

	for ir := range resultCh {
		ordered[ir.idx] = ir.rec
		if deliverErr == nil {
			if err := emitReady(); err != nil {
				deliverErr = err
				cancel()
			}
		}
	}
	if deliverErr != nil {
		return nil, deliverErr
	}

	mu.Lock()
	err := firstErr
	mu.Unlock()
	if err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(records))
	for _, r := range ordered {
		if r != nil {
			results = append(results, r)
		}
	}
	return results, nil
}

// structuredResponseFormat reports whether response_format requests structured
// output fields (json/xml/csv). Conversion to xml/csv still happens in backend;
// the LLM contract remains JSON.
func structuredResponseFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "xml", "csv":
		return true
	}
	return false
}

func requiresJSONOutput(_ config.ProcessingConfig) bool {
	return true
}

func strictOutputEnabled(cfg config.ProcessingConfig) bool {
	if cfg.StrictOutput == nil {
		return true
	}
	return *cfg.StrictOutput
}

func storeRawResponseEnabled(cfg config.ProcessingConfig) bool {
	if cfg.StoreRawResponse == nil {
		return true
	}
	return *cfg.StoreRawResponse
}

func includeThinkingInResponseFieldEnabled(cfg config.ProcessingConfig) bool {
	if cfg.IncludeThinkingInResponseField == nil {
		return true
	}
	return *cfg.IncludeThinkingInResponseField
}

func storeResponseField(outRec map[string]any, cfg config.ProcessingConfig, raw string, parsed map[string]any) {
	if !storeRawResponseEnabled(cfg) || strings.TrimSpace(cfg.ResponseField) == "" {
		return
	}
	if len(parsed) > 0 {
		if _, exists := parsed[cfg.ResponseField]; exists {
			// Preserve parsed structured value when it already uses response_field.
			return
		}
	}
	if !cfg.Thinking || includeThinkingInResponseFieldEnabled(cfg) {
		outRec[cfg.ResponseField] = raw
		return
	}
	// Hide thinking content while keeping the final answer.
	if len(parsed) > 0 {
		if b, err := json.Marshal(parsed); err == nil {
			outRec[cfg.ResponseField] = string(b)
			return
		}
	}
	outRec[cfg.ResponseField] = stripThinkingBlock(raw)
}

func stripThinkingBlock(raw string) string {
	trimmed := strings.TrimSpace(raw)
	const closeTag = "</thinking>"
	if idx := strings.Index(trimmed, closeTag); idx >= 0 {
		return strings.TrimSpace(trimmed[idx+len(closeTag):])
	}
	return trimmed
}

// extractLastJSON finds and parses the last JSON object {...} in s.
func extractLastJSON(s string) (map[string]any, bool) {
	end := strings.LastIndex(s, "}")
	if end < 0 {
		return nil, false
	}
	for start := end - 1; start >= 0; start-- {
		if s[start] != '{' {
			continue
		}
		var out map[string]any
		if json.Unmarshal([]byte(s[start:end+1]), &out) == nil {
			return out, true
		}
	}
	return nil, false
}

// parseJSONResponseFields extracts structured fields from a model response.
// llmflow expects JSON from the model for all modes.
// In strict mode, response must be exactly one JSON object (optionally after
// <thinking>...</thinking> when thinking is enabled).
//
// In lenient mode (strict_output=false), the last JSON object is extracted from
// mixed text and schema validation is skipped.
//
// Legacy structured mode notes:
// - response_format json/xml/csv still controls downstream conversion and schema.
// - xml/csv are backend output transforms; model I/O stays JSON.
//
// Strict behavior:
//   - without thinking: response must be exactly one JSON object
//   - with thinking: response must start with <thinking>...</thinking> and then
//     exactly one JSON object
func parseJSONResponseFields(responseText string, cfg config.ProcessingConfig) (map[string]any, error) {
	if !requiresJSONOutput(cfg) {
		return nil, nil
	}

	if !strictOutputEnabled(cfg) {
		parsed, ok := extractLastJSON(responseText)
		if !ok {
			return nil, nil
		}
		return parsed, nil
	}

	parsed, err := extractStrictJSON(responseText, cfg.Thinking)
	if err != nil {
		return nil, err
	}

	if err := validateResponseSchema(parsed, cfg.EffectiveLLMResponseSchema()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (a *App) repairStructuredResponse(ctx context.Context, gen Generator, raw string) (string, error) {
	schema := a.cfg.Processing.EffectiveLLMResponseSchema()
	return a.generateWithRetry(
		ctx, gen,
		"You are a strict JSON formatter. Always respond with a single valid JSON object — no markdown, no code fences, no explanation.",
		prompt.RepairPrompt(schema, raw),
	)
}

func extractStrictJSON(responseText string, thinking bool) (map[string]any, error) {
	trimmed := strings.TrimSpace(responseText)
	if trimmed == "" {
		return nil, fmt.Errorf("response is empty")
	}
	if !thinking {
		return parseStrictJSONObjectWithWrappers(trimmed)
	}

	const openTag = "<thinking>"
	const closeTag = "</thinking>"

	if strings.HasPrefix(trimmed, openTag) {
		closeIdx := strings.Index(trimmed, closeTag)
		if closeIdx < 0 {
			return nil, fmt.Errorf("thinking mode requires a closing %s tag", closeTag)
		}
		after := strings.TrimSpace(trimmed[closeIdx+len(closeTag):])
		if after == "" {
			return nil, fmt.Errorf("missing final JSON object after %s", closeTag)
		}
		return parseStrictJSONObjectWithWrappers(after)
	}
	// Some providers do not emit visible <thinking> blocks even when asked.
	// Accept a direct final JSON object (or common wrappers) in that case.
	return parseStrictJSONObjectWithWrappers(trimmed)
}

func parseStrictJSONObject(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("response must contain only one JSON object")
	}
	return out, nil
}

func parseStrictJSONObjectWithWrappers(s string) (map[string]any, error) {
	if out, err := parseStrictJSONObject(strings.TrimSpace(s)); err == nil {
		return out, nil
	}
	if unwrapped, ok := unwrapMarkdownCodeFence(s); ok {
		if out, err := parseStrictJSONObject(unwrapped); err == nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("invalid JSON object")
}

func unwrapMarkdownCodeFence(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return "", false
	}
	nl := strings.IndexByte(trimmed, '\n')
	if nl < 0 {
		return "", false
	}
	body := strings.TrimSpace(trimmed[nl+1:])
	if !strings.HasSuffix(body, "```") {
		return "", false
	}
	body = strings.TrimSpace(strings.TrimSuffix(body, "```"))
	if body == "" {
		return "", false
	}
	return body, true
}

func parseStrictJSONArray(s string) ([]json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	var out []json.RawMessage
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid JSON array: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("response must contain only one JSON array")
	}
	return out, nil
}

func validateResponseSchema(parsed map[string]any, schema map[string]string) error {
	if len(schema) == 0 {
		return nil
	}

	var missing []string
	for k := range schema {
		if _, ok := parsed[k]; !ok {
			missing = append(missing, k)
		}
	}
	var extra []string
	for k := range parsed {
		if _, ok := schema[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		slices.Sort(missing)
		slices.Sort(extra)
		parts := make([]string, 0, 2)
		if len(missing) > 0 {
			parts = append(parts, "missing: "+strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			parts = append(parts, "extra: "+strings.Join(extra, ", "))
		}
		return fmt.Errorf("response JSON keys do not match response_schema (%s)", strings.Join(parts, "; "))
	}

	for _, k := range slices.Sorted(maps.Keys(schema)) {
		if err := validateSchemaValue(k, parsed[k], schema[k]); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaValue(field string, value any, hint string) error {
	kind, enumValues := inferSchemaConstraint(hint)
	switch kind {
	case "enum":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("field %q must be a string enum (%s)", field, strings.Join(enumValues, "|"))
		}
		for _, allowed := range enumValues {
			if s == allowed {
				return nil
			}
		}
		return fmt.Errorf("field %q must be one of %s", field, strings.Join(enumValues, "|"))
	case "bool":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("field %q must be boolean", field)
		}
	case "int":
		if !isIntegerValue(value) {
			return fmt.Errorf("field %q must be integer", field)
		}
	case "number":
		if !isNumberValue(value) {
			return fmt.Errorf("field %q must be number", field)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("field %q must be string", field)
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("field %q must be array", field)
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("field %q must be object", field)
		}
	}
	return nil
}

func inferSchemaConstraint(hint string) (kind string, enumValues []string) {
	trimmed := strings.TrimSpace(hint)
	if trimmed == "" {
		return "any", nil
	}
	if values, ok := parseEnumValues(trimmed); ok {
		return "enum", values
	}

	lower := strings.ToLower(trimmed)
	tokens := splitHintTokens(lower)

	containsToken := func(target string) bool {
		for _, t := range tokens {
			if t == target {
				return true
			}
		}
		return false
	}

	switch {
	case containsToken("bool"), containsToken("boolean"):
		return "bool", nil
	case containsToken("int"), containsToken("integer"), containsToken("int32"), containsToken("int64"):
		return "int", nil
	case containsToken("number"), containsToken("float"), containsToken("double"), containsToken("decimal"):
		return "number", nil
	case containsToken("string"), containsToken("str"), containsToken("text"):
		return "string", nil
	case containsToken("array"), containsToken("list"), containsToken("slice"):
		return "array", nil
	case containsToken("object"), containsToken("map"), containsToken("json"):
		return "object", nil
	default:
		return "any", nil
	}
}

func splitHintTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func parseEnumValues(hint string) ([]string, bool) {
	if strings.Contains(hint, "|") {
		values := splitAndNormalize(hint, "|")
		if len(values) >= 2 {
			return values, true
		}
	}

	lower := strings.ToLower(hint)
	idx := strings.Index(lower, "one of")
	if idx < 0 {
		return nil, false
	}
	rest := strings.TrimSpace(hint[idx+len("one of"):])
	rest = strings.TrimLeft(rest, ": ")
	if rest == "" {
		return nil, false
	}
	switch {
	case strings.Contains(rest, ","):
		if values := splitAndNormalize(rest, ","); len(values) >= 2 {
			return values, true
		}
	case strings.Contains(rest, "|"):
		if values := splitAndNormalize(rest, "|"); len(values) >= 2 {
			return values, true
		}
	case strings.Contains(rest, "/"):
		if values := splitAndNormalize(rest, "/"); len(values) >= 2 {
			return values, true
		}
	}
	return nil, false
}

func splitAndNormalize(raw string, sep string) []string {
	parts := strings.Split(raw, sep)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(strings.Trim(p, `"'`))
		if v == "" {
			continue
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func isIntegerValue(v any) bool {
	switch n := v.(type) {
	case int, int8, int16, int32, int64:
		return true
	case uint, uint8, uint16, uint32, uint64:
		return true
	case float64:
		return math.Trunc(n) == n
	case json.Number:
		if _, err := n.Int64(); err == nil {
			return true
		}
		f, err := n.Float64()
		return err == nil && math.Trunc(f) == f
	default:
		return false
	}
}

func isNumberValue(v any) bool {
	switch n := v.(type) {
	case int, int8, int16, int32, int64:
		return true
	case uint, uint8, uint16, uint32, uint64:
		return true
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		_, err := n.Float64()
		return err == nil
	default:
		return false
	}
}

// applyOutputFields restricts rec to only the fields listed in fields.
// If fields is empty the record is returned unchanged.
func applyOutputFields(rec map[string]any, fields []string) map[string]any {
	if len(fields) == 0 {
		return rec
	}
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := rec[f]; ok {
			out[f] = v
		}
	}
	return out
}

func (a *App) processRecordStream(
	ctx context.Context,
	gen Generator,
	pb *prompt.Builder,
	activeTools []tools.Tool,
	reader input.Reader,
	workers int,
	rateLimiter <-chan time.Time,
	emit func(index int, record map[string]any) error,
) ([]map[string]any, error) {
	batchSize := a.cfg.Processing.BatchSize
	if batchSize > 1 {
		return a.processRecordStreamBatch(ctx, gen, pb, activeTools, reader, workers, batchSize, rateLimiter, emit)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan recordJob, workers*2)
	producerErr := make(chan error, 1)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(jobs)
		idx := 0
		for {
			rec, err := reader.Next(ctx)
			if err != nil {
				if err != io.EOF {
					producerErr <- err
				}
				return
			}
			select {
			case jobs <- recordJob{idx: idx, rec: rec}:
				idx++
			case <-ctx.Done():
				return
			}
		}
	}()

	results, err := a.processJobs(ctx, gen, pb, activeTools, jobs, 0, workers, rateLimiter, false, emit)
	<-producerDone
	select {
	case readErr := <-producerErr:
		if readErr != nil {
			return nil, readErr
		}
	default:
	}
	if err != nil {
		return nil, err
	}
	return results, nil
}

// processRecordStreamBatch handles the batch-mode variant of processRecordStream.
// Records are grouped into slices of batchSize, serialised as a JSON array, and
// sent to the LLM in a single request. The LLM is asked to return a JSON array
// of responses in the same order. Responses are mapped back to the originating
// records.
func (a *App) processRecordStreamBatch(
	ctx context.Context,
	gen Generator,
	pb *prompt.Builder,
	_ []tools.Tool, // tool-calling is not supported in batch mode
	reader input.Reader,
	workers int,
	batchSize int,
	rateLimiter <-chan time.Time,
	emit func(index int, record map[string]any) error,
) ([]map[string]any, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type batchJob struct {
		startIdx int
		recs     []input.Record
	}

	jobs := make(chan batchJob, workers*2)
	producerErr := make(chan error, 1)
	producerDone := make(chan struct{})

	go func() {
		defer close(producerDone)
		defer close(jobs)
		idx := 0
		for {
			batch := make([]input.Record, 0, batchSize)
			for len(batch) < batchSize {
				rec, err := reader.Next(ctx)
				if err != nil {
					if err != io.EOF {
						producerErr <- err
					}
					break
				}
				batch = append(batch, rec)
			}
			if len(batch) == 0 {
				return
			}
			select {
			case jobs <- batchJob{startIdx: idx, recs: batch}:
				idx += len(batch)
			case <-ctx.Done():
				return
			}
			if len(batch) < batchSize {
				return // last partial batch was sent
			}
		}
	}()

	type batchResult struct {
		startIdx int
		recs     []input.Record
		outRecs  []map[string]any
	}

	resultCh := make(chan batchResult, workers*2)

	var mu sync.Mutex
	var firstErr error
	var processed int64

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releaseWorker, err := a.acquireWorkerSlot(ctx)
			if err != nil {
				mu.Lock()
				if firstErr == nil && !errors.Is(err, context.Canceled) {
					firstErr = fmt.Errorf("acquire worker slot: %w", err)
				}
				mu.Unlock()
				return
			}
			defer releaseWorker()
			for bj := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if rateLimiter != nil {
					select {
					case <-ctx.Done():
						return
					case <-rateLimiter:
					}
				}

				outRecs, err := a.processBatch(ctx, gen, pb, bj.startIdx, bj.recs)
				if err != nil {
					if !a.cfg.Processing.ContinueOnError {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("llm batch call for records %d-%d: %w", bj.startIdx, bj.startIdx+len(bj.recs)-1, err)
							cancel()
						}
						mu.Unlock()
						return
					}
					a.logger.Error("llm batch call failed", "startIndex", bj.startIdx, "count", len(bj.recs), "error", err)
					// emit empty responses for failed batch records
					out := make([]map[string]any, len(bj.recs))
					for i, rec := range bj.recs {
						o := map[string]any{}
						if a.cfg.Processing.IncludeInputInOutput {
							for k, v := range rec {
								o[k] = v
							}
						}
						storeResponseField(o, a.cfg.Processing, "", nil)
						out[i] = o
					}
					outRecs = out
				}
				resultCh <- batchResult{startIdx: bj.startIdx, recs: bj.recs, outRecs: outRecs}
				cur := int(atomic.AddInt64(&processed, int64(len(bj.recs))))
				if a.progressFunc != nil {
					a.progressFunc(cur, 0)
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	ordered := map[int]map[string]any{}
	nextEmit := 0
	var deliverErr error
	var results []map[string]any

	emitReady := func() error {
		for {
			rec, ok := ordered[nextEmit]
			if !ok {
				return nil
			}
			if emit != nil {
				if err := emit(nextEmit, rec); err != nil {
					return err
				}
			}
			results = append(results, rec)
			if a.resultFunc != nil {
				preview := make(map[string]any, len(rec))
				for k, v := range rec {
					preview[k] = v
				}
				a.resultFunc(nextEmit, 0, preview)
			}
			delete(ordered, nextEmit)
			nextEmit++
		}
	}

	for br := range resultCh {
		for i, outRec := range br.outRecs {
			ordered[br.startIdx+i] = outRec
		}
		if deliverErr == nil {
			if err := emitReady(); err != nil {
				deliverErr = err
				cancel()
			}
		}
	}

	<-producerDone
	select {
	case readErr := <-producerErr:
		if readErr != nil {
			return nil, readErr
		}
	default:
	}
	if deliverErr != nil {
		return nil, deliverErr
	}
	mu.Lock()
	err := firstErr
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	return results, nil
}

// processBatch sends a slice of records to the LLM as JSON and parses the JSON
// array response back into individual output records.
func (a *App) processBatch(
	ctx context.Context,
	gen Generator,
	pb *prompt.Builder,
	startIdx int,
	recs []input.Record,
) ([]map[string]any, error) {
	// Build per-record prompt fragments.
	rendered := make([]string, len(recs))
	for i, rec := range recs {
		r, err := pb.BuildRaw(rec)
		if err != nil {
			return nil, fmt.Errorf("build prompt for record %d: %w", startIdx+i, err)
		}
		rendered[i] = r
	}

	// Serialise JSON payloads for the LLM.
	payload := make([]json.RawMessage, len(rendered))
	for i, r := range rendered {
		trimmed := strings.TrimSpace(r)
		if !json.Valid([]byte(trimmed)) {
			return nil, fmt.Errorf("input_template for record %d did not render valid JSON", startIdx+i)
		}
		payload[i] = json.RawMessage(trimmed)
	}
	recsJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal batch records: %w", err)
	}

	var userPrompt strings.Builder
	strictOutput := strictOutputEnabled(a.cfg.Processing)
	schema := a.cfg.Processing.EffectiveLLMResponseSchema()
	userPrompt.WriteString(fmt.Sprintf(
		"Process the following %d records. "+
			"Return ONLY a valid JSON array containing exactly %d JSON objects, one per record, in the same order.\n",
		len(recs), len(recs),
	))
	if len(schema) > 0 {
		userPrompt.WriteString("Each object must contain exactly these fields:\n")
		for _, k := range slices.Sorted(maps.Keys(schema)) {
			userPrompt.WriteString(fmt.Sprintf("- %s: %s\n", k, schema[k]))
		}
	}
	if a.cfg.Processing.Thinking {
		userPrompt.WriteString("Do not include reasoning text. Return only the JSON array.\n")
	}
	userPrompt.WriteString("\n")
	userPrompt.Write(recsJSON)
	if post := pb.PostPrompt(); post != "" {
		userPrompt.WriteString("\n\n")
		userPrompt.WriteString(post)
	}

	systemPrompt := pb.SystemPrompt()
	userPromptText := userPrompt.String()
	a.logger.Info("sending batch to LLM", "startIndex", startIdx, "count", len(recs))
	responseText, err := a.generateWithRetry(ctx, gen, systemPrompt, userPromptText)
	if err != nil {
		return nil, formatLLMErrorWithIO(
			fmt.Sprintf("llm batch call for records %d-%d", startIdx, startIdx+len(recs)-1),
			err,
			systemPrompt,
			userPromptText,
			responseText,
		)
	}

	// Parse JSON array response.
	trimmed := strings.TrimSpace(responseText)
	var responses []json.RawMessage
	if strictOutput {
		responses, err = parseStrictJSONArray(trimmed)
		if err != nil {
			return nil, formatLLMErrorWithIO(
				fmt.Sprintf("batch response must be exactly one JSON array for records %d-%d", startIdx, startIdx+len(recs)-1),
				err,
				systemPrompt,
				userPromptText,
				responseText,
			)
		}
		if len(responses) != len(recs) {
			return nil, formatLLMErrorWithIO(
				fmt.Sprintf("batch response length mismatch: expected %d objects, got %d", len(recs), len(responses)),
				fmt.Errorf("wrong number of JSON objects in batch response"),
				systemPrompt,
				userPromptText,
				responseText,
			)
		}
	} else {
		if idx := strings.Index(trimmed, "["); idx >= 0 {
			trimmed = trimmed[idx:]
		}
		if idx := strings.LastIndex(trimmed, "]"); idx >= 0 {
			trimmed = trimmed[:idx+1]
		}
	}

	if !strictOutput {
		if parseErr := json.Unmarshal([]byte(trimmed), &responses); parseErr != nil {
			a.logger.Warn("batch response is not a JSON array; assigning raw text to all records", "startIndex", startIdx, "error", parseErr)
			// Fallback: assign entire response to every record.
			outRecs := make([]map[string]any, len(recs))
			for i, rec := range recs {
				o := map[string]any{}
				if a.cfg.Processing.IncludeInputInOutput {
					for k, v := range rec {
						o[k] = v
					}
				}
				storeResponseField(o, a.cfg.Processing, responseText, nil)
				outRecs[i] = o
			}
			for i := range outRecs {
				outRecs[i] = applyOutputFields(outRecs[i], a.cfg.Processing.OutputFields)
			}
			return outRecs, nil
		}
	}

	outRecs := make([]map[string]any, len(recs))
	for i, rec := range recs {
		o := map[string]any{}
		if a.cfg.Processing.IncludeInputInOutput {
			for k, v := range rec {
				o[k] = v
			}
		}
		if i < len(responses) {
			if strictOutput {
				obj, parseErr := parseStrictJSONObject(string(responses[i]))
				if parseErr != nil {
					return nil, formatLLMErrorWithIO(
						fmt.Sprintf("batch response item %d is not a JSON object", startIdx+i),
						parseErr,
						systemPrompt,
						userPromptText,
						responseText,
					)
				}
				if schemaErr := validateResponseSchema(obj, schema); schemaErr != nil {
					return nil, formatLLMErrorWithIO(
						fmt.Sprintf("batch response item %d does not match response_schema", startIdx+i),
						schemaErr,
						systemPrompt,
						userPromptText,
						responseText,
					)
				}
				storeResponseField(o, a.cfg.Processing, string(responses[i]), obj)
				for k, v := range obj {
					o[k] = v
				}
				outRecs[i] = o
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal(responses[i], &obj); err == nil {
				storeResponseField(o, a.cfg.Processing, string(responses[i]), obj)
				for k, v := range obj {
					o[k] = v
				}
			} else {
				var s string
				if err := json.Unmarshal(responses[i], &s); err == nil {
					storeResponseField(o, a.cfg.Processing, s, nil)
				} else {
					storeResponseField(o, a.cfg.Processing, string(responses[i]), nil)
				}
			}
		} else {
			storeResponseField(o, a.cfg.Processing, "", nil)
		}
		outRecs[i] = o
	}

	for i := range outRecs {
		outRecs[i] = applyOutputFields(outRecs[i], a.cfg.Processing.OutputFields)
	}

	// Log individual rendered prompts for diagnostics but don't fail on them.
	_ = rendered

	return outRecs, nil
}

func (a *App) processJobs(
	ctx context.Context,
	gen Generator,
	pb *prompt.Builder,
	activeTools []tools.Tool,
	jobs <-chan recordJob,
	total int,
	workers int,
	rateLimiter <-chan time.Time,
	collect bool,
	emit func(index int, record map[string]any) error,
) ([]map[string]any, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var processed int64
	resultCh := make(chan indexedResult, workers*2)

	var mu sync.Mutex
	var firstErr error

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releaseWorker, err := a.acquireWorkerSlot(ctx)
			if err != nil {
				mu.Lock()
				if firstErr == nil && !errors.Is(err, context.Canceled) {
					firstErr = fmt.Errorf("acquire worker slot: %w", err)
				}
				mu.Unlock()
				return
			}
			defer releaseWorker()
			for j := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if rateLimiter != nil {
					select {
					case <-ctx.Done():
						return
					case <-rateLimiter:
					}
				}

				// Enrichment step (non-agentic web crawl) before prompt building.
				rec := j.rec
				if a.cfg.Enrich.Enabled && a.cfg.Enrich.Column != "" {
					enricher := enrich.New(a.cfg.Enrich)
					if enriched, enrichErr := enricher.Enrich(ctx, rec); enrichErr != nil {
						a.logger.Warn("enrich step failed", "index", j.idx, "error", enrichErr)
					} else {
						rec = enriched
					}
				}

				userPrompt, err := pb.Build(rec)
				if err != nil {
					if !a.cfg.Processing.ContinueOnError {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("build prompt for record %d: %w", j.idx, err)
							cancel()
						}
						mu.Unlock()
						return
					}
					a.logger.Error("build prompt failed", "index", j.idx, "error", err)
					continue
				}
				if instr := prompt.FormatInstructions(a.cfg.Processing); instr != "" {
					userPrompt = userPrompt + "\n\n" + instr
				}
				systemPrompt := pb.SystemPrompt()
				if sysNote := prompt.FormatSystemNote(a.cfg.Processing); sysNote != "" {
					if strings.TrimSpace(systemPrompt) != "" {
						systemPrompt = strings.TrimSpace(systemPrompt) + "\n\n" + sysNote
					} else {
						systemPrompt = sysNote
					}
				}

				var responseText string
				if len(activeTools) > 0 {
					responseText, err = a.runAgentic(ctx, gen, systemPrompt, userPrompt, activeTools, j.idx)
				} else {
					responseText, err = a.generateWithRetry(ctx, gen, systemPrompt, userPrompt)
				}
				if err != nil {
					errWithIO := formatLLMErrorWithIO(
						fmt.Sprintf("llm call for record %d", j.idx),
						err,
						systemPrompt,
						userPrompt,
						responseText,
					)
					if !a.cfg.Processing.ContinueOnError {
						mu.Lock()
						if firstErr == nil {
							firstErr = errWithIO
							cancel()
						}
						mu.Unlock()
						return
					}
					a.logLLMErrorWithIO("llm call failed", j.idx, err, systemPrompt, userPrompt, responseText)
					continue
				}

				outRec := map[string]any{}
				if a.cfg.Processing.IncludeInputInOutput {
					if a.cfg.Processing.KeyColumn != "" {
						if v, ok := rec[a.cfg.Processing.KeyColumn]; ok {
							outRec[a.cfg.Processing.KeyColumn] = v
						}
					} else {
						for k, v := range rec {
							outRec[k] = v
						}
					}
				}
				var parsed map[string]any
				if requiresJSONOutput(a.cfg.Processing) {
					var parseErr error
					parsed, parseErr = parseJSONResponseFields(responseText, a.cfg.Processing)
					if parseErr != nil && strictOutputEnabled(a.cfg.Processing) {
						if repaired, repairErr := a.repairStructuredResponse(ctx, gen, responseText); repairErr == nil {
							if reparsed, reparsedErr := parseJSONResponseFields(repaired, a.cfg.Processing); reparsedErr == nil {
								responseText = repaired
								parsed = reparsed
								parseErr = nil
								a.logger.Warn("structured response repaired", "index", j.idx)
							}
						}
					}
					if parseErr != nil {
						errWithIO := formatLLMErrorWithIO(
							fmt.Sprintf("invalid structured response for record %d", j.idx),
							parseErr,
							systemPrompt,
							userPrompt,
							responseText,
						)
						if !a.cfg.Processing.ContinueOnError {
							mu.Lock()
							if firstErr == nil {
								firstErr = errWithIO
								cancel()
							}
							mu.Unlock()
							return
						}
						a.logLLMErrorWithIO("invalid structured response", j.idx, parseErr, systemPrompt, userPrompt, responseText)
						continue
					}
					for k, v := range parsed {
						if _, exists := outRec[k]; exists && k != a.cfg.Processing.ResponseField {
							a.logger.Warn("parse_json_response: JSON key conflicts with existing field, overwriting", "key", k, "index", j.idx)
						}
						outRec[k] = v
					}
				}
				storeResponseField(outRec, a.cfg.Processing, responseText, parsed)
				resultCh <- indexedResult{idx: j.idx, rec: applyOutputFields(outRec, a.cfg.Processing.OutputFields)}
				cur := int(atomic.AddInt64(&processed, 1))
				if a.progressFunc != nil {
					a.progressFunc(cur, total)
				}
				if a.resultFunc != nil {
					preview := make(map[string]any, len(outRec))
					for k, v := range outRec {
						preview[k] = v
					}
					a.resultFunc(j.idx, total, preview)
				}
				if total > 0 {
					if cur == 1 || cur == total || cur%10 == 0 {
						a.logger.Info("processing progress", "current", cur, "total", total)
					}
				} else if cur == 1 || cur%10 == 0 {
					a.logger.Info("processing progress", "current", cur)
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	ordered := map[int]map[string]any{}
	nextEmit := 0
	deliverErr := error(nil)
	results := make([]map[string]any, 0)
	emitReady := func() error {
		for {
			rec, ok := ordered[nextEmit]
			if !ok {
				return nil
			}
			if emit != nil {
				if err := emit(nextEmit, rec); err != nil {
					return err
				}
			}
			if collect {
				results = append(results, rec)
			}
			delete(ordered, nextEmit)
			nextEmit++
		}
	}

	for ir := range resultCh {
		ordered[ir.idx] = ir.rec
		if deliverErr == nil {
			if err := emitReady(); err != nil {
				deliverErr = err
				cancel()
			}
		}
	}
	if deliverErr != nil {
		return nil, deliverErr
	}

	mu.Lock()
	err := firstErr
	mu.Unlock()
	if err != nil {
		return nil, err
	}

	return results, nil
}

// runAgentic executes the agentic tool-calling loop for a single record.
// It requires the generator to implement llm.AgentGenerator; if it does not,
// it falls back to a plain Generate call.
func (a *App) runAgentic(
	ctx context.Context,
	gen Generator,
	systemPrompt, userPrompt string,
	activeTools []tools.Tool,
	recIdx int,
) (string, error) {
	ag, ok := gen.(llm.AgentGenerator)
	if !ok {
		a.logger.Warn("generator does not support tool calling, falling back to standard generate",
			"index", recIdx)
		return a.generateWithRetry(ctx, gen, systemPrompt, userPrompt)
	}

	// Build tool definitions for the LLM.
	toolDefs := make([]llm.ToolDef, len(activeTools))
	for i, t := range activeTools {
		// Parameters is a JSON Schema — unmarshal to any so it serializes
		// without double-encoding.
		var params any
		if err := jsonUnmarshalParams(t.Parameters, &params); err != nil {
			params = string(t.Parameters)
		}
		toolDefs[i] = llm.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		}
	}

	// Index tools by name for fast lookup.
	toolMap := make(map[string]tools.Tool, len(activeTools))
	for _, t := range activeTools {
		toolMap[t.Name] = t
	}

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	maxRounds := a.cfg.Tools.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 5
	}

	for round := 0; round < maxRounds; round++ {
		resp, err := a.agentStepWithRetry(ctx, ag, messages, toolDefs)
		if err != nil {
			return "", err
		}

		// No tool calls → final answer.
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}

		a.logger.Info("tool calls requested", "index", recIdx, "round", round+1, "count", len(resp.ToolCalls))

		// Append the assistant's tool-call message.
		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool and append results.
		for _, tc := range resp.ToolCalls {
			result, toolErr := a.executeTool(ctx, toolMap, tc, recIdx)
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
			})
			if toolErr != nil {
				a.logger.Warn("tool execution error", "tool", tc.Name, "index", recIdx, "error", toolErr)
			}
		}
	}

	return "", fmt.Errorf("exceeded maximum tool-calling rounds (%d) for record %d", maxRounds, recIdx)
}

// executeTool runs a single tool call and returns the string result.
// Errors are embedded in the result string so the LLM can react to them.
func (a *App) executeTool(ctx context.Context, toolMap map[string]tools.Tool, tc llm.ToolCall, recIdx int) (string, error) {
	t, ok := toolMap[tc.Name]
	if !ok {
		msg := fmt.Sprintf("unknown tool: %s", tc.Name)
		return msg, fmt.Errorf("%s", msg)
	}
	a.logger.Info("executing tool", "tool", tc.Name, "index", recIdx, "args", tc.Args)
	result, err := t.Execute(ctx, tc.Args)
	if err != nil {
		msg := fmt.Sprintf("tool %s error: %s", tc.Name, err.Error())
		return msg, err
	}
	a.logger.Info("tool result", "tool", tc.Name, "index", recIdx, "result_len", len(result))
	return result, nil
}

// agentStepWithRetry calls GenerateAgent with retry logic.
func (a *App) agentStepWithRetry(ctx context.Context, ag llm.AgentGenerator, messages []llm.Message, tools []llm.ToolDef) (*llm.AgentResponse, error) {
	maxRetries := a.cfg.Processing.MaxRetries
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := ag.GenerateAgent(ctx, llm.AgentRequest{Messages: messages, Tools: tools})
		if err == nil {
			return resp, nil
		}
		lastErr = err
		wait := llm.RetryDelay(err, attempt)
		if llm.IsRateLimit(err) {
			a.logger.Warn("agent step rate-limited", "attempt", attempt, "wait", wait, "error", err)
		} else {
			a.logger.Warn("agent step failed", "attempt", attempt, "wait", wait, "error", err)
		}
		if attempt == maxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
}

func (a *App) generateWithRetry(ctx context.Context, gen Generator, systemPrompt, userPrompt string) (string, error) {
	maxRetries := a.cfg.Processing.MaxRetries
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		text, err := gen.Generate(ctx, systemPrompt, userPrompt)
		if err == nil {
			return text, nil
		}
		lastErr = err
		wait := llm.RetryDelay(err, attempt)
		if llm.IsRateLimit(err) {
			a.logger.Warn("llm request rate-limited", "attempt", attempt, "wait", wait, "error", err)
		} else {
			a.logger.Warn("llm request failed", "attempt", attempt, "wait", wait, "error", err)
		}
		if attempt == maxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	return "", lastErr
}

// dryRunGenerator returns a placeholder JSON response instead of calling an LLM.
type dryRunGenerator struct {
	responseField string
	schema        map[string]string
}

func (d *dryRunGenerator) Generate(_ context.Context, _, _ string) (string, error) {
	out := map[string]any{}
	for k, hint := range d.schema {
		out[k] = placeholderValueForHint(hint)
	}
	if len(out) == 0 {
		key := strings.TrimSpace(d.responseField)
		if key == "" {
			key = "response"
		}
		out[key] = "[dry-run]"
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func placeholderValueForHint(hint string) any {
	h := strings.ToLower(strings.TrimSpace(hint))
	switch {
	case strings.Contains(h, "bool"):
		return false
	case strings.Contains(h, "int"), strings.Contains(h, "integer"):
		return 0
	case strings.Contains(h, "float"), strings.Contains(h, "number"), strings.Contains(h, "double"):
		return 0
	}
	return "[dry-run]"
}

// jsonUnmarshalParams parses the raw JSON bytes of a tool's parameter schema
// into dest (usually *any).
func jsonUnmarshalParams(raw []byte, dest any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dest)
}
