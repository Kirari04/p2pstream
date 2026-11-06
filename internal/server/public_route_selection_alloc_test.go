package server

import (
	"math/rand"
	"net/url"
	"testing"
)

// A sole eligible target still updates selector state. Otherwise adding another
// target later changes round-robin phase and seeded random choices.
func TestRouteTargetSelectionSingleCandidatePreservesState(t *testing.T) {
	algorithms := []string{
		publicRouteTargetLoadBalancingRoundRobin,
		publicRouteTargetLoadBalancingWeightedRoundRobin,
		publicRouteTargetLoadBalancingRandom,
		publicRouteTargetLoadBalancingWeightedRandom,
		publicRouteTargetLoadBalancingLeastActiveRequests,
		publicRouteTargetLoadBalancingWeightedLeastActiveRequests,
	}
	for _, algorithm := range algorithms {
		t.Run(algorithm, func(t *testing.T) {
			app := NewApp(nil, nil)
			app.LoadBalancers = newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(29)))
			reference := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(29)))
			first := publicRouteTargetConfig{ID: 11, Enabled: true, TargetType: publicRouteTargetTypeStatic, Position: 10, Weight: 3}
			second := publicRouteTargetConfig{ID: 12, Enabled: true, TargetType: publicRouteTargetTypeStatic, Position: 5, Weight: 2}
			route := publicRouteConfig{ID: 71, TargetLoadBalancing: algorithm, Targets: []publicRouteTargetConfig{first}}
			snap := &publicProxySnapshot{}
			for range 3 {
				selected, agent, ok := app.selectRouteTarget(snap, route)
				want, referenceOK := reference.selectRouteTarget(route, []routeTargetCandidate{{Target: first, TargetID: first.ID, Position: first.Position, Weight: first.Weight}})
				if !ok || !referenceOK || selected.ID != want.TargetID || agent != nil {
					t.Fatalf("single target selected=%d agent=%v ok=%v, want=%d", selected.ID, agent, ok, want.TargetID)
				}
			}
			// An ineligible-only request must not advance state.
			route.Targets[0].Enabled = false
			if _, _, ok := app.selectRouteTarget(snap, route); ok {
				t.Fatal("selected a disabled sole target")
			}
			route.Targets = []publicRouteTargetConfig{first, second}
			for i := range 32 {
				selected, _, ok := app.selectRouteTarget(snap, route)
				want, referenceOK := reference.selectRouteTarget(route, []routeTargetCandidate{
					{Target: first, TargetID: first.ID, Position: first.Position, Weight: first.Weight},
					{Target: second, TargetID: second.ID, Position: second.Position, Weight: second.Weight},
				})
				if !ok || !referenceOK || selected.ID != want.TargetID {
					t.Fatalf("expanded pick %d selected=%d ok=%v, want=%d", i, selected.ID, ok, want.TargetID)
				}
			}
		})
	}
}

func TestRouteTargetSelectionOrdersPositionAndID(t *testing.T) {
	app := NewApp(nil, nil)
	route := publicRouteConfig{ID: 72, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Targets: []publicRouteTargetConfig{
		{ID: 3, Enabled: true, TargetType: publicRouteTargetTypeStatic, Position: 10},
		{ID: 2, Enabled: true, TargetType: publicRouteTargetTypeStatic, Position: 0},
		{ID: 1, Enabled: true, TargetType: publicRouteTargetTypeStatic, Position: 0},
	}}
	for _, want := range []int64{1, 2, 3, 1} {
		selected, _, ok := app.selectRouteTarget(&publicProxySnapshot{}, route)
		if !ok || selected.ID != want {
			t.Fatalf("selected=%d ok=%v, want=%d", selected.ID, ok, want)
		}
	}
}

func BenchmarkPublicRouteTargetSelection(b *testing.B) {
	for _, test := range []struct {
		name       string
		targetType string
		count      int
	}{
		{name: "single_direct", targetType: publicRouteTargetTypeProxy, count: 1},
		{name: "single_static", targetType: publicRouteTargetTypeStatic, count: 1},
		{name: "two_direct", targetType: publicRouteTargetTypeProxy, count: 2},
	} {
		b.Run(test.name, func(b *testing.B) {
			app := NewApp(nil, nil)
			origin, err := url.Parse("http://127.0.0.1:8080")
			if err != nil {
				b.Fatal(err)
			}
			route := publicRouteConfig{ID: 71, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin}
			for i := range test.count {
				route.Targets = append(route.Targets, publicRouteTargetConfig{
					ID: int64(i + 1), Enabled: true, TargetType: test.targetType,
					Transport: publicRouteTargetTransportDirect, ParsedURL: origin,
					Position: int64(i), Weight: 100,
				})
			}
			snap := &publicProxySnapshot{}
			if _, _, ok := app.selectRouteTarget(snap, route); !ok {
				b.Fatal("warmup selected no target")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				selected, _, ok := app.selectRouteTarget(snap, route)
				if !ok || selected.ID == 0 {
					b.Fatal("selected no target")
				}
			}
		})
	}
}
