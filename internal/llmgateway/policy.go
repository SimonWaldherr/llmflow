package llmgateway

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

type ProxyContext struct {
	RequestID string
	API       string
	Input     RouteInput
	Resolved  ResolvedRoute
	Backend   *Backend
}

type Policy interface {
	Name() string
	BeforeRoute(*ProxyContext, *http.Request) error
	BeforeProxy(*ProxyContext, *http.Request) error
	AfterProxy(*ProxyContext, *http.Request, *http.Response, error)
}

type PolicyFactory func(PolicySpec) (Policy, error)

type PolicyRegistry struct {
	mu        sync.RWMutex
	factories map[string]PolicyFactory
}

func NewPolicyRegistry() *PolicyRegistry {
	r := &PolicyRegistry{factories: map[string]PolicyFactory{}}
	r.Register("header_required", newHeaderRequiredPolicy)
	return r
}

func (r *PolicyRegistry) Register(kind string, factory PolicyFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[strings.ToLower(strings.TrimSpace(kind))] = factory
}

func (r *PolicyRegistry) Build(specs []PolicySpec) ([]Policy, error) {
	policies := make([]Policy, 0, len(specs))
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		r.mu.RLock()
		factory, ok := r.factories[strings.ToLower(spec.Type)]
		r.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("policy type %q is not registered", spec.Type)
		}
		policy, err := factory(spec)
		if err != nil {
			return nil, fmt.Errorf("build policy %q: %w", spec.Name, err)
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

type PolicyChain struct {
	policies []Policy
}

func NewPolicyChain(policies []Policy) *PolicyChain {
	return &PolicyChain{policies: policies}
}

func (c *PolicyChain) BeforeRoute(pc *ProxyContext, r *http.Request) error {
	for _, p := range c.policies {
		if err := p.BeforeRoute(pc, r); err != nil {
			return fmt.Errorf("policy %s before-route: %w", p.Name(), err)
		}
	}
	return nil
}

func (c *PolicyChain) BeforeProxy(pc *ProxyContext, r *http.Request) error {
	for _, p := range c.policies {
		if err := p.BeforeProxy(pc, r); err != nil {
			return fmt.Errorf("policy %s before-proxy: %w", p.Name(), err)
		}
	}
	return nil
}

func (c *PolicyChain) AfterProxy(pc *ProxyContext, r *http.Request, resp *http.Response, err error) {
	for _, p := range c.policies {
		p.AfterProxy(pc, r, resp, err)
	}
}

type headerRequiredPolicy struct {
	name   string
	header string
}

func newHeaderRequiredPolicy(spec PolicySpec) (Policy, error) {
	header := strings.TrimSpace(spec.Config["header"])
	if header == "" {
		return nil, errors.New("config.header is required")
	}
	return &headerRequiredPolicy{name: spec.Name, header: header}, nil
}

func (p *headerRequiredPolicy) Name() string { return p.name }

func (p *headerRequiredPolicy) BeforeRoute(_ *ProxyContext, r *http.Request) error {
	if strings.TrimSpace(r.Header.Get(p.header)) == "" {
		return fmt.Errorf("missing required header %q", p.header)
	}
	return nil
}

func (p *headerRequiredPolicy) BeforeProxy(_ *ProxyContext, _ *http.Request) error { return nil }
func (p *headerRequiredPolicy) AfterProxy(_ *ProxyContext, _ *http.Request, _ *http.Response, _ error) {
}
