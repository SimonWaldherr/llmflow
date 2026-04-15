package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
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
type App struct {
	cfg          config.Config
	logger       *slog.Logger
	dryRun       bool
	progressFunc func(current, total int)
	resultFunc   func(index, total int, record map[string]any)
}

// WithProgressFunc sets a callback invoked after each record is processed.
func (a *App) WithProgressFunc(f func(current, total int)) *App { a.progressFunc = f; return a }

// WithResultFunc sets a callback invoked for each successful output record.
func (a *App) WithResultFunc(f func(index, total int, record map[string]any)) *App {
	a.resultFunc = f
	return a
}

func New(cfg config.Config, logger *slog.Logger) *App {
	cfg.ApplyDefaults()
	return &App{cfg: cfg, logger: logger, dryRun: cfg.Processing.DryRun}
}

// WithDryRun overrides the dry-run flag set in the config (useful for CLI flag).
func (a *App) WithDryRun(v bool) *App { a.dryRun = v; return a }

func (a *App) Run(ctx context.Context) error {
	var gen Generator

	if a.dryRun {
		a.logger.Info("dry-run mode enabled — no LLM calls will be made")
		gen = &dryRunGenerator{}
	} else {
		apiKey, err := a.cfg.APIKey()
		if err != nil {
			return err
		}
		gen = llm.New(a.cfg.API, apiKey)
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

	workers := a.cfg.Processing.Workers
	if workers <= 0 {
		workers = 1
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
			columns := buildOutputColumns([]input.Record{record}, a.cfg.Processing.ResponseField, a.cfg.Processing.IncludeInputInOutput)
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

func buildOutputColumns(records []input.Record, responseField string, includeInput bool) []string {
	if !includeInput {
		if strings.TrimSpace(responseField) == "" {
			responseField = "response"
		}
		return []string{responseField}
	}
	set := map[string]struct{}{}
	for _, rec := range records {
		for key := range rec {
			set[key] = struct{}{}
		}
	}
	if responseField == "" {
		responseField = "response"
	}
	set[responseField] = struct{}{}
	columns := make([]string, 0, len(set))
	for key := range set {
		columns = append(columns, key)
	}
	sort.Strings(columns)
	return columns
}

// buildTools constructs the slice of active Tool objects from the config.
func (a *App) buildTools() []tools.Tool {
	if !a.cfg.Tools.Enabled {
		return nil
	}
	var ts []tools.Tool
	if a.cfg.Tools.WebFetch {
		ts = append(ts, tools.NewWebFetchTool())
		a.logger.Info("tool enabled", "tool", "web_fetch")
	}
	if a.cfg.Tools.WebSearch {
		ts = append(ts, tools.NewWebSearchTool())
		a.logger.Info("tool enabled", "tool", "web_search")
	}
	if a.cfg.Tools.WebExtractLinks {
		ts = append(ts, tools.NewWebExtractLinksTool())
		a.logger.Info("tool enabled", "tool", "web_extract_links")
	}
	if a.cfg.Tools.CodeExecute {
		ts = append(ts, tools.NewCodeExecTool(tools.CodeExecConfig{
			Timeout:         a.cfg.Tools.Code.Timeout,
			MaxSourceBytes:  a.cfg.Tools.Code.MaxSourceBytes,
			ReadOnlyFS:      a.cfg.Tools.Code.ReadOnlyFS,
			ReadWhitelist:   a.cfg.Tools.Code.ReadWhitelist,
			HTTPGet:         a.cfg.Tools.Code.HTTPGet,
			HTTPTimeout:     a.cfg.Tools.Code.HTTPTimeout,
			HTTPMinInterval: a.cfg.Tools.Code.HTTPMinInterval,
		}))
		a.logger.Info("tool enabled", "tool", "code_execute")
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
		ts = append(ts, tools.NewSQLQueryTool(driver, dsn))
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

				userPrompt, err := pb.Build(j.rec)
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

				var responseText string
				if len(activeTools) > 0 {
					responseText, err = a.runAgentic(ctx, gen, pb.SystemPrompt(), userPrompt, activeTools, j.idx)
				} else {
					responseText, err = a.generateWithRetry(ctx, gen, pb.SystemPrompt(), userPrompt)
				}
				if err != nil {
					if !a.cfg.Processing.ContinueOnError {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("llm call for record %d: %w", j.idx, err)
							cancel()
						}
						mu.Unlock()
						return
					}
					a.logger.Error("llm call failed", "index", j.idx, "error", err)
					continue
				}

				outRec := map[string]any{}
				if a.cfg.Processing.IncludeInputInOutput {
					for k, v := range j.rec {
						outRec[k] = v
					}
				}
				if a.cfg.Processing.ParseJSONResponse {
					var parsed map[string]any
					trimmed := strings.TrimSpace(responseText)
					if idx := strings.Index(trimmed, "{"); idx >= 0 {
						trimmed = trimmed[idx:]
					}
					if idx := strings.LastIndex(trimmed, "}"); idx >= 0 && idx < len(trimmed)-1 {
						trimmed = trimmed[:idx+1]
					}
					if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
						for k, v := range parsed {
							if _, exists := outRec[k]; exists {
								a.logger.Warn("parse_json_response: JSON key conflicts with existing field, overwriting", "key", k, "index", j.idx)
							}
							outRec[k] = v
						}
					} else {
						outRec[a.cfg.Processing.ResponseField] = responseText
					}
				} else {
					outRec[a.cfg.Processing.ResponseField] = responseText
				}
				resultCh <- indexedResult{idx: j.idx, rec: outRec}
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

				userPrompt, err := pb.Build(j.rec)
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

				var responseText string
				if len(activeTools) > 0 {
					responseText, err = a.runAgentic(ctx, gen, pb.SystemPrompt(), userPrompt, activeTools, j.idx)
				} else {
					responseText, err = a.generateWithRetry(ctx, gen, pb.SystemPrompt(), userPrompt)
				}
				if err != nil {
					if !a.cfg.Processing.ContinueOnError {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("llm call for record %d: %w", j.idx, err)
							cancel()
						}
						mu.Unlock()
						return
					}
					a.logger.Error("llm call failed", "index", j.idx, "error", err)
					continue
				}

				outRec := map[string]any{}
				if a.cfg.Processing.IncludeInputInOutput {
					for k, v := range j.rec {
						outRec[k] = v
					}
				}
				if a.cfg.Processing.ParseJSONResponse {
					var parsed map[string]any
					trimmed := strings.TrimSpace(responseText)
					if idx := strings.Index(trimmed, "{"); idx >= 0 {
						trimmed = trimmed[idx:]
					}
					if idx := strings.LastIndex(trimmed, "}"); idx >= 0 && idx < len(trimmed)-1 {
						trimmed = trimmed[:idx+1]
					}
					if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
						for k, v := range parsed {
							if _, exists := outRec[k]; exists {
								a.logger.Warn("parse_json_response: JSON key conflicts with existing field, overwriting", "key", k, "index", j.idx)
							}
							outRec[k] = v
						}
					} else {
						outRec[a.cfg.Processing.ResponseField] = responseText
					}
				} else {
					outRec[a.cfg.Processing.ResponseField] = responseText
				}
				resultCh <- indexedResult{idx: j.idx, rec: outRec}
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
		a.logger.Warn("agent step failed", "attempt", attempt, "error", err)
		if attempt == maxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(llm.Backoff(attempt)):
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
		a.logger.Warn("llm request failed", "attempt", attempt, "error", err)
		if attempt == maxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(llm.Backoff(attempt)):
		}
	}
	return "", lastErr
}

// dryRunGenerator returns a placeholder instead of calling an LLM.
type dryRunGenerator struct{}

func (d *dryRunGenerator) Generate(_ context.Context, _, _ string) (string, error) {
	return "[dry-run]", nil
}

// jsonUnmarshalParams parses the raw JSON bytes of a tool's parameter schema
// into dest (usually *any).
func jsonUnmarshalParams(raw []byte, dest any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dest)
}
