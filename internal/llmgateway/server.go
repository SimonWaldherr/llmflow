package llmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Gateway struct {
	logger  *slog.Logger
	runtime *RuntimeManager
	metrics *Metrics
	events  *EventHub
	client  *http.Client

	openai *http.Server
	ollama *http.Server
	admin  *http.Server

	reqID atomic.Uint64
}

func NewGateway(cfg Config, logger *slog.Logger) (*Gateway, error) {
	runtimeMgr, err := NewRuntimeManager(cfg, logger)
	if err != nil {
		return nil, err
	}
	g := &Gateway{
		logger:  logger,
		runtime: runtimeMgr,
		metrics: NewMetrics(),
		events:  NewEventHub(),
		client:  &http.Client{Timeout: 180 * time.Second},
	}
	state := runtimeMgr.Current()
	g.openai = &http.Server{Addr: state.cfg.Servers.OpenAIAddr, Handler: g.openAIHandler()}
	g.ollama = &http.Server{Addr: state.cfg.Servers.OllamaAddr, Handler: g.ollamaHandler()}
	g.admin = &http.Server{Addr: state.cfg.Admin.Addr, Handler: g.adminHandler()}
	return g, nil
}

func (g *Gateway) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go g.healthLoop(ctx)

	errs := make(chan error, 3)
	startServer := func(name string, srv *http.Server) {
		go func() {
			g.logger.Info(name+" endpoint listening", "addr", srv.Addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}
	startServer("openai", g.openai)
	startServer("ollama", g.ollama)
	startServer("admin", g.admin)

	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != nil {
			cancel()
			_ = g.shutdown(context.Background())
			return err
		}
	}
	return g.shutdown(context.Background())
}

func (g *Gateway) healthLoop(ctx context.Context) {
	state := g.runtime.Current()
	if state == nil {
		return
	}
	probeClient := &http.Client{Timeout: state.cfg.Health.Timeout}
	ticker := time.NewTicker(state.cfg.Health.Interval)
	defer ticker.Stop()
	for {
		current := g.runtime.Current()
		if current != nil {
			current.backends.CheckAll(ctx, current.cfg.Health, probeClient, func(b *Backend) {
				g.logger.Debug("health-check", "backend", b.Spec.Name, "healthy", b.IsHealthy(), "ms", b.LastDuration().Milliseconds(), "error", b.LastError())
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *Gateway) shutdown(ctx context.Context) error {
	tx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex

	shutdown := func(s *http.Server) {
		defer wg.Done()
		if err := s.Shutdown(tx); err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}

	wg.Add(3)
	go shutdown(g.openai)
	go shutdown(g.ollama)
	go shutdown(g.admin)
	wg.Wait()
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (g *Gateway) openAIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", g.healthHandler)
	mux.HandleFunc("/v1/models", g.openAIModelsHandler)
	mux.HandleFunc("/v1/", g.proxyHandler("openai"))
	mux.HandleFunc("/", notFound)
	return g.loggingMiddleware("openai", mux)
}

func (g *Gateway) ollamaHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", g.healthHandler)
	mux.HandleFunc("/api/tags", g.ollamaTagsHandler)
	mux.HandleFunc("/api/", g.proxyHandler("ollama"))
	mux.HandleFunc("/", notFound)
	return g.loggingMiddleware("ollama", mux)
}

func (g *Gateway) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", g.uiHandler)
	mux.HandleFunc("/metrics", g.promMetricsHandler)
	mux.HandleFunc("/admin/events", g.eventsHandler)
	mux.HandleFunc("/admin/config", g.getConfigHandler)
	mux.HandleFunc("/admin/config/validate", g.validateConfigHandler)
	mux.HandleFunc("/admin/config/apply", g.applyConfigHandler)
	mux.HandleFunc("/admin/config/rollback", g.rollbackConfigHandler)
	mux.HandleFunc("/admin/route/dry-run", g.dryRunRouteHandler)
	mux.HandleFunc("/admin/backends", g.backendsHandler)
	mux.HandleFunc("/admin/decisions", g.decisionsHandler)
	mux.HandleFunc("/admin/metrics", g.metricsHandler)
	mux.HandleFunc("/admin/feature-flags", g.featureFlagsHandler)
	mux.HandleFunc("/admin/snapshots", g.snapshotsHandler)
	mux.HandleFunc("/admin/audit", g.auditHandler)
	return g.loggingMiddleware("admin", mux)
}

func (g *Gateway) proxyHandler(api string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		state := g.runtime.Current()
		if state == nil {
			http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()

		info := parseRequestInfo(r.URL.Path, body)
		requestID := g.ensureRequestID(r)

		pc := &ProxyContext{RequestID: requestID, API: api, Input: RouteInput{Path: r.URL.Path, Model: info.Model, Prompt: info.Prompt, TokenCount: info.TokenCount, Metadata: info.Metadata, RequestBody: body}}
		if err := state.policies.BeforeRoute(pc, r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		resolved, err := state.router.Resolve(r.Context(), pc.Input)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		pc.Resolved = resolved
		if err := state.policies.BeforeProxy(pc, r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		start := time.Now()
		resp, backend, attempts, failoverCount, err := proxyWithFailover(r.Context(), g.client, r, body, resolved.Backends)
		if backend != nil {
			pc.Backend = backend
		}
		state.policies.AfterProxy(pc, r, resp, err)
		duration := time.Since(start)

		ev := DecisionEvent{
			Timestamp:    time.Now().UTC(),
			RequestID:    requestID,
			API:          api,
			Path:         r.URL.Path,
			Model:        info.Model,
			Tokens:       info.TokenCount,
			RouteName:    resolved.RouteName,
			Reason:       resolved.Reason,
			Strategy:     resolved.Strategy,
			DurationMs:   duration.Milliseconds(),
			Fallback:     attempts,
			FailureCount: failoverCount,
		}
		if err != nil {
			ev.Success = false
			ev.Status = http.StatusBadGateway
			ev.Error = err.Error()
			if backend != nil {
				ev.Backend = backend.Spec.Name
			}
			g.runtime.AddDecision(ev)
			g.metrics.RecordDecision(ev)
			g.events.Publish(map[string]any{"type": "decision", "data": ev})
			g.logger.Error("routing failed", "request_id", requestID, "api", api, "path", r.URL.Path, "model", info.Model, "tokens", info.TokenCount, "route", resolved.RouteName, "reason", resolved.Reason, "attempts", attempts, "error", err)
			http.Error(w, "all backends failed", http.StatusBadGateway)
			return
		}

		ev.Success = true
		ev.Status = resp.StatusCode
		ev.Backend = backend.Spec.Name
		g.runtime.AddDecision(ev)
		g.metrics.RecordDecision(ev)
		g.events.Publish(map[string]any{"type": "decision", "data": ev})

		g.logger.Info("route decision",
			"request_id", requestID,
			"api", api,
			"route", resolved.RouteName,
			"reason", resolved.Reason,
			"strategy", resolved.Strategy,
			"backend", backend.Spec.Name,
			"model", info.Model,
			"tokens", info.TokenCount,
			"path", r.URL.Path,
			"failover_hops", failoverCount,
		)
		if err := writeProxyResponse(w, resp); err != nil {
			g.logger.Warn("write response failed", "backend", backend.Spec.Name, "error", err)
		}
	}
}

func (g *Gateway) healthHandler(w http.ResponseWriter, _ *http.Request) {
	state := g.runtime.Current()
	if state == nil {
		http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	health := state.backends.HealthSnapshot()
	status := http.StatusOK
	for _, item := range health {
		h, _ := item["healthy"].(bool)
		if !h {
			status = http.StatusServiceUnavailable
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"backends": health, "status": status})
}

func (g *Gateway) openAIModelsHandler(w http.ResponseWriter, _ *http.Request) {
	type modelEntry struct {
		ID string `json:"id"`
	}
	models := g.collectModels()
	entries := make([]modelEntry, 0, len(models))
	for _, model := range models {
		entries = append(entries, modelEntry{ID: model})
	}
	writeJSON(w, map[string]any{"object": "list", "data": entries})
}

func (g *Gateway) ollamaTagsHandler(w http.ResponseWriter, _ *http.Request) {
	type modelEntry struct {
		Name string `json:"name"`
	}
	models := g.collectModels()
	entries := make([]modelEntry, 0, len(models))
	for _, model := range models {
		entries = append(entries, modelEntry{Name: model})
	}
	writeJSON(w, map[string]any{"models": entries})
}

func (g *Gateway) collectModels() []string {
	state := g.runtime.Current()
	if state == nil {
		return nil
	}
	set := map[string]struct{}{}
	for _, b := range state.backends.All() {
		for _, model := range b.Spec.Models {
			if strings.TrimSpace(model) != "" {
				set[model] = struct{}{}
			}
		}
		for from, to := range b.Spec.ModelMap {
			if strings.TrimSpace(from) != "" {
				set[from] = struct{}{}
			}
			if strings.TrimSpace(to) != "" {
				set[to] = struct{}{}
			}
		}
	}
	if len(set) == 0 {
		for _, b := range state.backends.All() {
			set[b.Spec.Name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for model := range set {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func (g *Gateway) uiHandler(w http.ResponseWriter, r *http.Request) {
	state := g.runtime.Current()
	if r.URL.Path != "/" {
		notFound(w, r)
		return
	}
	if state != nil && !state.cfg.Admin.EnableGUI {
		http.Error(w, "GUI disabled", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminUIHTML))
}

func (g *Gateway) getConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	state := g.runtime.Current()
	if state == nil {
		http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"yaml": g.runtime.ConfigYAML(), "version": g.runtime.version.Load(), "read_only": state.cfg.Admin.ReadOnly})
}

func (g *Gateway) validateConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	cfg, err := LoadConfigBytes([]byte(req.YAML))
	if err != nil {
		writeJSON(w, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"valid": true, "backends": len(cfg.Backends), "routes": len(cfg.Routes), "policies": len(cfg.Policies)})
}

func (g *Gateway) applyConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, false) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		YAML   string `json:"yaml"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	cfg, err := LoadConfigBytes([]byte(req.YAML))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := g.validateListenerAddresses(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Source == "" {
		req.Source = "api"
	}
	version, err := g.runtime.ApplyConfig(cfg, req.Source, g.actorFromRequest(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.events.Publish(map[string]any{"type": "config-applied", "version": version})
	writeJSON(w, map[string]any{"ok": true, "version": version})
}

func (g *Gateway) rollbackConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, false) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Version int64 `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	version, err := g.runtime.Rollback(req.Version, g.actorFromRequest(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.events.Publish(map[string]any{"type": "config-rollback", "version": version, "to": req.Version})
	writeJSON(w, map[string]any{"ok": true, "version": version})
}

func (g *Gateway) dryRunRouteHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path     string            `json:"path"`
		Model    string            `json:"model"`
		Prompt   string            `json:"prompt"`
		Tokens   int               `json:"tokens"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		req.Path = "/v1/chat/completions"
	}
	state := g.runtime.Current()
	if state == nil {
		http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	in := RouteInput{Path: req.Path, Model: req.Model, Prompt: req.Prompt, Metadata: req.Metadata, TokenCount: req.Tokens}
	if in.TokenCount <= 0 {
		in.TokenCount = estimateTokenCount(req.Prompt)
	}
	resolved, err := state.router.Resolve(r.Context(), in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	backendNames := make([]string, 0, len(resolved.Backends))
	for _, b := range resolved.Backends {
		backendNames = append(backendNames, b.Spec.Name)
	}
	writeJSON(w, map[string]any{"route": resolved.RouteName, "reason": resolved.Reason, "strategy": resolved.Strategy, "backends": backendNames, "tokens": in.TokenCount})
}

func (g *Gateway) backendsHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	state := g.runtime.Current()
	if state == nil {
		http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"backends": state.backends.HealthSnapshot()})
}

func (g *Gateway) decisionsHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, map[string]any{"decisions": g.runtime.Decisions(limit)})
}

func (g *Gateway) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	writeJSON(w, g.metrics.Snapshot())
}

func (g *Gateway) promMetricsHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(g.metrics.Prometheus()))
}

func (g *Gateway) featureFlagsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !g.authorizeAdmin(w, r, true) {
			return
		}
		writeJSON(w, map[string]any{"feature_flags": g.runtime.FeatureFlags()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !g.authorizeAdmin(w, r, false) {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := g.runtime.SetFeatureFlag(req.Name, req.Enabled, g.actorFromRequest(r)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.events.Publish(map[string]any{"type": "feature-flag", "name": req.Name, "enabled": req.Enabled})
	writeJSON(w, map[string]any{"ok": true, "feature_flags": g.runtime.FeatureFlags()})
}

func (g *Gateway) snapshotsHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	writeJSON(w, map[string]any{"snapshots": g.runtime.Snapshots()})
}

func (g *Gateway) auditHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	writeJSON(w, map[string]any{"audit": g.runtime.Audit()})
}

func (g *Gateway) eventsHandler(w http.ResponseWriter, r *http.Request) {
	if !g.authorizeAdmin(w, r, true) {
		return
	}
	writeSSE(w, r, g.events)
}

func (g *Gateway) authorizeAdmin(w http.ResponseWriter, r *http.Request, readOnlyAllowed bool) bool {
	state := g.runtime.Current()
	if state == nil {
		http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
		return false
	}
	if state.cfg.Admin.Token != "" {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != state.cfg.Admin.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
	}
	if state.cfg.Admin.ReadOnly && !readOnlyAllowed {
		http.Error(w, "admin API in read-only mode", http.StatusForbidden)
		return false
	}
	return true
}

func (g *Gateway) actorFromRequest(r *http.Request) string {
	if actor := strings.TrimSpace(r.Header.Get("X-Actor")); actor != "" {
		return actor
	}
	return r.RemoteAddr
}

func (g *Gateway) ensureRequestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-ID")); id != "" {
		return id
	}
	next := g.reqID.Add(1)
	return fmt.Sprintf("req-%d", next)
}

func (g *Gateway) validateListenerAddresses(cfg Config) error {
	if cfg.Servers.OpenAIAddr != g.openai.Addr || cfg.Servers.OllamaAddr != g.ollama.Addr || cfg.Admin.Addr != g.admin.Addr {
		return fmt.Errorf("listen addresses cannot be hot-reloaded; restart required")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
	}
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "not found", http.StatusNotFound)
}

func (g *Gateway) loggingMiddleware(api string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		g.logger.Debug("request", "api", api, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}
