package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/input"
	"github.com/SimonWaldherr/llmflow/internal/llm"
	"github.com/SimonWaldherr/llmflow/internal/output"
	"github.com/SimonWaldherr/llmflow/internal/prompt"
)

// Generator is the interface used to call an LLM, allowing injection of test fakes.
type Generator interface {
	Generate(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}
type App struct {
	cfg    config.Config
	logger *slog.Logger
	dryRun bool
}

func New(cfg config.Config, logger *slog.Logger) *App {
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

	records, err := reader.ReadAll(ctx)
	if err != nil {
		return err
	}
	a.logger.Info("loaded input records", "count", len(records))

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

	results, err := a.processRecords(ctx, gen, pb, records, workers, rateLimiter)
	if err != nil {
		return err
	}

	if err := writer.WriteAll(ctx, results); err != nil {
		return err
	}
	a.logger.Info("wrote output records", "count", len(results))
	return nil
}

type indexedResult struct {
	idx int
	rec map[string]any
}

func (a *App) processRecords(
	ctx context.Context,
	gen Generator,
	pb *prompt.Builder,
	records []input.Record,
	workers int,
	rateLimiter <-chan time.Time,
) ([]map[string]any, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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
	errCh := make(chan error, 1)

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

				responseText, err := a.generateWithRetry(ctx, gen, pb.SystemPrompt(), userPrompt)
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
				outRec[a.cfg.Processing.ResponseField] = responseText
				resultCh <- indexedResult{idx: j.idx, rec: outRec}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
		close(errCh)
	}()

	ordered := make([]map[string]any, len(records))
	for ir := range resultCh {
		ordered[ir.idx] = ir.rec
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

func (a *App) generateWithRetry(ctx context.Context, gen Generator, systemPrompt, userPrompt string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		text, err := gen.Generate(ctx, systemPrompt, userPrompt)
		if err == nil {
			return text, nil
		}
		lastErr = err
		a.logger.Warn("llm request failed", "attempt", attempt, "error", err)
		if attempt == 3 {
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
