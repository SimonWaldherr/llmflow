package llmgateway

import "testing"

func TestLeastConnectionsPrefersLessActive(t *testing.T) {
	t.Parallel()
	b := NewBalancer()
	cands := []*Backend{
		{Spec: BackendSpec{Name: "a", Weight: 1, Priority: 2}},
		{Spec: BackendSpec{Name: "b", Weight: 1, Priority: 1}},
	}
	cands[0].IncActive()
	cands[0].IncActive()
	selected := b.Select(StrategyLeastConnections, cands, map[string]struct{}{})
	if selected == nil || selected.Spec.Name != "b" {
		t.Fatalf("expected b, got %+v", selected)
	}
}

func TestWeightedRoundRobinUsesWeights(t *testing.T) {
	t.Parallel()
	b := NewBalancer()
	cands := []*Backend{
		{Spec: BackendSpec{Name: "a", Weight: 3}},
		{Spec: BackendSpec{Name: "b", Weight: 1}},
	}
	counts := map[string]int{}
	for i := 0; i < 40; i++ {
		sel := b.Select(StrategyWeightedRoundRobin, cands, map[string]struct{}{})
		counts[sel.Spec.Name]++
	}
	if counts["a"] <= counts["b"] {
		t.Fatalf("expected weighted preference for a, counts=%v", counts)
	}
}
