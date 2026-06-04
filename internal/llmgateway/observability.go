package llmgateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type DecisionEvent struct {
	Timestamp    time.Time `json:"timestamp"`
	RequestID    string    `json:"request_id"`
	API          string    `json:"api"`
	Path         string    `json:"path"`
	Model        string    `json:"model,omitempty"`
	Tokens       int       `json:"tokens"`
	RouteName    string    `json:"route_name"`
	Reason       string    `json:"reason"`
	Strategy     string    `json:"strategy"`
	Backend      string    `json:"backend"`
	Fallback     []string  `json:"fallback_chain,omitempty"`
	DurationMs   int64     `json:"duration_ms"`
	Status       int       `json:"status"`
	Success      bool      `json:"success"`
	Error        string    `json:"error,omitempty"`
	FailureCount int       `json:"failover_hops"`
}

type AuditEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Version   int64     `json:"version"`
	Detail    string    `json:"detail"`
}

type ConfigSnapshot struct {
	Version   int64     `json:"version"`
	AppliedAt time.Time `json:"applied_at"`
	Source    string    `json:"source"`
	RawYAML   string    `json:"raw_yaml"`
}

type Metrics struct {
	requestsTotal     atomic.Int64
	errorsTotal       atomic.Int64
	failoverTotal     atomic.Int64
	proxyLatencyNanos atomic.Int64

	mu              sync.RWMutex
	backendRequests map[string]int64
	backendErrors   map[string]int64
	routeHits       map[string]int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		backendRequests: map[string]int64{},
		backendErrors:   map[string]int64{},
		routeHits:       map[string]int64{},
	}
}

func (m *Metrics) RecordDecision(ev DecisionEvent) {
	m.requestsTotal.Add(1)
	if !ev.Success {
		m.errorsTotal.Add(1)
	}
	if ev.FailureCount > 0 {
		m.failoverTotal.Add(1)
	}
	m.proxyLatencyNanos.Add(ev.DurationMs * int64(time.Millisecond))

	m.mu.Lock()
	defer m.mu.Unlock()
	m.backendRequests[ev.Backend]++
	if !ev.Success {
		m.backendErrors[ev.Backend]++
	}
	m.routeHits[ev.RouteName]++
}

func (m *Metrics) Snapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	avgLatencyMs := int64(0)
	total := m.requestsTotal.Load()
	if total > 0 {
		avgLatencyMs = (m.proxyLatencyNanos.Load() / total) / int64(time.Millisecond)
	}
	return map[string]any{
		"requests_total":   total,
		"errors_total":     m.errorsTotal.Load(),
		"failover_total":   m.failoverTotal.Load(),
		"avg_latency_ms":   avgLatencyMs,
		"backend_requests": cloneIntMap(m.backendRequests),
		"backend_errors":   cloneIntMap(m.backendErrors),
		"route_hits":       cloneIntMap(m.routeHits),
	}
}

func (m *Metrics) Prometheus() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP llmgateway_requests_total Total proxied requests\n")
	fmt.Fprintf(&b, "# TYPE llmgateway_requests_total counter\n")
	fmt.Fprintf(&b, "llmgateway_requests_total %d\n", m.requestsTotal.Load())
	fmt.Fprintf(&b, "# HELP llmgateway_errors_total Total failed requests\n")
	fmt.Fprintf(&b, "# TYPE llmgateway_errors_total counter\n")
	fmt.Fprintf(&b, "llmgateway_errors_total %d\n", m.errorsTotal.Load())
	fmt.Fprintf(&b, "# HELP llmgateway_failover_total Requests with failover\n")
	fmt.Fprintf(&b, "# TYPE llmgateway_failover_total counter\n")
	fmt.Fprintf(&b, "llmgateway_failover_total %d\n", m.failoverTotal.Load())

	total := m.requestsTotal.Load()
	avgLatencyMs := int64(0)
	if total > 0 {
		avgLatencyMs = (m.proxyLatencyNanos.Load() / total) / int64(time.Millisecond)
	}
	fmt.Fprintf(&b, "# HELP llmgateway_latency_avg_ms Average proxy latency\n")
	fmt.Fprintf(&b, "# TYPE llmgateway_latency_avg_ms gauge\n")
	fmt.Fprintf(&b, "llmgateway_latency_avg_ms %d\n", avgLatencyMs)

	for _, key := range sortedKeys(m.backendRequests) {
		fmt.Fprintf(&b, "llmgateway_backend_requests_total{backend=%q} %d\n", key, m.backendRequests[key])
	}
	for _, key := range sortedKeys(m.backendErrors) {
		fmt.Fprintf(&b, "llmgateway_backend_errors_total{backend=%q} %d\n", key, m.backendErrors[key])
	}
	for _, key := range sortedKeys(m.routeHits) {
		fmt.Fprintf(&b, "llmgateway_route_hits_total{route=%q} %d\n", key, m.routeHits[key])
	}
	return b.String()
}

type EventHub struct {
	mu          sync.RWMutex
	subscribers map[chan string]struct{}
}

func NewEventHub() *EventHub {
	return &EventHub{subscribers: map[chan string]struct{}{}}
}

func (h *EventHub) Subscribe() chan string {
	ch := make(chan string, 16)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *EventHub) Unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.subscribers, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *EventHub) Publish(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subscribers {
		select {
		case ch <- string(payload):
		default:
		}
	}
}

func writeSSE(w http.ResponseWriter, r *http.Request, hub *EventHub) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	fmt.Fprint(w, "event: ping\ndata: {}\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-sub:
			fmt.Fprintf(w, "event: update\ndata: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func cloneIntMap(src map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
