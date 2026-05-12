package server

import (
	"math/rand"
	"sync"
	"testing"
)

func TestLoadBalancerRoundRobin(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	backend := publicBackendConfig{ID: 10, LoadBalancing: publicBackendLoadBalancingRoundRobin}
	candidates := testCandidates(1, 2, 3)

	got := []int64{
		registry.selectAgent(backend, candidates).AgentID,
		registry.selectAgent(backend, candidates).AgentID,
		registry.selectAgent(backend, candidates).AgentID,
		registry.selectAgent(backend, candidates).AgentID,
	}
	want := []int64{1, 2, 3, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round robin pick %d = %d, want %d (all picks=%v)", i, got[i], want[i], got)
		}
	}
}

func TestLoadBalancerWeightedRoundRobin(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	backend := publicBackendConfig{ID: 11, LoadBalancing: publicBackendLoadBalancingWeightedRoundRobin}
	candidates := []backendAgentCandidate{
		testCandidate(1, 0, 3),
		testCandidate(2, 1, 1),
	}

	counts := map[int64]int{}
	for range 8 {
		counts[registry.selectAgent(backend, candidates).AgentID]++
	}
	if counts[1] != 6 || counts[2] != 2 {
		t.Fatalf("weighted round robin counts = %+v, want 6/2", counts)
	}
}

func TestLoadBalancerRandomAndWeightedRandom(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(7)))
	randomBackend := publicBackendConfig{ID: 12, LoadBalancing: publicBackendLoadBalancingRandom}
	weightedBackend := publicBackendConfig{ID: 13, LoadBalancing: publicBackendLoadBalancingWeightedRandom}
	candidates := []backendAgentCandidate{
		testCandidate(1, 0, 1),
		testCandidate(2, 1, 1000),
	}

	seenRandom := map[int64]bool{}
	for range 20 {
		seenRandom[registry.selectAgent(randomBackend, candidates).AgentID] = true
	}
	if !seenRandom[1] || !seenRandom[2] {
		t.Fatalf("random selector did not select both agents: %+v", seenRandom)
	}

	counts := map[int64]int{}
	for range 50 {
		counts[registry.selectAgent(weightedBackend, candidates).AgentID]++
	}
	if counts[2] <= counts[1] {
		t.Fatalf("weighted random counts = %+v, expected high-weight agent to dominate", counts)
	}
}

func TestLoadBalancerLeastActive(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	backend := publicBackendConfig{ID: 14, LoadBalancing: publicBackendLoadBalancingLeastActiveRequests}
	candidates := testCandidates(1, 2, 3)
	candidates[0].Conn.ActiveRequests.Store(4)
	candidates[1].Conn.ActiveRequests.Store(1)
	candidates[2].Conn.ActiveRequests.Store(7)

	got := registry.selectAgent(backend, candidates)
	if got.AgentID != 2 {
		t.Fatalf("least active selected %d, want 2", got.AgentID)
	}
}

func TestLoadBalancerWeightedLeastActive(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	backend := publicBackendConfig{ID: 15, LoadBalancing: publicBackendLoadBalancingWeightedLeastActiveRequests}
	candidates := []backendAgentCandidate{
		testCandidate(1, 0, 1),
		testCandidate(2, 1, 10),
	}
	candidates[0].Conn.ActiveRequests.Store(2)
	candidates[1].Conn.ActiveRequests.Store(5)

	got := registry.selectAgent(backend, candidates)
	if got.AgentID != 2 {
		t.Fatalf("weighted least active selected %d, want 2", got.AgentID)
	}
}

func TestLoadBalancerConcurrentSelection(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	backend := publicBackendConfig{ID: 16, LoadBalancing: publicBackendLoadBalancingWeightedRoundRobin}
	candidates := testCandidates(1, 2, 3)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if registry.selectAgent(backend, candidates) == nil {
					t.Error("selector returned nil")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestRouteLoadBalancerRoundRobin(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	route := publicRouteConfig{ID: 20, LoadBalancing: publicBackendLoadBalancingRoundRobin}
	candidates := testRouteCandidates(1, 2, 3)

	got := []int64{}
	for range 4 {
		pick, ok := registry.selectRouteBackend(route, candidates)
		if !ok {
			t.Fatal("route selector returned no backend")
		}
		got = append(got, pick.BackendID)
	}
	want := []int64{1, 2, 3, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route round robin pick %d = %d, want %d (all picks=%v)", i, got[i], want[i], got)
		}
	}
}

func TestRouteLoadBalancerWeightedAlgorithms(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(7)))
	roundRobinRoute := publicRouteConfig{ID: 21, LoadBalancing: publicBackendLoadBalancingWeightedRoundRobin}
	randomRoute := publicRouteConfig{ID: 22, LoadBalancing: publicBackendLoadBalancingWeightedRandom}
	candidates := []routeBackendCandidate{
		testRouteCandidate(1, 0, 3, 0),
		testRouteCandidate(2, 1, 1, 0),
	}

	counts := map[int64]int{}
	for range 8 {
		pick, ok := registry.selectRouteBackend(roundRobinRoute, candidates)
		if !ok {
			t.Fatal("weighted route selector returned no backend")
		}
		counts[pick.BackendID]++
	}
	if counts[1] != 6 || counts[2] != 2 {
		t.Fatalf("route weighted round robin counts = %+v, want 6/2", counts)
	}

	counts = map[int64]int{}
	for range 50 {
		pick, ok := registry.selectRouteBackend(randomRoute, candidates)
		if !ok {
			t.Fatal("weighted random route selector returned no backend")
		}
		counts[pick.BackendID]++
	}
	if counts[1] <= counts[2] {
		t.Fatalf("route weighted random counts = %+v, expected high-weight backend to dominate", counts)
	}
}

func TestRouteLoadBalancerLeastActive(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	route := publicRouteConfig{ID: 23, LoadBalancing: publicBackendLoadBalancingLeastActiveRequests}
	candidates := []routeBackendCandidate{
		testRouteCandidate(1, 0, 1, 4),
		testRouteCandidate(2, 1, 1, 1),
		testRouteCandidate(3, 2, 1, 7),
	}

	pick, ok := registry.selectRouteBackend(route, candidates)
	if !ok {
		t.Fatal("least-active route selector returned no backend")
	}
	if pick.BackendID != 2 {
		t.Fatalf("route least active selected %d, want 2", pick.BackendID)
	}
}

func TestRouteAndAgentLoadBalancerStateIsolation(t *testing.T) {
	registry := newLoadBalancerRegistryWithRand(rand.New(rand.NewSource(1)))
	agentBackend := publicBackendConfig{ID: 30, LoadBalancing: publicBackendLoadBalancingRoundRobin}
	route := publicRouteConfig{ID: 30, LoadBalancing: publicBackendLoadBalancingRoundRobin}

	if got := registry.selectAgent(agentBackend, testCandidates(10, 11)).AgentID; got != 10 {
		t.Fatalf("agent first pick = %d, want 10", got)
	}
	routePick, ok := registry.selectRouteBackend(route, testRouteCandidates(20, 21))
	if !ok {
		t.Fatal("route selector returned no backend")
	}
	if routePick.BackendID != 20 {
		t.Fatalf("route first pick = %d, want 20", routePick.BackendID)
	}
}

func testCandidates(ids ...int64) []backendAgentCandidate {
	resp := make([]backendAgentCandidate, 0, len(ids))
	for idx, id := range ids {
		resp = append(resp, testCandidate(id, int64(idx), 1))
	}
	return resp
}

func testRouteCandidates(ids ...int64) []routeBackendCandidate {
	resp := make([]routeBackendCandidate, 0, len(ids))
	for idx, id := range ids {
		resp = append(resp, testRouteCandidate(id, int64(idx), 1, 0))
	}
	return resp
}

func testRouteCandidate(id int64, position int64, weight int64, active int64) routeBackendCandidate {
	return routeBackendCandidate{
		Backend:        publicBackendConfig{ID: id, Enabled: true},
		BackendID:      id,
		Position:       position,
		Weight:         weight,
		ActiveRequests: active,
	}
}

func testCandidate(id int64, position int64, weight int64) backendAgentCandidate {
	return backendAgentCandidate{
		Conn:     &AgentConn{AgentID: id, PublicID: "agent"},
		AgentID:  id,
		Position: position,
		Weight:   weight,
	}
}
