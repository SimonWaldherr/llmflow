// Package web provides an HTTP server with an embedded web UI for llmflow.
package web

import (
	"bytes"
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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/app"
	"github.com/SimonWaldherr/llmflow/internal/config"
	"github.com/SimonWaldherr/llmflow/internal/input"
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
	jobsFile  string
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
	DryRun       bool             `json:"dry_run,omitempty"`
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
	jobsFile := filepath.Join(dataDir, "jobs", "jobs.json")
	return &Server{
		addr:      addr,
		dataDir:   dataDir,
		jobsFile:  jobsFile,
		logger:    logger,
		serverCtx: context.Background(), // replaced by Run(); guards against nil if handleRun is called early
	}
}

func (s *Server) dataRoot() string {
	if s == nil {
		return "data"
	}
	if dataDir := strings.TrimSpace(s.dataDir); dataDir != "" {
		return dataDir
	}
	return "data"
}

func (s *Server) jobStatePath() string {
	if s == nil {
		return filepath.Join("data", "jobs", "jobs.json")
	}
	if jobsFile := strings.TrimSpace(s.jobsFile); jobsFile != "" {
		return jobsFile
	}
	return filepath.Join(s.dataRoot(), "jobs", "jobs.json")
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

	for _, subdir := range []string{"input", "output"} {
		dir := filepath.Join(s.dataRoot(), subdir)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create data dir %s: %w", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(s.dataRoot(), "jobs"), 0o750); err != nil {
		return fmt.Errorf("create jobs dir: %w", err)
	}
	if err := s.loadJobs(); err != nil {
		return err
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /api/validate", s.handleValidate)
	mux.HandleFunc("POST /api/run", s.handleRun)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/jobs/", s.handleGetJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/rerun", s.handleRerunJob)
	mux.HandleFunc("POST /api/jobs/{id}/archive", s.handleArchiveJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.handleDeleteJob)
	mux.HandleFunc("POST /api/upload", s.handleUpload)
	mux.HandleFunc("GET /api/files", s.handleListFiles)
	mux.HandleFunc("DELETE /api/files/{dir}/{name}", s.handleDeleteFile)
	mux.HandleFunc("GET /api/files/download/{dir}/{name}", s.handleDownloadFile)
	mux.HandleFunc("GET /api/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /docs", s.handleSwaggerUI)
	mux.HandleFunc("POST /api/models", s.handleModels)
	mux.HandleFunc("GET /api/detect", s.handleDetect)
	mux.HandleFunc("POST /api/suggest", s.handleSuggest)
	mux.HandleFunc("POST /api/preview", s.handlePreview)

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

type modelsRequest struct {
	Provider  string `json:"provider"`
	APIKey    string `json:"api_key"`
	APIKeyEnv string `json:"api_key_env"`
	BaseURL   string `json:"base_url"`
}

type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// FileInfo describes a file in the data directory.
type FileInfo struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	Path    string `json:"path"`
}

type jobState struct {
	Jobs []*JobStatus `json:"jobs"`
}

func writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) persistJobs() {
	if s == nil {
		return
	}
	s.mu.Lock()
	state := jobState{Jobs: make([]*JobStatus, 0, len(s.jobs))}
	for _, job := range s.jobs {
		state.Jobs = append(state.Jobs, cloneJobStatus(job))
	}
	path := s.jobStatePath()
	s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		if s.logger != nil {
			s.logger.Warn("persist jobs mkdir", "error", err)
		}
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("persist jobs marshal", "error", err)
		}
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		if s.logger != nil {
			s.logger.Warn("persist jobs write", "error", err)
		}
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		if s.logger != nil {
			s.logger.Warn("persist jobs rename", "error", err)
		}
	}
}

func (s *Server) loadJobs() error {
	if s == nil {
		return nil
	}
	path := s.jobStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read job state: %w", err)
	}
	var state jobState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse job state: %w", err)
	}
	now := time.Now()
	maxID := 0
	for _, job := range state.Jobs {
		if job == nil {
			continue
		}
		if job.ID > maxID {
			maxID = job.ID
		}
		if job.Status == "running" {
			job.Status = "failed"
			job.Error = "recovered after server restart"
			job.EndedAt = now
		}
	}
	s.mu.Lock()
	s.jobs = state.Jobs
	s.jobIDSeq = maxID
	s.mu.Unlock()
	if maxID > 0 {
		s.persistJobs()
	}
	return nil
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

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	var req modelsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
		return
	}

	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = config.ProviderOpenAI
	}

	apiCfg := config.APIConfig{
		Provider: provider,
		BaseURL:  req.BaseURL,
		Timeout:  15 * time.Second,
	}
	apiKeyDirect, apiKeyEnv := resolveQuickFormAPIKey(firstNonEmpty(req.APIKey, req.APIKeyEnv))
	apiCfg.APIKeyDirect = apiKeyDirect
	apiCfg.APIKeyEnv = apiKeyEnv
	apiCfg.ApplyProviderDefaults()

	apiKey := apiCfg.APIKeyDirect
	if apiKey == "" && apiCfg.APIKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(apiCfg.APIKeyEnv))
	}
	if apiKey == "" {
		if apiCfg.APIKeyEnv != "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("API key env var %q is not set; set it on the server before loading models", apiCfg.APIKeyEnv)})
		} else {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "API key is required"})
		}
		return
	}

	models, err := fetchProviderModels(r.Context(), apiCfg, apiKey)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: models})
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
	job := s.enqueueJob(req.Name, req.Config, req.DryRun, cfg)

	writeJSON(w, http.StatusAccepted, apiResponse{OK: true, Data: cloneJobStatus(job)})
}

func (s *Server) enqueueJob(name, rawConfig string, dryRun bool, cfg config.Config) *JobStatus {
	s.mu.Lock()
	s.jobIDSeq++
	job := &JobStatus{
		ID:        s.jobIDSeq,
		Name:      name,
		Status:    "running",
		StartedAt: time.Now(),
		Config:    rawConfig,
		DryRun:    dryRun,
	}
	s.jobs = append(s.jobs, job)
	s.mu.Unlock()
	s.persistJobs()

	s.mu.Lock()
	serverCtx := s.serverCtx
	if serverCtx == nil {
		serverCtx = context.Background()
	}
	jobCtx, jobCancel := context.WithCancel(serverCtx)
	job.cancelFn = jobCancel
	s.mu.Unlock()

	s.wg.Add(1)
	go s.runJob(job, cfg, jobCtx, jobCancel)
	return job
}

func (s *Server) runJob(job *JobStatus, cfg config.Config, jobCtx context.Context, jobCancel context.CancelFunc) {
	defer s.wg.Done()
	defer jobCancel()
	lc := &logCollector{job: job, mu: &s.mu, persist: s.persistJobs}
	logger := slog.New(slog.NewJSONHandler(lc, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const maxPreviewItems = 20
	progress := func(current, total int) {
		s.mu.Lock()
		job.Progress.Current = current
		job.Progress.Total = total
		s.mu.Unlock()
		s.persistJobs()
	}
	resultPreview := func(index, total int, record map[string]any) {
		s.mu.Lock()
		job.PreviewCount++
		item := JobPreviewItem{Index: index, Record: record}
		if len(job.Preview) >= maxPreviewItems {
			job.Preview = append(job.Preview[1:], item)
			s.mu.Unlock()
			s.persistJobs()
			return
		}
		job.Preview = append(job.Preview, item)
		s.mu.Unlock()
		s.persistJobs()
	}

	a := app.New(cfg, logger).WithDryRun(job.DryRun).WithProgressFunc(progress).WithResultFunc(resultPreview)
	runErr := a.Run(jobCtx)

	s.mu.Lock()
	job.EndedAt = time.Now()
	if job.Status == "cancelled" {
		s.mu.Unlock()
		s.persistJobs()
		return
	}
	if runErr != nil {
		job.Status = "failed"
		job.Error = runErr.Error()
	} else {
		job.Status = "completed"
	}
	s.mu.Unlock()
	s.persistJobs()
}

// allowedFileDir validates and returns the absolute path for "input" or "output".
func (s *Server) allowedFileDir(dir string) (string, bool) {
	if dir != "input" && dir != "output" {
		return "", false
	}
	return filepath.Join(s.dataRoot(), dir), true
}

// handleListFiles lists files in data/input and data/output.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	files := make([]FileInfo, 0)
	for _, dir := range []string{"input", "output"} {
		dirPath, ok := s.allowedFileDir(dir)
		if !ok {
			continue
		}
		if err := os.MkdirAll(dirPath, 0o750); err != nil {
			continue
		}
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			files = append(files, FileInfo{
				Name:    entry.Name(),
				Dir:     dir,
				Size:    info.Size(),
				ModTime: info.ModTime().UTC().Format(time.RFC3339),
				Path:    filepath.Join(dirPath, entry.Name()),
			})
		}
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: files})
}

// safeInDir verifies that dst is a direct child of dirPath (no traversal).
func safeInDir(dirPath, dst string) bool {
	rel, err := filepath.Rel(filepath.Clean(dirPath), filepath.Clean(dst))
	if err != nil {
		return false
	}
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

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":   "llmflow Web API",
			"version": "1.0.0",
		},
		"servers": []map[string]any{{"url": "/"}},
		"paths": map[string]any{
			"/health": map[string]any{
				"get": map[string]any{
					"summary":   "Health check",
					"responses": map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
			"/api/validate": map[string]any{
				"post": map[string]any{
					"summary": "Validate a config",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"config": map[string]any{"type": "string"}},
							"required":   []string{"config"},
						}}},
					},
					"responses": map[string]any{"200": map[string]any{"description": "Validation result"}},
				},
			},
			"/api/run": map[string]any{
				"post": map[string]any{
					"summary": "Submit a job",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"config":  map[string]any{"type": "string"},
								"dry_run": map[string]any{"type": "boolean"},
								"name":    map[string]any{"type": "string"},
							},
							"required": []string{"config"},
						}}},
					},
					"responses": map[string]any{"202": map[string]any{"description": "Job accepted"}},
				},
			},
			"/api/jobs": map[string]any{
				"get": map[string]any{
					"summary":   "List jobs",
					"responses": map[string]any{"200": map[string]any{"description": "Job list"}},
				},
			},
			"/api/jobs/{id}": map[string]any{
				"get":    map[string]any{"summary": "Get job details", "responses": map[string]any{"200": map[string]any{"description": "Job"}}},
				"delete": map[string]any{"summary": "Delete a job", "responses": map[string]any{"200": map[string]any{"description": "Deleted"}}},
			},
			"/api/jobs/{id}/rerun":   map[string]any{"post": map[string]any{"summary": "Re-run an old job", "responses": map[string]any{"202": map[string]any{"description": "Job accepted"}}}},
			"/api/jobs/{id}/cancel":  map[string]any{"post": map[string]any{"summary": "Cancel a running job", "responses": map[string]any{"200": map[string]any{"description": "Cancelled"}}}},
			"/api/jobs/{id}/archive": map[string]any{"post": map[string]any{"summary": "Archive a job", "responses": map[string]any{"200": map[string]any{"description": "Archived"}}}},
			"/api/upload": map[string]any{
				"post": map[string]any{
					"summary": "Upload an input file",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{"multipart/form-data": map[string]any{"schema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"file": map[string]any{"type": "string", "format": "binary"}},
							"required":   []string{"file"},
						}}},
					},
					"responses": map[string]any{"200": map[string]any{"description": "Uploaded"}},
				},
			},
			"/api/files":                       map[string]any{"get": map[string]any{"summary": "List input and output files", "responses": map[string]any{"200": map[string]any{"description": "Files"}}}},
			"/api/files/download/{dir}/{name}": map[string]any{"get": map[string]any{"summary": "Download a file", "responses": map[string]any{"200": map[string]any{"description": "File download"}}}},
			"/api/models":                      map[string]any{"post": map[string]any{"summary": "Fetch available models", "responses": map[string]any{"200": map[string]any{"description": "Models"}}}},
			"/api/detect":                      map[string]any{"get": map[string]any{"summary": "Detect local providers", "responses": map[string]any{"200": map[string]any{"description": "Providers"}}}},
			"/api/suggest":                     map[string]any{"post": map[string]any{"summary": "Generate quick setup suggestions", "responses": map[string]any{"200": map[string]any{"description": "Suggestion"}}}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spec)
}

func (s *Server) handleSwaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>llmflow API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>body{margin:0;background:#0d1117}#swagger-ui{max-width:none}</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: '/openapi.json',
      dom_id: '#swagger-ui',
      deepLinking: true,
      displayRequestDuration: true,
      presets: [SwaggerUIBundle.presets.apis],
      layout: 'BaseLayout'
    });
  </script>
</body>
</html>`)
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
		result = append(result, cloneJobStatus(j))
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
			writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: cloneJobStatus(j)})
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
	var resp apiResponse
	for _, j := range s.jobs {
		if j.ID == id {
			if j.Status != "running" {
				s.mu.Unlock()
				writeJSON(w, http.StatusConflict, apiResponse{Error: "job is not running"})
				return
			}
			j.Status = "cancelled"
			j.EndedAt = time.Now()
			if j.cancelFn != nil {
				j.cancelFn()
			}
			resp = apiResponse{OK: true}
			s.mu.Unlock()
			s.persistJobs()
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
}

func (s *Server) handleArchiveJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePatternID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid job id"})
		return
	}
	s.mu.Lock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.Archived = true
			s.mu.Unlock()
			s.persistJobs()
			writeJSON(w, http.StatusOK, apiResponse{OK: true})
			return
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
}

func (s *Server) handleRerunJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePatternID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid job id"})
		return
	}

	s.mu.Lock()
	var source *JobStatus
	for _, j := range s.jobs {
		if j.ID == id {
			source = cloneJobStatus(j)
			break
		}
	}
	s.mu.Unlock()
	if source == nil {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
		return
	}

	cfg, err := parseConfig(source.Config)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "cannot rerun job: " + err.Error()})
		return
	}

	name := strings.TrimSpace(source.Name)
	if name == "" {
		name = fmt.Sprintf("job #%d", source.ID)
	}
	job := s.enqueueJob(name+" (re-run)", source.Config, source.DryRun, cfg)
	writeJSON(w, http.StatusAccepted, apiResponse{OK: true, Data: cloneJobStatus(job)})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePatternID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid job id"})
		return
	}
	s.mu.Lock()
	for i, j := range s.jobs {
		if j.ID == id {
			if j.Status == "running" {
				s.mu.Unlock()
				writeJSON(w, http.StatusConflict, apiResponse{Error: "cannot delete a running job; cancel it first"})
				return
			}
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			s.mu.Unlock()
			s.persistJobs()
			writeJSON(w, http.StatusOK, apiResponse{OK: true})
			return
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusNotFound, apiResponse{Error: "job not found"})
}

func cloneJobStatus(src *JobStatus) *JobStatus {
	if src == nil {
		return nil
	}
	clone := *src
	if src.Logs != nil {
		clone.Logs = append([]string(nil), src.Logs...)
	}
	if src.Preview != nil {
		clone.Preview = append([]JobPreviewItem(nil), src.Preview...)
	}
	clone.cancelFn = nil
	return &clone
}

func fetchProviderModels(ctx context.Context, cfg config.APIConfig, apiKey string) ([]string, error) {
	modelsURL := strings.TrimRight(cfg.BaseURL, "/")
	if modelsURL == "" {
		return nil, fmt.Errorf("base url is required")
	}

	var req *http.Request
	var err error
	switch strings.ToLower(cfg.Provider) {
	case config.ProviderOllama:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, modelsURL+"/api/tags", nil)
	case config.ProviderOpenAI, config.ProviderGeneric, config.ProviderLMStudio:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, modelsURL+"/models", nil)
	case config.ProviderGemini:
		endpoint := modelsURL + "/models"
		if apiKey != "" {
			endpoint += "?key=" + url.QueryEscape(apiKey)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	default:
		return nil, fmt.Errorf("model listing is not supported for provider %q", cfg.Provider)
	}
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if apiKey != "" {
		switch strings.ToLower(cfg.Provider) {
		case config.ProviderGemini:
			// Key is passed in the query string for Gemini.
		default:
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("model list status %d: %s", resp.StatusCode, string(body))
	}

	return parseModelNames(body), nil
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

	uploadDir := filepath.Join(s.dataRoot(), "input")
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "create upload dir"})
		return
	}

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
	APIKey    string `json:"api_key"`
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
	Notes         string `json:"notes,omitempty"`
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
- notes: one short sentence explaining why the chosen fields fit the task.

When a current config is supplied, treat it as the baseline and only change fields that
improve the task. Be deliberate about every field: provider/model/base_url only when the
selected runtime matters, input/output format when the file shape or downstream needs
change, include_input_in_output when traceability matters, workers/rate limits/timeouts
when throughput or reliability matter, prompt_caching when prompts are long/static, and
tools only when the task truly needs web/search/code/SQL assistance.

Respond with ONLY a JSON object — no markdown, no explanation — following this schema:
{
  "system_prompt": "...",
  "pre_prompt": "...",
  "input_template": "...",
  "post_prompt": "...",
  "response_field": "...",
	"job_name": "...",
	"notes": "..."
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
		Provider: provider,
		Model:    req.Model,
		BaseURL:  req.BaseURL,
		Timeout:  suggestTimeout,
	}
	apiKeyDirect, apiKeyEnv := resolveQuickFormAPIKey(firstNonEmpty(req.APIKey, req.APIKeyEnv))
	apiCfg.APIKeyDirect = apiKeyDirect
	apiCfg.APIKeyEnv = apiKeyEnv
	apiCfg.ApplyProviderDefaults()

	apiKey := apiCfg.APIKeyDirect
	if apiKey == "" && apiCfg.APIKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(apiCfg.APIKeyEnv))
	}
	nokeyProviders := map[string]bool{config.ProviderOllama: true, config.ProviderLMStudio: true}
	if apiKey == "" && !nokeyProviders[strings.ToLower(apiCfg.Provider)] {
		if apiCfg.APIKeyEnv != "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("API key env var %q is not set; set it on the server before using AI suggestions", apiCfg.APIKeyEnv)})
		} else {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "API key is required"})
		}
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
		fallback := buildSuggestFallback(req.Task, req.Config)
		writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: fallback})
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

func buildSuggestFallback(task, currentConfig string) suggestResponse {
	jobName := buildSuggestJobName(task)
	responseField := "response"
	if strings.Contains(strings.ToLower(task), "summary") {
		responseField = "summary"
	} else if strings.Contains(strings.ToLower(task), "classif") {
		responseField = "classification"
	}
	prePrompt := strings.TrimSpace(task)
	if prePrompt != "" {
		prePrompt = "Follow this task: " + prePrompt
	}
	inputTemplate := "{{ toPrettyJSON .record }}"
	if strings.TrimSpace(currentConfig) != "" {
		inputTemplate = strings.TrimSpace(inputTemplate)
	}
	return suggestResponse{
		SystemPrompt:  "You are a helpful data-processing assistant.",
		PrePrompt:     prePrompt,
		InputTemplate: inputTemplate,
		PostPrompt:    "Return a concise result that matches the task.",
		JobName:       jobName,
		ResponseField: responseField,
		Notes:         "Fallback configuration generated because the LLM request failed.",
	}
}

func buildSuggestJobName(task string) string {
	cleaned := strings.ToLower(strings.TrimSpace(task))
	if cleaned == "" {
		return "data job"
	}
	re := regexp.MustCompile(`[^a-z0-9]+`)
	parts := strings.Fields(re.ReplaceAllString(cleaned, " "))
	if len(parts) == 0 {
		return "data job"
	}
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, " ")
}

func resolveQuickFormAPIKey(raw string) (direct string, env string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if strings.HasPrefix(raw, "sk") {
		return raw, ""
	}
	return "", raw
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

// ─── /api/preview ─────────────────────────────────────────────────────────────
// handlePreview reads the first N records from an input file and returns them
// along with the detected column names. This lets the web UI render a preview
// table and let the user deselect columns before running a job.

type previewRequest struct {
	// Type is the input type: csv, json, jsonl, xml.
	Type string `json:"type"`
	// Path is the file path on the server (e.g. data/input/file.csv).
	Path string `json:"path"`
	// N is how many records to read (default 10, capped at 100).
	N int `json:"n"`
	// CSV holds optional CSV config overrides (delimiter, has_header).
	CSV config.CSVConfig `json:"csv"`
	// JSON holds optional JSON/JSONL config.
	JSON config.JSONConfig `json:"json"`
	// XML holds optional XML config.
	XML config.XMLConfig `json:"xml"`
}

type previewResponse struct {
	Columns []string         `json:"columns"`
	Records []map[string]any `json:"records"`
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
		return
	}

	reqType := strings.TrimSpace(strings.ToLower(req.Type))
	if reqType == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "type is required"})
		return
	}

	// Sanitise the path: only allow files inside the data root.
	cleanPath := filepath.Clean(req.Path)
	dataRoot := filepath.Clean(s.dataRoot())
	if !strings.HasPrefix(cleanPath, dataRoot+string(os.PathSeparator)) && cleanPath != dataRoot {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "path must be inside the data directory"})
		return
	}

	n := req.N
	if n <= 0 {
		n = 10
	}
	if n > 100 {
		n = 100
	}

	cfg := config.InputConfig{
		Type: reqType,
		Path: cleanPath,
		CSV:  req.CSV,
		JSON: req.JSON,
		XML:  req.XML,
	}

	reader, err := input.New(cfg)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "open input: " + err.Error()})
		return
	}
	defer reader.Close()

	records, err := app.PreviewRecords(reader, n)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "read records: " + err.Error()})
		return
	}

	// Collect column names in stable order.
	colSet := map[string]struct{}{}
	var columns []string
	for _, rec := range records {
		for k := range rec {
			if _, seen := colSet[k]; !seen {
				colSet[k] = struct{}{}
				columns = append(columns, k)
			}
		}
	}
	sort.Strings(columns)

	recs := make([]map[string]any, len(records))
	for i, rec := range records {
		recs[i] = rec
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: previewResponse{
		Columns: columns,
		Records: recs,
	}})
}

func parseConfig(yamlText string) (config.Config, error) {
	var cfg config.Config
	trimmed := bytes.TrimSpace([]byte(yamlText))
	if len(trimmed) == 0 {
		return cfg, fmt.Errorf("parse config: empty input")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &cfg); err != nil {
			return cfg, fmt.Errorf("parse json: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(trimmed, &cfg); err != nil {
			return cfg, fmt.Errorf("parse yaml: %w", err)
		}
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

type logCollector struct {
	job     *JobStatus
	mu      *sync.Mutex
	persist func()
}

func (lc *logCollector) Write(p []byte) (int, error) {
	lc.mu.Lock()
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		lc.job.Logs = append(lc.job.Logs, line)
	}
	lc.mu.Unlock()
	if lc.persist != nil {
		lc.persist()
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
	return parseModelNames(body)
}

func parseModelNames(body []byte) []string {
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}

	out := make([]string, 0, len(resp.Data)+len(resp.Models))
	for _, model := range resp.Data {
		if model.ID != "" {
			out = append(out, model.ID)
			continue
		}
		if model.Name != "" {
			out = append(out, model.Name)
		}
	}
	for _, model := range resp.Models {
		if model.Name != "" {
			out = append(out, model.Name)
			continue
		}
		if model.ID != "" {
			out = append(out, model.ID)
		}
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
