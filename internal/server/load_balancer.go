package server

import (
	"math/rand"
	"sort"
	"sync"
	"time"
)

type backendAgentCandidate struct {
	Conn     *AgentConn
	AgentID  int64
	Position int64
	Weight   int64
}

type routeBackendCandidate struct {
	Backend        publicBackendConfig
	BackendID      int64
	Position       int64
	Weight         int64
	ActiveRequests int64
}

type loadBalancerRegistry struct {
	mu          sync.Mutex
	states      map[int64]*backendSelectorState
	routeStates map[int64]*backendSelectorState
	rng         *rand.Rand
}

type backendSelectorState struct {
	roundRobin uint64
	smooth     map[int64]int64
}

func newLoadBalancerRegistry() *loadBalancerRegistry {
	return &loadBalancerRegistry{
		states:      make(map[int64]*backendSelectorState),
		routeStates: make(map[int64]*backendSelectorState),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func newLoadBalancerRegistryWithRand(rng *rand.Rand) *loadBalancerRegistry {
	return &loadBalancerRegistry{
		states:      make(map[int64]*backendSelectorState),
		routeStates: make(map[int64]*backendSelectorState),
		rng:         rng,
	}
}

func (r *loadBalancerRegistry) reconcile(snap *publicProxySnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if snap == nil {
		r.states = make(map[int64]*backendSelectorState)
		r.routeStates = make(map[int64]*backendSelectorState)
		return
	}
	for backendID := range r.states {
		if _, ok := snap.Backends[backendID]; !ok {
			delete(r.states, backendID)
		}
	}
	activeRoutes := make(map[int64]struct{})
	for _, routes := range snap.RoutesByListener {
		for _, route := range routes {
			activeRoutes[route.ID] = struct{}{}
		}
	}
	for routeID := range r.routeStates {
		if _, ok := activeRoutes[routeID]; !ok {
			delete(r.routeStates, routeID)
		}
	}
}

func (r *loadBalancerRegistry) selectAgent(backend publicBackendConfig, candidates []backendAgentCandidate) *AgentConn {
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Position == candidates[j].Position {
			return candidates[i].AgentID < candidates[j].AgentID
		}
		return candidates[i].Position < candidates[j].Position
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.states[backend.ID]
	if state == nil {
		state = &backendSelectorState{smooth: make(map[int64]int64)}
		r.states[backend.ID] = state
	}

	switch backend.LoadBalancing {
	case publicBackendLoadBalancingRandom:
		return candidates[r.rng.Intn(len(candidates))].Conn
	case publicBackendLoadBalancingWeightedRandom:
		return weightedRandom(candidates, r.rng).Conn
	case publicBackendLoadBalancingLeastActiveRequests:
		return state.leastActive(candidates, false).Conn
	case publicBackendLoadBalancingWeightedLeastActiveRequests:
		return state.leastActive(candidates, true).Conn
	case publicBackendLoadBalancingWeightedRoundRobin:
		return state.weightedRoundRobin(candidates).Conn
	default:
		return state.roundRobinPick(candidates).Conn
	}
}

func (r *loadBalancerRegistry) selectRouteBackend(route publicRouteConfig, candidates []routeBackendCandidate) (routeBackendCandidate, bool) {
	if len(candidates) == 0 {
		return routeBackendCandidate{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Position == candidates[j].Position {
			return candidates[i].BackendID < candidates[j].BackendID
		}
		return candidates[i].Position < candidates[j].Position
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.routeStates[route.ID]
	if state == nil {
		state = &backendSelectorState{smooth: make(map[int64]int64)}
		r.routeStates[route.ID] = state
	}

	switch route.LoadBalancing {
	case publicBackendLoadBalancingRandom:
		return candidates[r.rng.Intn(len(candidates))], true
	case publicBackendLoadBalancingWeightedRandom:
		return weightedRandomRoute(candidates, r.rng), true
	case publicBackendLoadBalancingLeastActiveRequests:
		return state.leastActiveRoute(candidates, false), true
	case publicBackendLoadBalancingWeightedLeastActiveRequests:
		return state.leastActiveRoute(candidates, true), true
	case publicBackendLoadBalancingWeightedRoundRobin:
		return state.weightedRouteRoundRobin(candidates), true
	default:
		return state.roundRobinRoutePick(candidates), true
	}
}

func (s *backendSelectorState) roundRobinPick(candidates []backendAgentCandidate) backendAgentCandidate {
	pick := candidates[int(s.roundRobin%uint64(len(candidates)))]
	s.roundRobin++
	return pick
}

func (s *backendSelectorState) roundRobinRoutePick(candidates []routeBackendCandidate) routeBackendCandidate {
	pick := candidates[int(s.roundRobin%uint64(len(candidates)))]
	s.roundRobin++
	return pick
}

func (s *backendSelectorState) weightedRoundRobin(candidates []backendAgentCandidate) backendAgentCandidate {
	if s.smooth == nil {
		s.smooth = make(map[int64]int64)
	}
	active := make(map[int64]struct{}, len(candidates))
	var total int64
	var best backendAgentCandidate
	bestSet := false
	for _, candidate := range candidates {
		weight := normalizedWeight(candidate.Weight)
		active[candidate.AgentID] = struct{}{}
		total += weight
		current := s.smooth[candidate.AgentID] + weight
		s.smooth[candidate.AgentID] = current
		if !bestSet || current > s.smooth[best.AgentID] ||
			(current == s.smooth[best.AgentID] && candidate.Position < best.Position) {
			best = candidate
			bestSet = true
		}
	}
	for agentID := range s.smooth {
		if _, ok := active[agentID]; !ok {
			delete(s.smooth, agentID)
		}
	}
	s.smooth[best.AgentID] -= total
	return best
}

func (s *backendSelectorState) weightedRouteRoundRobin(candidates []routeBackendCandidate) routeBackendCandidate {
	if s.smooth == nil {
		s.smooth = make(map[int64]int64)
	}
	active := make(map[int64]struct{}, len(candidates))
	var total int64
	var best routeBackendCandidate
	bestSet := false
	for _, candidate := range candidates {
		weight := normalizedWeight(candidate.Weight)
		active[candidate.BackendID] = struct{}{}
		total += weight
		current := s.smooth[candidate.BackendID] + weight
		s.smooth[candidate.BackendID] = current
		if !bestSet || current > s.smooth[best.BackendID] ||
			(current == s.smooth[best.BackendID] && candidate.Position < best.Position) {
			best = candidate
			bestSet = true
		}
	}
	for backendID := range s.smooth {
		if _, ok := active[backendID]; !ok {
			delete(s.smooth, backendID)
		}
	}
	s.smooth[best.BackendID] -= total
	return best
}

func (s *backendSelectorState) leastActive(candidates []backendAgentCandidate, weighted bool) backendAgentCandidate {
	tied := make([]backendAgentCandidate, 0, len(candidates))
	best := candidates[0]
	for _, candidate := range candidates {
		cmp := compareActive(candidate, best, weighted)
		if cmp < 0 {
			best = candidate
			tied = tied[:0]
			tied = append(tied, candidate)
			continue
		}
		if cmp == 0 {
			tied = append(tied, candidate)
		}
	}
	if len(tied) == 0 {
		return best
	}
	if weighted {
		return s.weightedRoundRobin(tied)
	}
	return s.roundRobinPick(tied)
}

func (s *backendSelectorState) leastActiveRoute(candidates []routeBackendCandidate, weighted bool) routeBackendCandidate {
	tied := make([]routeBackendCandidate, 0, len(candidates))
	best := candidates[0]
	for _, candidate := range candidates {
		cmp := compareRouteActive(candidate, best, weighted)
		if cmp < 0 {
			best = candidate
			tied = tied[:0]
			tied = append(tied, candidate)
			continue
		}
		if cmp == 0 {
			tied = append(tied, candidate)
		}
	}
	if len(tied) == 0 {
		return best
	}
	if weighted {
		return s.weightedRouteRoundRobin(tied)
	}
	return s.roundRobinRoutePick(tied)
}

func compareActive(a, b backendAgentCandidate, weighted bool) int {
	activeA := a.Conn.ActiveRequests.Load()
	activeB := b.Conn.ActiveRequests.Load()
	if weighted {
		left := activeA * normalizedWeight(b.Weight)
		right := activeB * normalizedWeight(a.Weight)
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		}
	} else {
		switch {
		case activeA < activeB:
			return -1
		case activeA > activeB:
			return 1
		}
	}
	return 0
}

func compareRouteActive(a, b routeBackendCandidate, weighted bool) int {
	activeA := a.ActiveRequests
	activeB := b.ActiveRequests
	if weighted {
		left := activeA * normalizedWeight(b.Weight)
		right := activeB * normalizedWeight(a.Weight)
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		}
	} else {
		switch {
		case activeA < activeB:
			return -1
		case activeA > activeB:
			return 1
		}
	}
	return 0
}

func weightedRandom(candidates []backendAgentCandidate, rng *rand.Rand) backendAgentCandidate {
	var total int64
	for _, candidate := range candidates {
		total += normalizedWeight(candidate.Weight)
	}
	if total <= 0 {
		return candidates[rng.Intn(len(candidates))]
	}
	pick := rng.Int63n(total)
	for _, candidate := range candidates {
		pick -= normalizedWeight(candidate.Weight)
		if pick < 0 {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func weightedRandomRoute(candidates []routeBackendCandidate, rng *rand.Rand) routeBackendCandidate {
	var total int64
	for _, candidate := range candidates {
		total += normalizedWeight(candidate.Weight)
	}
	if total <= 0 {
		return candidates[rng.Intn(len(candidates))]
	}
	pick := rng.Int63n(total)
	for _, candidate := range candidates {
		pick -= normalizedWeight(candidate.Weight)
		if pick < 0 {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func normalizedWeight(weight int64) int64 {
	if weight < 1 {
		return 1
	}
	if weight > 1000 {
		return 1000
	}
	return weight
}
