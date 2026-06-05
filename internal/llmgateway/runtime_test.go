package llmgateway

import (
	"io"
	"log/slog"
	"testing"
)

func TestRuntimeApplyAndRollback(t *testing.T) {
	t.Parallel()
	base := Config{
		Servers:  ServerConfig{OpenAIAddr: ":1234", OllamaAddr: ":11434"},
		Admin:    AdminConfig{Addr: ":18080", MaxDecisionLog: 100, MaxSnapshots: 5},
		Defaults: DefaultConfig{Strategy: StrategyWeightedRoundRobin, Backends: []string{"a"}},
		Backends: []BackendSpec{{Name: "a", URL: "http://localhost:8001"}},
		Routes:   []RouteSpec{{Name: "default", Backends: []string{"a"}}},
	}
	base.applyDefaults()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rm, err := NewRuntimeManager(base, logger)
	if err != nil {
		t.Fatalf("NewRuntimeManager: %v", err)
	}

	updated := base
	updated.Backends = append(updated.Backends, BackendSpec{Name: "b", URL: "http://localhost:8002"})
	updated.Defaults.Backends = []string{"b"}
	updated.Routes = []RouteSpec{{Name: "new", Backends: []string{"b"}}}
	updated.applyDefaults()
	v2, err := rm.ApplyConfig(updated, "test", "tester")
	if err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if v2 < 2 {
		t.Fatalf("expected version >= 2, got %d", v2)
	}

	v3, err := rm.Rollback(1, "tester")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if v3 <= v2 {
		t.Fatalf("expected rollback to increment version, got %d <= %d", v3, v2)
	}
	if rm.Current().cfg.Defaults.Backends[0] != "a" {
		t.Fatalf("expected rollback to backend a, got %+v", rm.Current().cfg.Defaults.Backends)
	}
}
