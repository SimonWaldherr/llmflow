package llmgateway

import (
"context"
"errors"
"fmt"
"net/http"
"net/url"
"sort"
"strings"
"sync"
"sync/atomic"
"time"
)

type Backend struct {
Spec BackendSpec
URL  *url.URL

activeConnections atomic.Int64

mu           sync.RWMutex
healthy      bool
lastError    string
lastChecked  time.Time
lastDuration time.Duration
}

type BackendManager struct {
backends map[string]*Backend
names    []string
}

func NewBackendManager(specs []BackendSpec) (*BackendManager, error) {
items := make(map[string]*Backend, len(specs))
for _, spec := range specs {
parsed, err := url.Parse(spec.URL)
if err != nil {
return nil, fmt.Errorf("parse backend %s url: %w", spec.Name, err)
}
if parsed.Scheme == "" || parsed.Host == "" {
return nil, fmt.Errorf("backend %s url must include scheme and host", spec.Name)
}
if spec.Disabled {
continue
}
items[spec.Name] = &Backend{Spec: spec, URL: parsed, healthy: true}
}
if len(items) == 0 {
return nil, errors.New("all backends are disabled")
}
names := make([]string, 0, len(items))
for name := range items {
names = append(names, name)
}
sort.Strings(names)
return &BackendManager{backends: items, names: names}, nil
}

func (m *BackendManager) Get(name string) (*Backend, bool) {
b, ok := m.backends[name]
return b, ok
}

func (m *BackendManager) All() []*Backend {
out := make([]*Backend, 0, len(m.names))
for _, name := range m.names {
out = append(out, m.backends[name])
}
return out
}

func (m *BackendManager) Resolve(names []string) []*Backend {
if len(names) == 0 {
return m.AllHealthy()
}
out := make([]*Backend, 0, len(names))
for _, name := range names {
if b, ok := m.backends[name]; ok && b.IsHealthy() {
out = append(out, b)
}
}
return out
}

func (m *BackendManager) AllHealthy() []*Backend {
out := make([]*Backend, 0, len(m.backends))
for _, name := range m.names {
b := m.backends[name]
if b.IsHealthy() {
out = append(out, b)
}
}
return out
}

func (m *BackendManager) HealthSnapshot() map[string]map[string]any {
result := make(map[string]map[string]any, len(m.backends))
for _, name := range m.names {
b := m.backends[name]
result[name] = map[string]any{
"healthy":             b.IsHealthy(),
"last_error":          b.LastError(),
"active_connections":  b.ActiveConnections(),
"last_check_ms":       b.LastDuration().Milliseconds(),
"last_check_timestamp": b.LastChecked(),
}
}
return result
}

func (m *BackendManager) StartHealthChecks(ctx context.Context, hc HealthConfig, client *http.Client, onCheck func(*Backend)) {
ticker := time.NewTicker(hc.Interval)
defer ticker.Stop()
for {
for _, b := range m.All() {
m.checkBackend(ctx, b, hc, client)
if onCheck != nil {
onCheck(b)
}
}
select {
case <-ctx.Done():
return
case <-ticker.C:
}
}
}

func (m *BackendManager) checkBackend(ctx context.Context, b *Backend, hc HealthConfig, client *http.Client) {
probeCtx, cancel := context.WithTimeout(ctx, hc.Timeout)
defer cancel()

start := time.Now()
status, errText := probeBackend(probeCtx, client, b)
d := time.Since(start)

b.mu.Lock()
defer b.mu.Unlock()
b.lastChecked = time.Now()
b.lastDuration = d
if errText != "" || status >= 500 {
b.healthy = false
if errText != "" {
b.lastError = errText
} else {
b.lastError = fmt.Sprintf("health status %d", status)
}
return
}
b.healthy = true
b.lastError = ""
}

func probeBackend(ctx context.Context, client *http.Client, b *Backend) (status int, errText string) {
probeURL := *b.URL
basePath := strings.TrimRight(probeURL.Path, "/")
path := b.Spec.HealthPath
if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(path, "/v1/") {
probeURL.Path = basePath + strings.TrimPrefix(path, "/v1")
} else {
probeURL.Path = strings.TrimRight(basePath, "/") + path
}
if probeURL.Path == "" {
probeURL.Path = "/"
}
req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL.String(), nil)
if err != nil {
return 0, err.Error()
}
resp, err := client.Do(req)
if err != nil {
return 0, err.Error()
}
defer resp.Body.Close()
return resp.StatusCode, ""
}

func (b *Backend) IsHealthy() bool {
b.mu.RLock()
defer b.mu.RUnlock()
return b.healthy
}

func (b *Backend) LastError() string {
b.mu.RLock()
defer b.mu.RUnlock()
return b.lastError
}

func (b *Backend) LastChecked() time.Time {
b.mu.RLock()
defer b.mu.RUnlock()
return b.lastChecked
}

func (b *Backend) LastDuration() time.Duration {
b.mu.RLock()
defer b.mu.RUnlock()
return b.lastDuration
}

func (b *Backend) ActiveConnections() int64 {
return b.activeConnections.Load()
}

func (b *Backend) IncActive() {
b.activeConnections.Add(1)
}

func (b *Backend) DecActive() {
b.activeConnections.Add(-1)
}

func (b *Backend) MarkFailure(err error) {
b.mu.Lock()
defer b.mu.Unlock()
b.healthy = false
if err != nil {
b.lastError = err.Error()
}
}

func (b *Backend) MarkSuccess() {
b.mu.Lock()
defer b.mu.Unlock()
b.healthy = true
b.lastError = ""
}
