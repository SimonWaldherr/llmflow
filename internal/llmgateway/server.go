package llmgateway

import (
"context"
"encoding/json"
"errors"
"fmt"
"io"
"log/slog"
"net/http"
"slices"
"sort"
"strings"
"sync"
"time"
)

type Gateway struct {
cfg      Config
logger   *slog.Logger
backends *BackendManager
router   *Router
client   *http.Client
openai   *http.Server
ollama   *http.Server
}

func NewGateway(cfg Config, logger *slog.Logger) (*Gateway, error) {
manager, err := NewBackendManager(cfg.Backends)
if err != nil {
return nil, err
}
balancer := NewBalancer()
router := NewRouter(cfg, manager, balancer, logger)
client := &http.Client{Timeout: 180 * time.Second}
g := &Gateway{cfg: cfg, logger: logger, backends: manager, router: router, client: client}
g.openai = &http.Server{Addr: cfg.Servers.OpenAIAddr, Handler: g.openAIHandler()}
g.ollama = &http.Server{Addr: cfg.Servers.OllamaAddr, Handler: g.ollamaHandler()}
return g, nil
}

func (g *Gateway) Run(ctx context.Context) error {
ctx, cancel := context.WithCancel(ctx)
defer cancel()

go g.backends.StartHealthChecks(ctx, g.cfg.Health, &http.Client{Timeout: g.cfg.Health.Timeout}, func(b *Backend) {
g.logger.Debug("health-check", "backend", b.Spec.Name, "healthy", b.IsHealthy(), "ms", b.LastDuration().Milliseconds(), "error", b.LastError())
})

errs := make(chan error, 2)
go func() {
g.logger.Info("openai endpoint listening", "addr", g.openai.Addr)
if err := g.openai.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
errs <- err
}
}()
go func() {
g.logger.Info("ollama endpoint listening", "addr", g.ollama.Addr)
if err := g.ollama.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
errs <- err
}
}()

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

wg.Add(2)
go shutdown(g.openai)
go shutdown(g.ollama)
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
return loggingMiddleware(g.logger, "openai", mux)
}

func (g *Gateway) ollamaHandler() http.Handler {
mux := http.NewServeMux()
mux.HandleFunc("/health", g.healthHandler)
mux.HandleFunc("/api/tags", g.ollamaTagsHandler)
mux.HandleFunc("/api/", g.proxyHandler("ollama"))
mux.HandleFunc("/", notFound)
return loggingMiddleware(g.logger, "ollama", mux)
}

func (g *Gateway) proxyHandler(api string) http.HandlerFunc {
return func(w http.ResponseWriter, r *http.Request) {
if r.Method != http.MethodPost && r.Method != http.MethodGet {
http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
return
}
body, err := io.ReadAll(r.Body)
if err != nil {
http.Error(w, "invalid request body", http.StatusBadRequest)
return
}
_ = r.Body.Close()

info := parseRequestInfo(r.URL.Path, body)
resolved, err := g.router.Resolve(r.Context(), RouteInput{
Path:        r.URL.Path,
Model:       info.Model,
Prompt:      info.Prompt,
TokenCount:  info.TokenCount,
Metadata:    info.Metadata,
RequestBody: body,
})
if err != nil {
http.Error(w, err.Error(), http.StatusServiceUnavailable)
return
}

resp, backend, err := proxyWithFailover(r.Context(), g.client, r, body, resolved.Backends)
if err != nil {
g.logger.Error("routing failed", "api", api, "path", r.URL.Path, "model", info.Model, "tokens", info.TokenCount, "route", resolved.RouteName, "reason", resolved.Reason, "error", err)
http.Error(w, "all backends failed", http.StatusBadGateway)
return
}

g.logger.Info("route decision",
"api", api,
"route", resolved.RouteName,
"reason", resolved.Reason,
"strategy", resolved.Strategy,
"backend", backend.Spec.Name,
"model", info.Model,
"tokens", info.TokenCount,
"path", r.URL.Path,
)
if err := writeProxyResponse(w, resp); err != nil {
g.logger.Warn("write response failed", "backend", backend.Spec.Name, "error", err)
}
}
}

func (g *Gateway) healthHandler(w http.ResponseWriter, _ *http.Request) {
health := g.backends.HealthSnapshot()
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
set := map[string]struct{}{}
for _, b := range g.backends.All() {
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
for _, b := range g.backends.All() {
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

func writeJSON(w http.ResponseWriter, payload any) {
w.Header().Set("Content-Type", "application/json")
if err := json.NewEncoder(w).Encode(payload); err != nil {
http.Error(w, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
}
}

func notFound(w http.ResponseWriter, _ *http.Request) {
http.Error(w, "not found", http.StatusNotFound)
}

func loggingMiddleware(logger *slog.Logger, api string, next http.Handler) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
if slices.Contains([]string{"/health", "/v1/models", "/api/tags"}, r.URL.Path) {
next.ServeHTTP(w, r)
return
}
start := time.Now()
next.ServeHTTP(w, r)
logger.Debug("request", "api", api, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
})
}
