package llmgateway

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type runtimeState struct {
	cfg       Config
	backends  *BackendManager
	router    *Router
	policies  *PolicyChain
	updatedAt time.Time
}

type RuntimeManager struct {
	logger   *slog.Logger
	registry *PolicyRegistry
	version  atomic.Int64

	mu          sync.RWMutex
	state       *runtimeState
	snapshots   []ConfigSnapshot
	audit       []AuditEvent
	decisions   []DecisionEvent
	featureFlag map[string]bool
}

func NewRuntimeManager(cfg Config, logger *slog.Logger) (*RuntimeManager, error) {
	rm := &RuntimeManager{
		logger:      logger,
		registry:    NewPolicyRegistry(),
		featureFlag: cloneBoolMap(cfg.Admin.FeatureFlags),
	}
	if _, err := rm.apply(cfg, "startup", "system", true); err != nil {
		return nil, err
	}
	return rm, nil
}

func (rm *RuntimeManager) Current() *runtimeState {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.state
}

func (rm *RuntimeManager) ConfigYAML() string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	if rm.state == nil {
		return ""
	}
	b, _ := rm.state.cfg.ToYAML()
	return string(b)
}

func (rm *RuntimeManager) ApplyConfig(cfg Config, source, actor string) (int64, error) {
	return rm.apply(cfg, source, actor, false)
}

func (rm *RuntimeManager) apply(cfg Config, source, actor string, startup bool) (int64, error) {
	backends, err := NewBackendManager(cfg.Backends)
	if err != nil {
		return 0, err
	}
	policies, err := rm.registry.Build(cfg.Policies)
	if err != nil {
		return 0, err
	}
	balancer := NewBalancer()
	router := NewRouter(cfg, backends, balancer, rm.logger)

	rm.mu.Lock()
	defer rm.mu.Unlock()

	version := rm.version.Add(1)
	rm.state = &runtimeState{
		cfg:       cfg,
		backends:  backends,
		router:    router,
		policies:  NewPolicyChain(policies),
		updatedAt: time.Now().UTC(),
	}
	rm.featureFlag = cloneBoolMap(cfg.Admin.FeatureFlags)

	raw, _ := cfg.ToYAML()
	rm.snapshots = append(rm.snapshots, ConfigSnapshot{
		Version:   version,
		AppliedAt: time.Now().UTC(),
		Source:    source,
		RawYAML:   string(raw),
	})
	maxSnapshots := cfg.Admin.MaxSnapshots
	if maxSnapshots <= 0 {
		maxSnapshots = 20
	}
	if len(rm.snapshots) > maxSnapshots {
		rm.snapshots = rm.snapshots[len(rm.snapshots)-maxSnapshots:]
	}
	action := "config.apply"
	if startup {
		action = "config.startup"
	}
	rm.audit = append(rm.audit, AuditEvent{
		Timestamp: time.Now().UTC(),
		Action:    action,
		Actor:     actor,
		Version:   version,
		Detail:    fmt.Sprintf("source=%s backends=%d routes=%d policies=%d", source, len(cfg.Backends), len(cfg.Routes), len(cfg.Policies)),
	})
	rm.trimAuditLocked(200)
	return version, nil
}

func (rm *RuntimeManager) Rollback(version int64, actor string) (int64, error) {
	rm.mu.RLock()
	var snapshot *ConfigSnapshot
	for i := len(rm.snapshots) - 1; i >= 0; i-- {
		if rm.snapshots[i].Version == version {
			copy := rm.snapshots[i]
			snapshot = &copy
			break
		}
	}
	rm.mu.RUnlock()
	if snapshot == nil {
		return 0, fmt.Errorf("snapshot version %d not found", version)
	}
	cfg, err := LoadConfigBytes([]byte(snapshot.RawYAML))
	if err != nil {
		return 0, fmt.Errorf("rollback parse failed: %w", err)
	}
	newVersion, err := rm.apply(cfg, fmt.Sprintf("rollback:%d", version), actor, false)
	if err != nil {
		return 0, err
	}
	rm.mu.Lock()
	rm.audit = append(rm.audit, AuditEvent{
		Timestamp: time.Now().UTC(),
		Action:    "config.rollback",
		Actor:     actor,
		Version:   newVersion,
		Detail:    fmt.Sprintf("rolled back to version %d", version),
	})
	rm.trimAuditLocked(200)
	rm.mu.Unlock()
	return newVersion, nil
}

func (rm *RuntimeManager) AddDecision(ev DecisionEvent) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.decisions = append(rm.decisions, ev)
	limit := rm.state.cfg.Admin.MaxDecisionLog
	if limit <= 0 {
		limit = 200
	}
	if len(rm.decisions) > limit {
		rm.decisions = rm.decisions[len(rm.decisions)-limit:]
	}
}

func (rm *RuntimeManager) Decisions(limit int) []DecisionEvent {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	max := len(rm.decisions)
	if state := rm.state; state != nil && state.cfg.Admin.MaxDecisionLog > 0 && state.cfg.Admin.MaxDecisionLog < max {
		max = state.cfg.Admin.MaxDecisionLog
	}
	if limit <= 0 || limit > len(rm.decisions) {
		limit = len(rm.decisions)
	}
	if limit > max {
		limit = max
	}
	start := len(rm.decisions) - limit
	out := make([]DecisionEvent, limit)
	copy(out, rm.decisions[start:])
	return out
}

func (rm *RuntimeManager) Snapshots() []ConfigSnapshot {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	out := make([]ConfigSnapshot, len(rm.snapshots))
	copy(out, rm.snapshots)
	return out
}

func (rm *RuntimeManager) Audit() []AuditEvent {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	out := make([]AuditEvent, len(rm.audit))
	copy(out, rm.audit)
	return out
}

func (rm *RuntimeManager) FeatureFlags() map[string]bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return cloneBoolMap(rm.featureFlag)
}

func (rm *RuntimeManager) SetFeatureFlag(name string, enabled bool, actor string) error {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return fmt.Errorf("feature flag name is required")
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.featureFlag[name] = enabled
	rm.audit = append(rm.audit, AuditEvent{
		Timestamp: time.Now().UTC(),
		Action:    "feature-flag.set",
		Actor:     actor,
		Version:   rm.version.Load(),
		Detail:    fmt.Sprintf("%s=%t", name, enabled),
	})
	rm.trimAuditLocked(200)
	return nil
}

func (rm *RuntimeManager) trimAuditLocked(limit int) {
	if len(rm.audit) > limit {
		rm.audit = rm.audit[len(rm.audit)-limit:]
	}
}

func cloneBoolMap(src map[string]bool) map[string]bool {
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
