package llmgateway

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestRouterKeywordAndFallback(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Defaults: DefaultConfig{Strategy: StrategyWeightedRoundRobin, Backends: []string{"general"}},
		Backends: []BackendSpec{
			{Name: "general", URL: "http://localhost:8001", Weight: 1},
			{Name: "coder", URL: "http://localhost:8002", Weight: 1},
		},
		Routes: []RouteSpec{
			{Name: "coding", Keywords: []string{"code", "debug"}, Backends: []string{"coder"}, Fallback: []string{"general"}, Strategy: StrategyLeastConnections},
		},
	}
	manager, err := NewBackendManager(cfg.Backends)
	if err != nil {
		t.Fatalf("NewBackendManager: %v", err)
	}
	router := NewRouter(cfg, manager, NewBalancer(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	resolved, err := router.Resolve(context.Background(), RouteInput{Path: "/v1/chat/completions", Prompt: "please debug this code", TokenCount: 10})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.RouteName != "coding" {
		t.Fatalf("expected coding route, got %s", resolved.RouteName)
	}
	if len(resolved.Backends) < 2 {
		t.Fatalf("expected primary+fallback candidates, got %d", len(resolved.Backends))
	}
	if resolved.Backends[0].Spec.Name != "coder" {
		t.Fatalf("expected coder first, got %s", resolved.Backends[0].Spec.Name)
	}
}

func TestRouterTokenBasedRoute(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Defaults: DefaultConfig{Strategy: StrategyWeightedRoundRobin, Backends: []string{"small"}},
		Backends: []BackendSpec{
			{Name: "small", URL: "http://localhost:8001", ContextLength: 2048},
			{Name: "large", URL: "http://localhost:8002", ContextLength: 16000},
		},
		Routes: []RouteSpec{{Name: "long-context", MinTokens: 4000, Backends: []string{"large"}, Strategy: StrategyWeightedRoundRobin}},
	}
	manager, _ := NewBackendManager(cfg.Backends)
	router := NewRouter(cfg, manager, NewBalancer(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	resolved, err := router.Resolve(context.Background(), RouteInput{Path: "/v1/chat/completions", Prompt: "x", TokenCount: 5000})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Backends[0].Spec.Name != "large" {
		t.Fatalf("expected large backend, got %s", resolved.Backends[0].Spec.Name)
	}
}
