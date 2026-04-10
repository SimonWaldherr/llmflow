// Package web provides an HTTP server with an embedded web UI for llmflow.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SimonWaldherr/llmflow/internal/app"
	"github.com/SimonWaldherr/llmflow/internal/config"
	"gopkg.in/yaml.v3"
)

//go:embed static/*
var staticFS embed.FS

// Server holds the web UI HTTP server state.
type Server struct {
	logger    *slog.Logger
	addr      string
	mu        sync.Mutex
	jobs      []*JobStatus
	jobIDSeq  int
	wg        sync.WaitGroup // tracks running job goroutines
	serverCtx context.Context // cancelled on graceful shutdown; used for job lifecycles
}

// JobStatus tracks a running or completed job.
type JobStatus struct {
	ID        int       `json:"id"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	Config    string    `json:"config"`
	Logs      []string  `json:"logs"`
}

// NewServer creates a new web UI server.
func NewServer(addr string, logger *slog.Logger) *Server {
	return &Server{
		addr:      addr,
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

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /api/validate", s.handleValidate)
	mux.HandleFunc("POST /api/run", s.handleRun)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/jobs/", s.handleGetJob)
	mux.HandleFunc("POST /api/upload", s.handleUpload)
	mux.HandleFunc("GET /api/detect", s.handleDetect)

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
		Status:    "running",
		StartedAt: time.Now(),
		Config:    req.Config,
	}
	s.jobs = append(s.jobs, job)
	s.mu.Unlock()

	// Use the server context so jobs are cancelled during graceful shutdown,
	// not when the HTTP response is sent.
	s.mu.Lock()
	jobCtx := s.serverCtx
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		lc := &logCollector{job: job, mu: &s.mu}
		logger := slog.New(slog.NewJSONHandler(lc, &slog.HandlerOptions{Level: slog.LevelDebug}))
		a := app.New(cfg, logger).WithDryRun(req.DryRun)
		runErr := a.Run(jobCtx)

		s.mu.Lock()
		defer s.mu.Unlock()
		job.EndedAt = time.Now()
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
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.jobs)
	start := 0
	if n > 50 {
		start = n - 50
	}
	result := make([]*JobStatus, 0, n-start)
	for i := n - 1; i >= start; i-- {
		result = append(result, s.jobs[i])
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

	uploadDir := filepath.Join(os.TempDir(), "llmflow-uploads")
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "create upload dir"})
		return
	}

	// Prefix with a nanosecond timestamp so uploads never overwrite each other.
	uniqueName := fmt.Sprintf("%d-%s", time.Now().UnixNano(), name)
	dst := filepath.Join(uploadDir, uniqueName)
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

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"path": dst}})
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

// handleDetect probes well-known local addresses for Ollama and LM Studio.
func (s *Server) handleDetect(w http.ResponseWriter, r *http.Request) {
	type candidate struct {
		provider string
		baseURL  string
		modelsURL string
		// extractModels parses the response body and returns model names.
		extractModels func(body []byte) []string
	}

	candidates := []candidate{
		{
			provider:  config.ProviderOllama,
			baseURL:   "http://localhost:11434",
			modelsURL: "http://localhost:11434/api/tags",
			extractModels: func(body []byte) []string {
				var resp struct {
					Models []struct {
						Name string `json:"name"`
					} `json:"models"`
				}
				if err := json.Unmarshal(body, &resp); err != nil {
					return nil
				}
				out := make([]string, 0, len(resp.Models))
				for _, m := range resp.Models {
					out = append(out, m.Name)
				}
				return out
			},
		},
		{
			provider:  config.ProviderLMStudio,
			baseURL:   "http://localhost:1234/v1",
			modelsURL: "http://localhost:1234/v1/models",
			extractModels: func(body []byte) []string {
				var resp struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if err := json.Unmarshal(body, &resp); err != nil {
					return nil
				}
				out := make([]string, 0, len(resp.Data))
				for _, m := range resp.Data {
					out = append(out, m.ID)
				}
				return out
			},
		},
	}

	client := &http.Client{Timeout: 2 * time.Second}
	var detected []ProviderInfo

	for _, c := range candidates {
		resp, err := client.Get(c.modelsURL)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		models := c.extractModels(body)
		detected = append(detected, ProviderInfo{
			Provider: c.provider,
			BaseURL:  c.baseURL,
			Models:   models,
		})
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: detected})
}
