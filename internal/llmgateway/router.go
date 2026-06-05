package llmgateway

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
)

type RouteInput struct {
	Path        string
	Model       string
	Prompt      string
	TokenCount  int
	Metadata    map[string]string
	RequestBody []byte
}

type ResolvedRoute struct {
	RouteName string
	Strategy  string
	Backends  []*Backend
	Reason    string
}

type Router struct {
	cfg        Config
	backends   *BackendManager
	balancer   *Balancer
	classifier *Classifier
	logger     *slog.Logger
}

func NewRouter(cfg Config, backends *BackendManager, balancer *Balancer, logger *slog.Logger) *Router {
	return &Router{
		cfg:        cfg,
		backends:   backends,
		balancer:   balancer,
		classifier: NewClassifier(backends),
		logger:     logger,
	}
}

func (r *Router) Resolve(ctx context.Context, in RouteInput) (ResolvedRoute, error) {
	routes := append([]RouteSpec(nil), r.cfg.Routes...)
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Priority > routes[j].Priority })

	for _, candidate := range routes {
		if !r.matches(candidate, in) {
			continue
		}
		backendRefs := candidate.Backends
		reason := "rule-match"
		if candidate.Classifier != nil {
			picked, label, err := r.classifier.Classify(ctx, candidate, in)
			if err != nil {
				r.logger.Warn("classifier routing failed", "route", candidate.Name, "error", err)
			} else if len(picked) > 0 {
				backendRefs = picked
				reason = "classifier:" + label
			}
		}

		order := r.buildOrderedBackendList(candidate.Strategy, backendRefs, candidate.Fallback, in)
		if len(order) == 0 {
			continue
		}
		return ResolvedRoute{RouteName: candidate.Name, Strategy: candidate.Strategy, Backends: order, Reason: reason}, nil
	}

	fallback := r.buildOrderedBackendList(r.cfg.Defaults.Strategy, r.cfg.Defaults.Backends, r.cfg.Defaults.Fallback, in)
	if len(fallback) == 0 {
		fallback = r.buildOrderedBackendList(r.cfg.Defaults.Strategy, nil, nil, in)
	}
	if len(fallback) == 0 {
		return ResolvedRoute{}, fmt.Errorf("no healthy backend available")
	}
	return ResolvedRoute{RouteName: "defaults", Strategy: r.cfg.Defaults.Strategy, Backends: fallback, Reason: "default"}, nil
}

func (r *Router) buildOrderedBackendList(strategy string, primaryRefs, fallbackRefs []string, in RouteInput) []*Backend {
	seen := map[string]struct{}{}
	ordered := make([]*Backend, 0)
	addPool := func(pool []*Backend, strategy string) {
		for {
			selected := r.balancer.Select(strategy, pool, seen)
			if selected == nil {
				return
			}
			seen[selected.Spec.Name] = struct{}{}
			ordered = append(ordered, selected)
		}
	}

	primary := r.filterByContext(r.backends.Resolve(primaryRefs), in.TokenCount)
	if len(primary) == 0 && len(primaryRefs) > 0 {
		primary = r.backends.Resolve(primaryRefs)
	}
	addPool(primary, strategy)
	addPool(r.backends.Resolve(fallbackRefs), r.cfg.Defaults.Strategy)
	if len(primaryRefs) == 0 && len(fallbackRefs) == 0 {
		addPool(r.filterByContext(r.backends.AllHealthy(), in.TokenCount), strategy)
	}
	return ordered
}

func (r *Router) filterByContext(candidates []*Backend, tokenCount int) []*Backend {
	if tokenCount <= 0 {
		return candidates
	}
	filtered := make([]*Backend, 0, len(candidates))
	for _, b := range candidates {
		if b.Spec.ContextLength <= 0 || tokenCount <= b.Spec.ContextLength {
			filtered = append(filtered, b)
		}
	}
	return filtered
}

func (r *Router) matches(route RouteSpec, in RouteInput) bool {
	if len(route.Paths) > 0 {
		matched := false
		for _, p := range route.Paths {
			if ok, _ := path.Match(p, strings.TrimPrefix(in.Path, "/")); ok || strings.HasPrefix(in.Path, p) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if route.MinTokens > 0 && in.TokenCount < route.MinTokens {
		return false
	}
	if route.MaxTokens > 0 && in.TokenCount > route.MaxTokens {
		return false
	}
	if len(route.Keywords) > 0 {
		p := strings.ToLower(in.Prompt)
		keywordMatched := false
		for _, kw := range route.Keywords {
			if strings.Contains(p, strings.ToLower(kw)) {
				keywordMatched = true
				break
			}
		}
		if !keywordMatched {
			return false
		}
	}
	for _, c := range route.Conditions {
		if !evalCondition(c, in) {
			return false
		}
	}
	return true
}

func evalCondition(c ConditionSpec, in RouteInput) bool {
	field := strings.ToLower(strings.TrimSpace(c.Field))
	op := strings.ToLower(strings.TrimSpace(c.Operator))
	value := c.Value
	if op == "" {
		op = "eq"
	}
	left := ""
	switch field {
	case "path":
		left = in.Path
	case "model":
		left = in.Model
	case "prompt":
		left = in.Prompt
	case "tokens":
		left = strconv.Itoa(in.TokenCount)
	default:
		left = in.Metadata[field]
	}

	switch op {
	case "contains":
		return strings.Contains(strings.ToLower(left), strings.ToLower(value))
	case "gt", "gte", "lt", "lte":
		ln, err1 := strconv.Atoi(strings.TrimSpace(left))
		rn, err2 := strconv.Atoi(strings.TrimSpace(value))
		if err1 != nil || err2 != nil {
			return false
		}
		switch op {
		case "gt":
			return ln > rn
		case "gte":
			return ln >= rn
		case "lt":
			return ln < rn
		default:
			return ln <= rn
		}
	case "exists":
		return strings.TrimSpace(left) != ""
	default:
		return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(value))
	}
}
