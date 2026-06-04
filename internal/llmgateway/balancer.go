package llmgateway

import (
"math"
"strings"
"sync"
"sync/atomic"
)

const (
StrategyWeightedRoundRobin = "weighted_round_robin"
StrategyLeastConnections   = "least_connections"
)

type Balancer struct {
rrIndex atomic.Uint64

mu          sync.Mutex
smoothState map[string]int
}

func NewBalancer() *Balancer {
return &Balancer{smoothState: make(map[string]int)}
}

func (b *Balancer) Select(strategy string, candidates []*Backend, used map[string]struct{}) *Backend {
if len(candidates) == 0 {
return nil
}
strategy = strings.ToLower(strings.TrimSpace(strategy))
if strategy == "" {
strategy = StrategyWeightedRoundRobin
}
switch strategy {
case StrategyLeastConnections:
return b.selectLeastConnections(candidates, used)
default:
return b.selectWeightedRoundRobin(candidates, used)
}
}

func (b *Balancer) selectWeightedRoundRobin(candidates []*Backend, used map[string]struct{}) *Backend {
available := make([]*Backend, 0, len(candidates))
for _, c := range candidates {
if _, ok := used[c.Spec.Name]; ok {
continue
}
available = append(available, c)
}
if len(available) == 0 {
return nil
}

b.mu.Lock()
defer b.mu.Unlock()

totalWeight := 0
for _, c := range available {
totalWeight += max(1, c.Spec.Weight)
}

var best *Backend
bestWeight := math.MinInt
for _, c := range available {
name := c.Spec.Name
b.smoothState[name] += max(1, c.Spec.Weight)
if b.smoothState[name] > bestWeight {
bestWeight = b.smoothState[name]
best = c
}
}
if best != nil {
b.smoothState[best.Spec.Name] -= totalWeight
}
return best
}

func (b *Balancer) selectLeastConnections(candidates []*Backend, used map[string]struct{}) *Backend {
var selected *Backend
bestScore := math.MaxFloat64
for _, c := range candidates {
if _, ok := used[c.Spec.Name]; ok {
continue
}
score := float64(c.ActiveConnections()+1) / float64(max(1, c.Spec.Weight))
if score < bestScore || (score == bestScore && selected != nil && c.Spec.Priority < selected.Spec.Priority) {
selected = c
bestScore = score
}
}
return selected
}
