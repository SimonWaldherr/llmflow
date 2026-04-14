// Package web provides an HTTP server with an embedded web UI for llmflow.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/app"
	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/llm"
	"gopkg.in/yaml.v3"
)

//go:embed static/*
var staticFS embed.FS

// Server holds the web UI HTTP server state.
type Server struct {
	logger    *slog.Logger
	addr      string
	dataDir   string
	mu        sync.Mutex
	jobs      []*JobStatus
	jobIDSeq  int
	wg        sync.WaitGroup  // tracks running job goroutines
	serverCtx context.Context // cancelled on graceful shutdown; used for job lifecycles
}

// JobProgress tracks how many records have been processed.
type JobProgress struct {
	Current int `json:"current"`
	Total   int `json:"total"`
}

// JobPreviewItem captures one completed output record for live preview.
type JobPreviewItem struct {
	Index  int            `json:"index"`
	Record map[string]any `json:"record"`
}

// JobStatus tracks a running or completed job.
type JobStatus struct {
	ID           int              `json:"id"`
	Name         string           `json:"name,omitempty"`
	Status       string           `json:"status"` // running | completed | failed | cancelled
	StartedAt    time.Time        `json:"started_at"`
	EndedAt      time.Time        `json:"ended_at,omitempty"`
	Error        string           `json:"error,omitempty"`
	Config       string           `json:"config"`
	Logs         []string         `json:"logs"`
	Preview      []JobPreviewItem `json:"preview,omitempty"`
	PreviewCount int              `json:"preview_count"`
	Archived     bool             `json:"archived"`
	Progress     JobProgress      `json:"progress"`
	// unexported – not serialised
	cancelFn context.CancelFunc
}

// NewServer creates a new web UI server.
func NewServer(addr string, logger *slog.Logger) *Server {
	dataDir := strings.TrimSpace(os.Getenv("LLMFLOW_DATA_DIR"))
	if dataDir == "" {
		dataDir = "data"
	}
	return &Server{
		addr:      addr,
		dataDir:   dataDir,
		logger:    logger,
		serverCtx: context.Background(), // replaced by Run(); guards against nil if handleRun is called early
	}
}

// authMiddleware enforces Bearer token authentication when LLMFLOW_WEB_TOKEN is set.
// Unauthenticated requests to /api/* paths are rejected with 401.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	token := strings.TrimSpace(os.Getenv("LLMFLOW_WEB_TOKEN"))
	if token == "" {
		return next // auth disabled
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			auth := r.Header.Get("Authorization")
			bearer, found := strings.CutPrefix(auth, "Bearer ")
			if !found || subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, apiResponse{Error: "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Run starts the HTTP server and blocks until ctx is cancelled, then shuts down
// gracefully (waiting for in-flight jobs to finish).
func (s *Server) Run(ctx context.Context) error {
	// Store the server context so job goroutines can be cancelled during shutdown.
	s.mu.Lock()
	s.serverCtx = ctx
	s.mu.Unlock()

	// Ensure the data directories exist.
	for _, sub := range []string{"input", "output"} {
		dir := filepath.Join(s.dataDir, sub)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create data dir %s: %w", dir, err)
		}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /api/validate", s.handleValidate)
	mux.HandleFunc("POST /api/run", s.handleRun)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/jobs/", s.handleGetJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/archive", s.handleArchiveJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.handleDeleteJob)
	mux.HandleFunc("POST /api/upload", s.handleUpload)
	mux.HandleFunc("GET /api/detect", s.handleDetect)
	mux.HandleFunc("POST /api/suggest", s.handleSuggest)
	mux.HandleFunc("GET /api/files", s.handleListFiles)
	mux.HandleFunc("DELETE /api/files/{dir}/{name}", s.handleDeleteFile)
	mux.HandleFunc("GET /api/files/download/{dir}/{name}", s.handleDownloadFile)
	mux.HandleFunc("GET /api/models", s.handleListModels)

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("embed fs: %w", err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	srv := &http.Server{
		Addr:           s.addr,
		Handler:        s.authMiddleware(mux),
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   5 * time.Minute,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 1 << 20, // 1 MiB
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("web UI listening", "addr", s.addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown: stop accepting new requests.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.logger.Warn("web server shutdown error", "error", err)
	}

	// Wait for all running jobs to finish.
	s.logger.Info("waiting for running jobs to complete")
	s.wg.Wait()
	return nil
}

// ListenAndServe starts the HTTP server without graceful shutdown (kept for
// backwards compatibility; prefer Run with a context).
func (s *Server) ListenAndServe() error {
	return s.Run(context.Background())
}

// handleHealth returns a simple liveness response.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

type validateRequest struct {
	Config string `json:"config"`
}

type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
		return
	}
	_, err := parseConfig(req.Config)
	if err != nil {
		writeJSON(w, http.StatusOK, apiResponse{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

type runRequest struct {
	Config string `json:"config"`
	DryRun bool   `json:"dry_run"`
	Name   string `json:"name"`
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
		return
	}

	cfg, err := parseConfig(req.Config)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	s.mu.Lock()
	s.jobIDSeq++
	job := &JobStatus{
		ID:        s.jobIDSeq,
		Name:      req.Name,
		Status:    "running",
		StartedAt: time.Now(),
		Config:    req.Config,
	}
	s.jobs = append(s.jobs, job)
	s.mu.Unlock()

	// Use the server context so jobs are cancelled during graceful shutdown,
	// not when the HTTP response is sent.
	s.mu.Lock()
	jobCtx, jobCancel := context.WithCancel(s.serverCtx)
	job.cancelFn = jobCancel
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer jobCancel() // ensure context is always released
		lc := &logCollector{job: job, mu: &s.mu}
		logger := slog.New(slog.NewJSONHandler(lc, &slog.HandlerOptions{Level: slog.LevelDebug}))

		const maxPreviewItems = 20

		// Progress callback – updates the job in a goroutine-safe way.
		progress := func(current, total int) {
			s.mu.Lock()
			job.Progress.Current = current
			job.Progress.Total = total
			s.mu.Unlock()
		}

		resultPreview := func(index, total int, record map[string]any) {
			s.mu.Lock()
			defer s.mu.Unlock()
			job.PreviewCount++
			item := JobPreviewItem{Index: index, Record: record}
			if len(job.Preview) >= maxPreviewItems {
				job.Preview = append(job.Preview[1:], item)
				return
			}
			job.Preview = append(job.Preview, item)
		}

		a := app.New(cfg, logger).WithDryRun(req.DryRun).WithProgressFunc(progress).WithResultFunc(resultPreview)
		runErr := a.Run(jobCtx)

		s.mu.Lock()
		defer s.mu.Unlock()
		job.EndedAt = time.Now()
		// Respect an explicit cancellation – don't overwrite the status set by handleCancelJob.
		if job.Status == "cancelled" {
			return
		}
		if runErr != nil {
			job.Status = "failed"
			job.Error = runErr.Error()
		} else {
			job.Status = "completed"
		}
	}()

	writeJSON(w, http.StatusAccepted, apiResponse{OK: true, Data: job})
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	showArchived := r.URL.Query().Get("archived") == "true"
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.jobs)
	start := 0
	if n > 100 {
		start = n - 100
	}
	result := make([]*JobStatus, 0, n-start)
	for i := n - 1; i >= start; i-- {
		j := s.jobs[i]
		if !showArchived && j.Archived {
			continue
		}
		result = append(result, j)
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: result})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "missing job id"})
		return
	}

	var id int
	if _, err := fmt.Sscan(parts[0], &id); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid job id"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: j})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
}

// parsePatternID extracts the numeric {id} path parameter from a ServeMux pattern route.
func parsePatternID(r *http.Request) (int, bool) {
	var id int
	_, err := fmt.Sscan(r.PathValue("id"), &id)
	return id, err == nil
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePatternID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid job id"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			if j.Status != "running" {
				writeJSON(w, http.StatusConflict, apiResponse{Error: "job is not running"})
				return
			}
			j.Status = "cancelled"
			j.EndedAt = time.Now()
			if j.cancelFn != nil {
				j.cancelFn()
			}
			writeJSON(w, http.StatusOK, apiResponse{OK: true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
}

func (s *Server) handleArchiveJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePatternID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid job id"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.Archived = true
			writeJSON(w, http.StatusOK, apiResponse{OK: true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePatternID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid job id"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.ID == id {
			if j.Status == "running" {
				writeJSON(w, http.StatusConflict, apiResponse{Error: "cannot delete a running job; cancel it first"})
				return
			}
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			writeJSON(w, http.StatusOK, apiResponse{OK: true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "parse form: " + err.Error()})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "missing file field"})
		return
	}
	defer file.Close()

	// Strip any directory components from the user-supplied filename to prevent
	// path traversal attacks (e.g. "../../etc/passwd" → "passwd").
	name := filepath.Base(header.Filename)
	if name == "." || name == string(filepath.Separator) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid filename"})
		return
	}

	uploadDir := filepath.Join(s.dataDir, "input")
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "create upload dir"})
		return
	}

	// If the file already exists, prefix with a nanosecond timestamp so uploads never overwrite each other.
	dst := filepath.Join(uploadDir, name)
	if _, statErr := os.Stat(dst); statErr == nil {
		name = fmt.Sprintf("%d-%s", time.Now().UnixNano(), name)
		dst = filepath.Join(uploadDir, name)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "create file"})
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, io.LimitReader(file, 32<<20)); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "write file"})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"path": dst, "name": name}})
}

// ─── /api/suggest ─────────────────────────────────────────────────────────────
// handleSuggest accepts a plain-text task description and an (optional) partial
// config YAML, calls an LLM to fill in / improve the config fields, and returns
// a SuggestResponse with pre-filled values that the frontend can apply to the form.

type suggestRequest struct {
	// Task is the free-text description of what the user wants to do.
	Task string `json:"task"`
	// Config is the current (possibly empty) YAML config to base suggestions on.
	Config string `json:"config"`
	// Provider / model / key let the UI pass a temporary LLM config that is used
	// only for this suggestion call, without having to run a full job.
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"api_key_env"`
	BaseURL   string `json:"base_url"`
	Timeout   string `json:"timeout"`
}

type suggestResponse struct {
	SystemPrompt  string `json:"system_prompt"`
	PrePrompt     string `json:"pre_prompt"`
	InputTemplate string `json:"input_template"`
	PostPrompt    string `json:"post_prompt"`
	JobName       string `json:"job_name"`
	ResponseField string `json:"response_field"`
}

const suggestSystemPrompt = `You are an expert llmflow configuration assistant.
llmflow is a batch-processing tool that reads records from a file (CSV/JSON/JSONL/XML),
sends each record to an LLM with a configurable prompt pipeline, and writes the responses
back to an output file.

The prompt pipeline per record is:
  system_prompt  → sent once as the LLM role / global instructions (NOT per record)
  pre_prompt     → optional text prepended before each record's rendered data
  input_template → Go template executed per record:
                   {{ toPrettyJSON .record }} renders the full record as JSON,
                   {{ .fieldName }} accesses a specific field.
  post_prompt    → optional instruction appended after each record (e.g. output format)
  response_field → the key in the output row where the LLM's answer is stored.

The user will describe a task. Your job is to fill in the five prompt fields and a
short job_name. Rules:
- system_prompt: set the LLM's persona and any global constraints.
- pre_prompt: frame the task or give context (optional – leave empty if not useful).
- input_template: almost always "{{ toPrettyJSON .record }}" unless the user mentions
  specific fields, in which case reference them by name.
- post_prompt: enforce the output format (JSON schema, plain text, etc.) – especially
  useful when structured output is needed.
- response_field: a short snake_case key name describing the output (e.g. "sentiment",
  "summary", "classification").
- job_name: ≤ 6 words describing what this job does.

Respond with ONLY a JSON object — no markdown, no explanation — following this schema:
{
  "system_prompt": "...",
  "pre_prompt": "...",
  "input_template": "...",
  "post_prompt": "...",
  "response_field": "...",
  "job_name": "..."
}`

func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	var req suggestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "task is required"})
		return
	}

	suggestTimeout, err := resolveSuggestTimeout(req.Timeout)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	// Build a minimal APIConfig from the quick-form state passed by the client.
	provider := req.Provider
	if provider == "" {
		provider = config.ProviderOpenAI
	}
	apiCfg := config.APIConfig{
		Provider:  provider,
		Model:     req.Model,
		APIKeyEnv: req.APIKeyEnv,
		BaseURL:   req.BaseURL,
		Timeout:   suggestTimeout,
	}
	apiCfg.ApplyProviderDefaults()

	apiKey, err := func() (string, error) {
		if apiCfg.APIKeyEnv == "" {
			return "", nil
		}
		v := strings.TrimSpace(os.Getenv(apiCfg.APIKeyEnv))
		return v, nil
	}()
	if err != nil || (apiCfg.APIKeyEnv != "" && apiKey == "") {
		writeJSON(w, http.StatusBadRequest, apiResponse{
			Error: fmt.Sprintf("API key env var %q is not set; set it on the server before using AI suggestions", apiCfg.APIKeyEnv),
		})
		return
	}

	userMsg := "Task description:\n" + strings.TrimSpace(req.Task)
	if cfg := strings.TrimSpace(req.Config); cfg != "" {
		userMsg += "\n\nCurrent config for reference:\n```yaml\n" + cfg + "\n```"
	}

	generateWithTimeout := func(timeout time.Duration) (string, error) {
		tmpCfg := apiCfg
		tmpCfg.Timeout = timeout
		gen := llm.New(tmpCfg, apiKey)
		ctx, cancel := context.WithTimeout(r.Context(), timeout+5*time.Second)
		defer cancel()
		return gen.Generate(ctx, suggestSystemPrompt, userMsg)
	}

	raw, err := generateWithTimeout(suggestTimeout)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		retryTimeout := suggestTimeout * 2
		if retryTimeout > 10*time.Minute {
			retryTimeout = 10 * time.Minute
		}
		if retryTimeout > suggestTimeout {
			raw, err = generateWithTimeout(retryTimeout)
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Error: fmt.Sprintf("LLM call failed after %s timeout budget: %s", suggestTimeout.String(), err.Error())})
		return
	}

	// Strip markdown code fences if the model wrapped the JSON anyway.
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[:idx+1]
	}

	var sr suggestResponse
	if err := json.Unmarshal([]byte(raw), &sr); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "could not parse LLM response as JSON: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: sr})
}

func resolveSuggestTimeout(raw string) (time.Duration, error) {
	// Default timeout for quick suggestions. Can be overridden server-side.
	timeout := 120 * time.Second
	if fromEnv := strings.TrimSpace(os.Getenv("LLMFLOW_WEB_SUGGEST_TIMEOUT")); fromEnv != "" {
		d, err := time.ParseDuration(fromEnv)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("invalid LLMFLOW_WEB_SUGGEST_TIMEOUT value %q", fromEnv)
		}
		timeout = d
	}

	if raw = strings.TrimSpace(raw); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("invalid suggest timeout %q (use duration like 60s or 5m)", raw)
		}
		timeout = d
	}

	if timeout < 10*time.Second {
		timeout = 10 * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}

	return timeout, nil
}

func parseConfig(yamlText string) (config.Config, error) {
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(yamlText), &cfg); err != nil {
		return cfg, fmt.Errorf("parse yaml: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

type logCollector struct {
	job *JobStatus
	mu  *sync.Mutex
}

func (lc *logCollector) Write(p []byte) (int, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		lc.job.Logs = append(lc.job.Logs, line)
	}
	return len(p), nil
}

// ProviderInfo describes a detected local LLM provider with its available models.
type ProviderInfo struct {
	Provider string   `json:"provider"`
	BaseURL  string   `json:"base_url"`
	Models   []string `json:"models"`
}

type detectProbe struct {
	modelsURL     string
	extractModels func(body []byte) []string
}

type detectCandidate struct {
	provider string
	baseURL  string
	probes   []detectProbe
}

type detectResult struct {
	info  ProviderInfo
	score int
}

// handleDetect probes well-known local addresses for Ollama and LM Studio.
func (s *Server) handleDetect(w http.ResponseWriter, r *http.Request) {
	candidates := buildDetectCandidates()
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	results := make(chan detectResult, len(candidates))

	var wg sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, ok := probeCandidate(client, candidate); ok {
				results <- res
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	bestByProvider := make(map[string]detectResult, 2)
	for result := range results {
		current, ok := bestByProvider[result.info.Provider]
		if !ok || result.score > current.score || (result.score == current.score && result.info.BaseURL < current.info.BaseURL) {
			bestByProvider[result.info.Provider] = result
		}
	}

	detected := make([]ProviderInfo, 0, len(bestByProvider))
	providerOrder := []string{config.ProviderOllama, config.ProviderLMStudio}
	for _, provider := range providerOrder {
		if result, ok := bestByProvider[provider]; ok {
			detected = append(detected, result.info)
		}
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: detected})
}

func buildDetectCandidates() []detectCandidate {
	ollamaBases := uniqueStrings([]string{
		normalizeHostBaseURL(os.Getenv("OLLAMA_HOST")),
		"http://localhost:11434",
		"http://127.0.0.1:11434",
	})

	lmStudioBases := uniqueStrings([]string{
		normalizeLMStudioBaseURL(os.Getenv("LLMFLOW_LMSTUDIO_BASE_URL")),
		"http://localhost:1234/v1",
		"http://127.0.0.1:1234/v1",
	})

	candidates := make([]detectCandidate, 0, len(ollamaBases)+len(lmStudioBases))
	for _, baseURL := range ollamaBases {
		candidates = append(candidates, detectCandidate{
			provider: config.ProviderOllama,
			baseURL:  baseURL,
			probes: []detectProbe{
				{
					modelsURL:     baseURL + "/api/tags",
					extractModels: parseOllamaTagsModels,
				},
				{
					modelsURL:     baseURL + "/api/ps",
					extractModels: parseOllamaRunningModels,
				},
			},
		})
	}
	for _, baseURL := range lmStudioBases {
		candidates = append(candidates, detectCandidate{
			provider: config.ProviderLMStudio,
			baseURL:  baseURL,
			probes: []detectProbe{
				{
					modelsURL:     baseURL + "/models",
					extractModels: parseOpenAICompatibleModels,
				},
			},
		})
	}

	return candidates
}

func probeCandidate(client *http.Client, candidate detectCandidate) (detectResult, bool) {
	if candidate.provider == "" || candidate.baseURL == "" {
		return detectResult{}, false
	}

	models := make([]string, 0, 8)
	reachable := false

	for _, probe := range candidate.probes {
		resp, err := client.Get(probe.modelsURL)
		if err != nil {
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			continue
		}

		reachable = true
		models = append(models, probe.extractModels(body)...)
	}

	if !reachable {
		return detectResult{}, false
	}

	info := ProviderInfo{
		Provider: candidate.provider,
		BaseURL:  candidate.baseURL,
		Models:   normalizeModelNames(models),
	}

	return detectResult{
		info:  info,
		score: detectionScore(info),
	}, true
}

func parseOllamaTagsModels(body []byte) []string {
	var resp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}

	out := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		out = append(out, model.Name)
	}
	return out
}

func parseOllamaRunningModels(body []byte) []string {
	var resp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}

	out := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		out = append(out, model.Name)
	}
	return out
}

func parseOpenAICompatibleModels(body []byte) []string {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}

	out := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		out = append(out, model.ID)
	}
	return out
}

func detectionScore(info ProviderInfo) int {
	score := len(info.Models) * 100
	if strings.Contains(info.BaseURL, "localhost") {
		score += 20
	}
	if strings.Contains(info.BaseURL, "127.0.0.1") {
		score += 10
	}
	return score
}

func normalizeModelNames(models []string) []string {
	set := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := set[model]; exists {
			continue
		}
		set[model] = struct{}{}
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeHostBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + u.Host
}

func normalizeLMStudioBaseURL(raw string) string {
	base := normalizeHostBaseURL(raw)
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return strings.TrimRight(base, "/") + "/v1"
}

// ─── /api/files ───────────────────────────────────────────────────────────────

// FileInfo describes a file in the data directory.
type FileInfo struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"` // "input" or "output"
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	Path    string `json:"path"`
}

// allowedFileDir validates and returns the absolute path for "input" or "output".
func (s *Server) allowedFileDir(dir string) (string, bool) {
	if dir != "input" && dir != "output" {
		return "", false
	}
	return filepath.Join(s.dataDir, dir), true
}

// handleListFiles lists files in data/input and data/output.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	var files []FileInfo
	for _, dir := range []string{"input", "output"} {
		dirPath := filepath.Join(s.dataDir, dir)
		if err := os.MkdirAll(dirPath, 0o750); err != nil {
			continue
		}
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			files = append(files, FileInfo{
				Name:    e.Name(),
				Dir:     dir,
				Size:    info.Size(),
				ModTime: info.ModTime().UTC().Format(time.RFC3339),
				Path:    filepath.Join(dirPath, e.Name()),
			})
		}
	}
	if files == nil {
		files = []FileInfo{}
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: files})
}

// safeInDir verifies that dst is a direct child of dirPath (no traversal).
func safeInDir(dirPath, dst string) bool {
	rel, err := filepath.Rel(filepath.Clean(dirPath), filepath.Clean(dst))
	if err != nil {
		return false
	}
	// rel must be a plain filename (no ".." components, no path separator).
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !strings.ContainsRune(rel, os.PathSeparator)
}

// handleDeleteFile deletes a file from data/input or data/output.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	dir := r.PathValue("dir")
	name := r.PathValue("name")

	dirPath, ok := s.allowedFileDir(dir)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid dir; must be 'input' or 'output'"})
		return
	}

	// Sanitize name to prevent path traversal.
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid filename"})
		return
	}

	dst := filepath.Join(dirPath, name)
	if !safeInDir(dirPath, dst) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid filename"})
		return
	}

	if err := os.Remove(dst); err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, apiResponse{Error: "file not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "delete file: " + err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// handleDownloadFile serves a file from data/input or data/output for download.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	dir := r.PathValue("dir")
	name := r.PathValue("name")

	dirPath, ok := s.allowedFileDir(dir)
	if !ok {
		http.Error(w, "invalid dir", http.StatusBadRequest)
		return
	}

	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	dst := filepath.Join(dirPath, name)
	if !safeInDir(dirPath, dst) {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, dst)
}

// ─── /api/models ──────────────────────────────────────────────────────────────

// handleListModels fetches the available models from the provider API and returns them.
// Query params: provider, base_url, api_key_env.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = config.ProviderOpenAI
	}
	baseURL := strings.TrimSpace(r.URL.Query().Get("base_url"))
	apiKeyEnv := strings.TrimSpace(r.URL.Query().Get("api_key_env"))

	apiCfg := config.APIConfig{
		Provider:  provider,
		BaseURL:   baseURL,
		APIKeyEnv: apiKeyEnv,
	}
	apiCfg.ApplyProviderDefaults()

	// Validate the resolved base URL is a well-formed http(s) URL to prevent SSRF.
	parsedURL, err := url.Parse(strings.TrimRight(apiCfg.BaseURL, "/"))
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid or unsupported base_url"})
		return
	}

	apiKey := ""
	if apiKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
	}

	modelsURL := parsedURL.String() + "/models"

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, modelsURL, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "build request: " + err.Error()})
		return
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Error: "fetch models: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "read response: " + err.Error()})
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, apiResponse{Error: fmt.Sprintf("provider returned %d: %s", resp.StatusCode, string(body))})
		return
	}

	models := parseOpenAICompatibleModels(body)
	sort.Strings(models)
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: models})
}
