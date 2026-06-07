package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

const (
	publicRouteTargetPassiveUnhealthyCooldown  = 30 * time.Second
	publicRouteTargetHealthTraceLimitPerTarget = 100
)

type publicRouteTargetHealthSnapshot struct {
	Status                          p2pstreamv1.PublicRouteTargetHealthStatus
	LastCheckedAtUnixMillis         int64
	LastError                       string
	PassiveUnhealthyUntilUnixMillis int64
}

type publicRouteTargetAgentHealthSnapshot struct {
	Status                          p2pstreamv1.PublicRouteTargetHealthStatus
	Connected                       bool
	Available                       bool
	LastCheckedAtUnixMillis         int64
	LastError                       string
	PassiveUnhealthyUntilUnixMillis int64
	ActiveRequests                  int64
}

type publicRouteTargetHealthMonitor struct {
	mu       sync.Mutex
	app      *App
	sequence uint64
	states   map[int64]*publicRouteTargetHealthState
}

type publicRouteTargetHealthState struct {
	target             publicRouteTargetHealthConfig
	directCancel       context.CancelFunc
	directCheckRunning bool
	direct             publicRouteTargetCheckState
	directTraces       []*p2pstreamv1.PublicRouteTargetHealthTrace
	agentStates        map[int64]*publicRouteTargetAgentHealthState
	activeRequests     atomic.Int64
}

type publicRouteTargetAgentHealthState struct {
	agentID       int64
	agentPublicID string
	connected     bool
	cancel        context.CancelFunc
	checkRunning  bool
	state         publicRouteTargetCheckState
	traces        []*p2pstreamv1.PublicRouteTargetHealthTrace
}

type publicRouteTargetCheckState struct {
	explicitStatus        p2pstreamv1.PublicRouteTargetHealthStatus
	lastCheckedAt         time.Time
	lastError             string
	passiveUnhealthyUntil time.Time
	healthyStreak         int64
	unhealthyStreak       int64
}

type publicRouteTargetHealthCheckAttempt struct {
	StartedAt       time.Time
	FinishedAt      time.Time
	Method          string
	URL             string
	StatusCode      int64
	ExpectedMin     int64
	ExpectedMax     int64
	Timeout         time.Duration
	TLSSkipVerify   bool
	AgentID         int64
	AgentPublicID   string
	AgentName       string
	ErrorKind       string
	Err             error
	Skipped         bool
	DebugAttributes map[string]string
}

type publicRouteTargetHealthTraceState struct {
	status          p2pstreamv1.PublicRouteTargetHealthStatus
	available       bool
	healthyStreak   int64
	unhealthyStreak int64
	passiveUntil    int64
}

func newPublicRouteTargetHealthMonitor() *publicRouteTargetHealthMonitor {
	return &publicRouteTargetHealthMonitor{states: make(map[int64]*publicRouteTargetHealthState)}
}

func (m *publicRouteTargetHealthMonitor) reconcile(app *App, snap *publicProxySnapshot, active bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.app = app
	if snap == nil {
		m.app = nil
		for id, state := range m.states {
			state.stopLocked()
			delete(m.states, id)
		}
		return
	}
	healthBackends := publicHealthTargetConfigsFromSnapshot(snap)
	for id, state := range m.states {
		if _, ok := healthBackends[id]; !ok {
			state.stopLocked()
			delete(m.states, id)
		}
	}
	for _, target := range healthBackends {
		state := m.states[target.ID]
		if state == nil {
			state = newPublicRouteTargetHealthState(target)
			m.states[target.ID] = state
		}
		state.target = target

		shouldRunDirect := active &&
			target.Enabled &&
			target.TargetType == publicRouteTargetTypeProxy &&
			target.Transport != publicRouteTargetTransportAgent &&
			target.HealthCheck.Enabled &&
			target.ParsedOrigin != nil
		if shouldRunDirect && !state.directCheckRunning {
			ctx, cancel := context.WithCancel(context.Background())
			state.directCancel = cancel
			state.directCheckRunning = true
			go m.directHealthLoop(ctx, target.ID)
		}
		if !shouldRunDirect {
			state.stopDirectLocked()
		}

		shouldRunAgents := active &&
			target.Enabled &&
			target.TargetType == publicRouteTargetTypeProxy &&
			target.Transport == publicRouteTargetTransportAgent &&
			target.HealthCheck.Enabled &&
			target.ParsedOrigin != nil
		desiredAgents := make(map[int64]struct{})
		if shouldRunAgents {
			for _, assignment := range target.AgentAssignments {
				if !assignment.Enabled {
					continue
				}
				desiredAgents[assignment.AgentID] = struct{}{}
				agentState := state.ensureAgentStateLocked(assignment.AgentID)
				if !agentState.checkRunning {
					ctx, cancel := context.WithCancel(context.Background())
					agentState.cancel = cancel
					agentState.checkRunning = true
					go m.agentHealthLoop(ctx, app, target.ID, assignment.AgentID)
				}
			}
		}
		for agentID, agentState := range state.agentStates {
			if _, ok := desiredAgents[agentID]; ok {
				continue
			}
			agentState.stopLocked()
			delete(state.agentStates, agentID)
		}
		if !target.HealthCheck.Enabled {
			state.clearHealthStateLocked()
		}
	}
}

func publicHealthTargetConfigsFromSnapshot(snap *publicProxySnapshot) map[int64]publicRouteTargetHealthConfig {
	resp := make(map[int64]publicRouteTargetHealthConfig)
	if snap == nil {
		return resp
	}
	for _, target := range snap.RouteTargets {
		if target.TargetType != publicRouteTargetTypeProxy {
			continue
		}
		resp[target.ID] = publicRouteTargetHealthConfigFromRouteTarget(target, snap.Agents)
	}
	return resp
}

func publicRouteTargetHealthConfigFromRouteTarget(target publicRouteTargetConfig, agents map[int64]publicAgentConfig) publicRouteTargetHealthConfig {
	transport := publicRouteTargetTransportDirect
	assignments := []publicRouteTargetAgentAssignment(nil)
	if target.Transport == publicRouteTargetTransportAgent {
		transport = publicRouteTargetTransportAgent
		for agentID, agent := range agents {
			if !agent.Enabled || !agentSelectorMatchesLabels(target.AgentSelector, agent.Labels) {
				continue
			}
			assignments = append(assignments, publicRouteTargetAgentAssignment{
				TargetID: target.ID,
				AgentID:  agentID,
				Position: agentID,
				Weight:   100,
				Enabled:  true,
			})
		}
		sort.Slice(assignments, func(i, j int) bool { return assignments[i].AgentID < assignments[j].AgentID })
	}
	return publicRouteTargetHealthConfig{
		ID:                            target.ID,
		Name:                          target.Name,
		TargetOrigin:                  target.URL,
		TargetType:                    publicRouteTargetTypeProxy,
		Transport:                     transport,
		LoadBalancing:                 target.AgentLoadBalancing,
		TLSSkipVerify:                 target.TLSSkipVerify,
		UpstreamRequestHeaders:        target.UpstreamRequestHeaders,
		UpstreamBasicAuth:             target.UpstreamBasicAuth,
		UpstreamResponseHeaderTimeout: target.UpstreamResponseHeaderTimeout,
		Enabled:                       target.Enabled,
		ParsedOrigin:                  target.ParsedURL,
		AgentAssignments:              assignments,
		HealthCheck:                   target.HealthCheck,
	}
}

func publicRouteTargetConfigFromHealthTarget(target publicRouteTargetHealthConfig) publicRouteTargetConfig {
	transport := publicRouteTargetTransportDirect
	if target.Transport == publicRouteTargetTransportAgent {
		transport = publicRouteTargetTransportAgent
	}
	return publicRouteTargetConfig{
		ID:                            target.ID,
		Name:                          target.Name,
		Enabled:                       target.Enabled,
		TargetType:                    publicRouteTargetTypeProxy,
		URL:                           target.TargetOrigin,
		Transport:                     transport,
		AgentLoadBalancing:            target.LoadBalancing,
		TLSSkipVerify:                 target.TLSSkipVerify,
		UpstreamRequestHeaders:        target.UpstreamRequestHeaders,
		UpstreamBasicAuth:             target.UpstreamBasicAuth,
		UpstreamResponseHeaderTimeout: target.UpstreamResponseHeaderTimeout,
		HealthCheck:                   target.HealthCheck,
		ParsedURL:                     target.ParsedOrigin,
	}
}

func newPublicRouteTargetHealthState(target publicRouteTargetHealthConfig) *publicRouteTargetHealthState {
	return &publicRouteTargetHealthState{
		target:      target,
		direct:      unknownPublicRouteTargetCheckState(),
		agentStates: make(map[int64]*publicRouteTargetAgentHealthState),
	}
}

func unknownPublicRouteTargetCheckState() publicRouteTargetCheckState {
	return publicRouteTargetCheckState{explicitStatus: p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN}
}

func (s *publicRouteTargetHealthState) stopLocked() {
	s.stopDirectLocked()
	for _, agentState := range s.agentStates {
		agentState.stopLocked()
	}
}

func (s *publicRouteTargetHealthState) stopDirectLocked() {
	if s.directCancel != nil {
		s.directCancel()
		s.directCancel = nil
	}
	s.directCheckRunning = false
}

func (s *publicRouteTargetHealthState) ensureAgentStateLocked(agentID int64) *publicRouteTargetAgentHealthState {
	if s.agentStates == nil {
		s.agentStates = make(map[int64]*publicRouteTargetAgentHealthState)
	}
	agentState := s.agentStates[agentID]
	if agentState == nil {
		agentState = &publicRouteTargetAgentHealthState{
			agentID: agentID,
			state:   unknownPublicRouteTargetCheckState(),
		}
		s.agentStates[agentID] = agentState
	}
	return agentState
}

func (s *publicRouteTargetAgentHealthState) stopLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.checkRunning = false
}

func (s *publicRouteTargetHealthState) clearHealthStateLocked() {
	s.direct = unknownPublicRouteTargetCheckState()
	for _, agentState := range s.agentStates {
		agentState.state = unknownPublicRouteTargetCheckState()
	}
}

func (m *publicRouteTargetHealthMonitor) directHealthLoop(ctx context.Context, targetID int64) {
	for {
		target, ok := m.targetForDirectCheck(targetID)
		if !ok {
			return
		}
		m.recordDirectExplicitCheck(targetID, runPublicRouteTargetHealthCheck(ctx, target))
		if !waitPublicTargetHealthInterval(ctx, target.HealthCheck.Interval) {
			return
		}
	}
}

func (m *publicRouteTargetHealthMonitor) agentHealthLoop(ctx context.Context, app *App, targetID int64, agentID int64) {
	for {
		target, ok := m.backendForAgentCheck(targetID, agentID)
		if !ok {
			return
		}
		agent := (*AgentConn)(nil)
		if app != nil && app.AgentHub != nil {
			agent = app.AgentHub.connectedByID(agentID)
		}
		if agent == nil {
			m.recordAgentDisconnected(targetID, agentID)
			m.recordAgentActiveCheckSkipped(targetID, agentID, target, "agent_disconnected", errAgentDisconnected)
		} else {
			m.recordAgentConnected(targetID, agentID, agent.PublicID)
			m.recordAgentExplicitCheck(targetID, agentID, app.runPublicRouteTargetHealthCheckViaAgent(ctx, target, agent))
		}
		if !waitPublicTargetHealthInterval(ctx, target.HealthCheck.Interval) {
			return
		}
	}
}

func waitPublicTargetHealthInterval(ctx context.Context, interval time.Duration) bool {
	if interval <= 0 {
		interval = time.Duration(defaultTargetHealthCheckIntervalMillis) * time.Millisecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *publicRouteTargetHealthMonitor) targetForDirectCheck(targetID int64) (publicRouteTargetHealthConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil || !state.directCheckRunning {
		return publicRouteTargetHealthConfig{}, false
	}
	return state.target, true
}

func (m *publicRouteTargetHealthMonitor) backendForAgentCheck(targetID int64, agentID int64) (publicRouteTargetHealthConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return publicRouteTargetHealthConfig{}, false
	}
	agentState := state.agentStates[agentID]
	if agentState == nil || !agentState.checkRunning {
		return publicRouteTargetHealthConfig{}, false
	}
	return state.target, true
}

func publicRouteTargetHealthCheckURL(target publicRouteTargetHealthConfig) (*url.URL, error) {
	if target.ParsedOrigin == nil {
		return nil, errors.New("target origin is not configured")
	}
	checkURL := *target.ParsedOrigin
	checkURL.Path = target.HealthCheck.Path
	checkURL.RawPath = ""
	checkURL.RawQuery = ""
	checkURL.Fragment = ""
	return &checkURL, nil
}

func checkPublicTargetHealth(parent context.Context, target publicRouteTargetHealthConfig) error {
	return runPublicRouteTargetHealthCheck(parent, target).Err
}

func runPublicRouteTargetHealthCheck(parent context.Context, target publicRouteTargetHealthConfig) publicRouteTargetHealthCheckAttempt {
	attempt := newPublicRouteTargetHealthCheckAttempt(target)
	defer finishPublicRouteTargetHealthCheckAttempt(&attempt)

	checkURL, err := publicRouteTargetHealthCheckURL(target)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	attempt.URL = redactSensitiveTraceURL(checkURL.String())
	timeout := target.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultTargetHealthCheckTimeoutMillis) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, target.HealthCheck.Method, checkURL.String(), nil)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	applyUpstreamRequestConfig(req, target)
	client := &http.Client{Transport: directProxyTransport(target.TLSSkipVerify, timeout)}
	resp, err := client.Do(req)
	if err != nil {
		attempt.applyContextFailure(parent, ctx, "request_failed", err)
		return attempt
	}
	defer resp.Body.Close()
	attempt.StatusCode = int64(resp.StatusCode)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		attempt.applyContextFailure(parent, ctx, "response_body_read_failed", fmt.Errorf("drain health check response: %w", err))
		return attempt
	}
	if int64(resp.StatusCode) < target.HealthCheck.ExpectedStatusMin || int64(resp.StatusCode) > target.HealthCheck.ExpectedStatusMax {
		attempt.fail("unexpected_status", fmt.Errorf("health check status %d outside expected range %d-%d", resp.StatusCode, target.HealthCheck.ExpectedStatusMin, target.HealthCheck.ExpectedStatusMax))
		return attempt
	}
	attempt.ErrorKind = "success"
	return attempt
}

func (a *App) checkPublicTargetHealthViaAgent(parent context.Context, target publicRouteTargetHealthConfig, agent *AgentConn) error {
	return a.runPublicRouteTargetHealthCheckViaAgent(parent, target, agent).Err
}

func (a *App) runPublicRouteTargetHealthCheckViaAgent(parent context.Context, target publicRouteTargetHealthConfig, agent *AgentConn) publicRouteTargetHealthCheckAttempt {
	attempt := newPublicRouteTargetHealthCheckAttempt(target)
	attempt.DebugAttributes["transport"] = "agent_pool"
	defer finishPublicRouteTargetHealthCheckAttempt(&attempt)

	if a == nil {
		attempt.fail("request_failed", errors.New("app is not configured"))
		return attempt
	}
	if agent == nil {
		attempt.fail("agent_disconnected", errAgentDisconnected)
		return attempt
	}
	attempt.AgentID = agent.AgentID
	attempt.AgentPublicID = agent.PublicID
	attempt.AgentName = agent.Name
	attempt.DebugAttributes["agent_id"] = strconv.FormatInt(agent.AgentID, 10)
	attempt.DebugAttributes["agent_public_id"] = agent.PublicID
	checkURL, err := publicRouteTargetHealthCheckURL(target)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	attempt.URL = redactSensitiveTraceURL(checkURL.String())
	timeout := target.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultTargetHealthCheckTimeoutMillis) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, target.HealthCheck.Method, checkURL.String(), nil)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	applyUpstreamRequestConfig(req, target)

	id, err := uuid.NewV7()
	if err != nil {
		attempt.fail("request_failed", fmt.Errorf("generate health check request id: %w", err))
		return attempt
	}
	attempt.DebugAttributes["agent_request_id"] = id.String()
	attempt.DebugAttributes["response_header_timeout_ms"] = strconv.FormatInt(int64(timeout/time.Millisecond), 10)
	req = req.WithContext(withAgentDialRequestID(req.Context(), id.String()))

	healthBackend := target
	healthBackend.UpstreamResponseHeaderTimeout = timeout
	client := &http.Client{Transport: a.agentTargetTransport(agent, publicRouteTargetConfigFromHealthTarget(healthBackend))}
	resp, err := client.Do(req)
	if err != nil {
		var dialErr agentDialError
		switch {
		case errors.Is(err, errAgentDisconnected):
			attempt.fail("agent_disconnected", err)
		case errors.As(err, &dialErr):
			kind := "agent_dial_failed"
			if dialErr.Kind != "" {
				kind = "agent_" + dialErr.Kind
			}
			attempt.fail(kind, err)
		default:
			attempt.applyContextFailure(parent, ctx, "request_failed", err)
		}
		return attempt
	}
	defer resp.Body.Close()
	attempt.StatusCode = int64(resp.StatusCode)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		attempt.applyContextFailure(parent, ctx, "response_body_read_failed", fmt.Errorf("drain health check response: %w", err))
		return attempt
	}
	if int64(resp.StatusCode) < target.HealthCheck.ExpectedStatusMin || int64(resp.StatusCode) > target.HealthCheck.ExpectedStatusMax {
		attempt.fail("unexpected_status", fmt.Errorf("health check status %d outside expected range %d-%d", resp.StatusCode, target.HealthCheck.ExpectedStatusMin, target.HealthCheck.ExpectedStatusMax))
		return attempt
	}
	attempt.ErrorKind = "success"
	return attempt
}

func newPublicRouteTargetHealthCheckAttempt(target publicRouteTargetHealthConfig) publicRouteTargetHealthCheckAttempt {
	timeout := target.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultTargetHealthCheckTimeoutMillis) * time.Millisecond
	}
	attempt := publicRouteTargetHealthCheckAttempt{
		StartedAt:     time.Now(),
		Method:        target.HealthCheck.Method,
		ExpectedMin:   target.HealthCheck.ExpectedStatusMin,
		ExpectedMax:   target.HealthCheck.ExpectedStatusMax,
		Timeout:       timeout,
		TLSSkipVerify: target.TLSSkipVerify,
		ErrorKind:     "success",
		DebugAttributes: map[string]string{
			"transport":    "direct",
			"timeout_ms":   strconv.FormatInt(int64(timeout/time.Millisecond), 10),
			"check_method": target.HealthCheck.Method,
			"check_path":   target.HealthCheck.Path,
		},
	}
	if checkURL, err := publicRouteTargetHealthCheckURL(target); err == nil {
		attempt.URL = redactSensitiveTraceURL(checkURL.String())
	}
	return attempt
}

func finishPublicRouteTargetHealthCheckAttempt(attempt *publicRouteTargetHealthCheckAttempt) {
	if attempt == nil {
		return
	}
	if attempt.FinishedAt.IsZero() {
		attempt.FinishedAt = time.Now()
	}
	if attempt.ErrorKind == "" {
		attempt.ErrorKind = "success"
	}
}

func (a *publicRouteTargetHealthCheckAttempt) fail(kind string, err error) {
	if kind == "" {
		kind = "request_failed"
	}
	a.ErrorKind = kind
	a.Err = err
}

func (a *publicRouteTargetHealthCheckAttempt) skip(kind string, err error) {
	a.fail(kind, err)
	a.Skipped = true
}

func (a *publicRouteTargetHealthCheckAttempt) applyContextFailure(parent context.Context, ctx context.Context, fallbackKind string, err error) {
	kind, skipped, cancelSource := classifyHealthCheckFailure(parent, ctx, fallbackKind, err)
	if skipped {
		a.skip(kind, err)
		if cancelSource != "" {
			a.DebugAttributes["cancel_source"] = cancelSource
		}
		return
	}
	a.fail(kind, err)
}

func newPassiveHealthTraceAttempt(target publicRouteTargetHealthConfig, agentID int64, agentPublicID string, agentName string, err error) publicRouteTargetHealthCheckAttempt {
	if err == nil {
		err = errors.New("temporary upstream failure")
	}
	attempt := newPublicRouteTargetHealthCheckAttempt(target)
	finishPublicRouteTargetHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.AgentPublicID = agentPublicID
	attempt.AgentName = agentName
	attempt.ErrorKind = "passive_failure"
	attempt.Err = err
	attempt.DebugAttributes["passive_cooldown_ms"] = strconv.FormatInt(int64(publicRouteTargetPassiveUnhealthyCooldown/time.Millisecond), 10)
	if agentID > 0 {
		attempt.DebugAttributes["transport"] = "agent_pool"
	}
	return attempt
}

func classifyHealthCheckFailure(parent context.Context, ctx context.Context, fallbackKind string, err error) (string, bool, string) {
	if fallbackKind == "" {
		fallbackKind = "request_failed"
	}
	if parent != nil && errors.Is(parent.Err(), context.Canceled) {
		return "health_check_cancelled", true, "health_monitor"
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
		return "health_check_cancelled", true, "health_monitor"
	}
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return "health_check_timeout", false, ""
	}
	return fallbackKind, false, ""
}

func (m *publicRouteTargetHealthMonitor) recordDirectExplicitCheck(targetID int64, attempt publicRouteTargetHealthCheckAttempt) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return
	}
	now := time.Now()
	before := publicRouteTargetHealthTraceStateFromCheck(state.direct, state.target.HealthCheck.Enabled, now)
	if attempt.Skipped {
		after := publicRouteTargetHealthTraceStateFromCheck(state.direct, state.target.HealthCheck.Enabled, time.Now())
		trace := m.newHealthTraceLocked(
			state.target,
			p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
			attempt,
			before,
			after,
		)
		m.appendHealthTraceLocked(&state.directTraces, trace)
		return
	}
	recordPublicRouteTargetCheckResult(&state.direct, state.target.HealthCheck, attempt.Err)
	after := publicRouteTargetHealthTraceStateFromCheck(state.direct, state.target.HealthCheck.Enabled, time.Now())
	trace := m.newHealthTraceLocked(
		state.target,
		p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&state.directTraces, trace)
}

func (m *publicRouteTargetHealthMonitor) recordAgentExplicitCheck(targetID int64, agentID int64, attempt publicRouteTargetHealthCheckAttempt) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return
	}
	agentState := state.ensureAgentStateLocked(agentID)
	if attempt.AgentID == 0 {
		attempt.AgentID = agentID
	}
	if attempt.AgentPublicID == "" {
		attempt.AgentPublicID = agentState.agentPublicID
	}
	now := time.Now()
	before := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, now)
	if attempt.Skipped {
		after := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
		trace := m.newHealthTraceLocked(
			state.target,
			p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
			attempt,
			before,
			after,
		)
		m.appendHealthTraceLocked(&agentState.traces, trace)
		return
	}
	recordPublicRouteTargetCheckResult(&agentState.state, state.target.HealthCheck, attempt.Err)
	after := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	trace := m.newHealthTraceLocked(
		state.target,
		p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func (m *publicRouteTargetHealthMonitor) recordAgentActiveCheckSkipped(targetID int64, agentID int64, target publicRouteTargetHealthConfig, errorKind string, err error) {
	if m == nil {
		return
	}
	attempt := newPublicRouteTargetHealthCheckAttempt(target)
	finishPublicRouteTargetHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.ErrorKind = errorKind
	attempt.Err = err
	attempt.DebugAttributes["transport"] = "agent_pool"
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return
	}
	agentState := state.ensureAgentStateLocked(agentID)
	attempt.AgentPublicID = agentState.agentPublicID
	before := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	after := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	trace := m.newHealthTraceLocked(
		state.target,
		p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
		attempt,
		before,
		after,
	)
	trace.Outcome = p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SKIPPED
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func recordPublicRouteTargetCheckResult(state *publicRouteTargetCheckState, config publicRouteTargetHealthCheckConfig, err error) {
	if state == nil {
		return
	}
	state.lastCheckedAt = time.Now()
	if err == nil {
		state.healthyStreak++
		state.unhealthyStreak = 0
		state.lastError = ""
		state.passiveUnhealthyUntil = time.Time{}
		if state.healthyStreak >= normalizedThreshold(config.HealthyThreshold, defaultTargetHealthCheckHealthyThreshold) {
			state.explicitStatus = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY
		}
		return
	}
	state.unhealthyStreak++
	state.healthyStreak = 0
	state.lastError = err.Error()
	if state.unhealthyStreak >= normalizedThreshold(config.UnhealthyThreshold, defaultTargetHealthCheckUnhealthyThreshold) {
		state.explicitStatus = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY
	}
}

func publicRouteTargetHealthTraceStateFromCheck(state publicRouteTargetCheckState, healthEnabled bool, now time.Time) publicRouteTargetHealthTraceState {
	status, passiveUntil := checkStateSnapshotStatus(state, healthEnabled, now)
	return publicRouteTargetHealthTraceState{
		status:          status,
		available:       checkStateAvailable(state, healthEnabled, now),
		healthyStreak:   state.healthyStreak,
		unhealthyStreak: state.unhealthyStreak,
		passiveUntil:    passiveUntil,
	}
}

func (m *publicRouteTargetHealthMonitor) newHealthTraceLocked(
	target publicRouteTargetHealthConfig,
	source p2pstreamv1.PublicRouteTargetHealthTraceSource,
	attempt publicRouteTargetHealthCheckAttempt,
	before publicRouteTargetHealthTraceState,
	after publicRouteTargetHealthTraceState,
) *p2pstreamv1.PublicRouteTargetHealthTrace {
	outcome := p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SUCCESS
	if attempt.Skipped {
		outcome = p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SKIPPED
	} else if attempt.Err != nil {
		outcome = p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_FAILURE
	}
	if attempt.FinishedAt.IsZero() {
		attempt.FinishedAt = time.Now()
	}
	errorMessage := ""
	if attempt.Err != nil {
		errorMessage = attempt.Err.Error()
	}
	duration := attempt.FinishedAt.Sub(attempt.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	trace := &p2pstreamv1.PublicRouteTargetHealthTrace{
		Sequence:                        m.nextHealthTraceSequenceLocked(),
		RouteTargetId:                   target.ID,
		RouteTargetName:                 target.Name,
		Transport:                       publicRouteTargetTransportFromConfig(target.Transport),
		Source:                          source,
		Outcome:                         outcome,
		AgentId:                         attempt.AgentID,
		AgentPublicId:                   attempt.AgentPublicID,
		AgentName:                       attempt.AgentName,
		StartedAtUnixMillis:             unixMillis(attempt.StartedAt),
		FinishedAtUnixMillis:            unixMillis(attempt.FinishedAt),
		DurationMillis:                  duration,
		Method:                          attempt.Method,
		Url:                             attempt.URL,
		StatusCode:                      attempt.StatusCode,
		ExpectedStatusMin:               attempt.ExpectedMin,
		ExpectedStatusMax:               attempt.ExpectedMax,
		TimeoutMillis:                   int64(attempt.Timeout / time.Millisecond),
		TlsSkipVerify:                   attempt.TLSSkipVerify,
		StatusBefore:                    before.status,
		StatusAfter:                     after.status,
		AvailableBefore:                 before.available,
		AvailableAfter:                  after.available,
		HealthyStreakBefore:             before.healthyStreak,
		HealthyStreakAfter:              after.healthyStreak,
		UnhealthyStreakBefore:           before.unhealthyStreak,
		UnhealthyStreakAfter:            after.unhealthyStreak,
		PassiveUnhealthyUntilUnixMillis: after.passiveUntil,
		ErrorKind:                       attempt.ErrorKind,
		Error:                           errorMessage,
		DebugAttributes:                 cloneStringMap(attempt.DebugAttributes),
	}
	if trace.ErrorKind == "" {
		trace.ErrorKind = "success"
	}
	return trace
}

func (m *publicRouteTargetHealthMonitor) nextHealthTraceSequenceLocked() uint64 {
	m.sequence++
	return m.sequence
}

func (m *publicRouteTargetHealthMonitor) appendHealthTraceLocked(target *[]*p2pstreamv1.PublicRouteTargetHealthTrace, trace *p2pstreamv1.PublicRouteTargetHealthTrace) {
	if trace == nil {
		return
	}
	*target = append(*target, trace)
	if len(*target) <= publicRouteTargetHealthTraceLimitPerTarget {
		return
	}
	copy(*target, (*target)[len(*target)-publicRouteTargetHealthTraceLimitPerTarget:])
	*target = (*target)[:publicRouteTargetHealthTraceLimitPerTarget]
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (m *publicRouteTargetHealthMonitor) recordAgentConnected(targetID int64, agentID int64, publicID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return
	}
	m.recordAgentConnectedLocked(state, agentID, publicID)
}

func (m *publicRouteTargetHealthMonitor) recordAgentConnectedForAll(agentID int64, publicID string) {
	if m == nil || agentID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, state := range m.states {
		if !backendHasAgentAssignment(state.target, agentID) {
			continue
		}
		m.recordAgentConnectedLocked(state, agentID, publicID)
	}
}

func (m *publicRouteTargetHealthMonitor) recordAgentConnectedLocked(state *publicRouteTargetHealthState, agentID int64, publicID string) {
	agentState := state.ensureAgentStateLocked(agentID)
	wasConnected := agentState.connected
	before := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	agentState.connected = true
	agentState.agentPublicID = publicID
	if !wasConnected {
		agentState.state = unknownPublicRouteTargetCheckState()
	}
	if wasConnected {
		return
	}
	agentName := ""
	if conn := m.connectedAgentLocked(agentID); conn != nil {
		agentName = conn.Name
		if publicID == "" {
			publicID = conn.PublicID
		}
	}
	after := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	attempt := newPublicRouteTargetHealthCheckAttempt(state.target)
	finishPublicRouteTargetHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.AgentPublicID = publicID
	attempt.AgentName = agentName
	attempt.ErrorKind = "agent_connected"
	attempt.DebugAttributes["transport"] = "agent_pool"
	attempt.DebugAttributes["agent_event"] = "connected"
	trace := m.newHealthTraceLocked(
		state.target,
		p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_AGENT_CONNECTIVITY,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func (m *publicRouteTargetHealthMonitor) recordAgentDisconnected(targetID int64, agentID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return
	}
	m.recordAgentDisconnectedLocked(state, agentID)
}

func (m *publicRouteTargetHealthMonitor) recordAgentDisconnectedForAll(agentID int64) {
	if m == nil || agentID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, state := range m.states {
		if !backendHasAgentAssignment(state.target, agentID) {
			continue
		}
		m.recordAgentDisconnectedLocked(state, agentID)
	}
}

func (m *publicRouteTargetHealthMonitor) recordAgentDisconnectedLocked(state *publicRouteTargetHealthState, agentID int64) {
	agentState := state.ensureAgentStateLocked(agentID)
	wasConnected := agentState.connected
	before := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	agentState.connected = false
	agentState.state.explicitStatus = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN
	agentState.state.healthyStreak = 0
	agentState.state.unhealthyStreak = 0
	if !wasConnected {
		return
	}
	after := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	attempt := newPublicRouteTargetHealthCheckAttempt(state.target)
	finishPublicRouteTargetHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.AgentPublicID = agentState.agentPublicID
	attempt.ErrorKind = "agent_disconnected_event"
	attempt.Err = errAgentDisconnected
	attempt.DebugAttributes["transport"] = "agent_pool"
	attempt.DebugAttributes["agent_event"] = "disconnected"
	trace := m.newHealthTraceLocked(
		state.target,
		p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_AGENT_CONNECTIVITY,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func backendHasAgentAssignment(target publicRouteTargetHealthConfig, agentID int64) bool {
	for _, assignment := range target.AgentAssignments {
		if assignment.AgentID == agentID {
			return true
		}
	}
	return false
}

func normalizedThreshold(value int64, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func (m *publicRouteTargetHealthMonitor) markPassiveFailure(targetID int64, err error) {
	if m == nil || targetID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return
	}
	if !m.passiveFailuresEnabledLocked(state) {
		state.direct = unknownPublicRouteTargetCheckState()
		return
	}
	before := publicRouteTargetHealthTraceStateFromCheck(state.direct, state.target.HealthCheck.Enabled, time.Now())
	m.markCheckPassiveFailureLocked(&state.direct, err)
	after := publicRouteTargetHealthTraceStateFromCheck(state.direct, state.target.HealthCheck.Enabled, time.Now())
	attempt := newPassiveHealthTraceAttempt(state.target, 0, "", "", err)
	trace := m.newHealthTraceLocked(
		state.target,
		p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_PASSIVE_FAILURE,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&state.directTraces, trace)
}

func (m *publicRouteTargetHealthMonitor) markAgentPassiveFailure(targetID int64, agentID int64, err error) {
	if m == nil || targetID <= 0 || agentID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return
	}
	agentState := state.agentStates[agentID]
	if !m.passiveFailuresEnabledLocked(state) {
		if agentState != nil {
			agentState.state = unknownPublicRouteTargetCheckState()
		}
		return
	}
	if agentState == nil {
		agentState = state.ensureAgentStateLocked(agentID)
	}
	before := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	m.markCheckPassiveFailureLocked(&agentState.state, err)
	after := publicRouteTargetHealthTraceStateFromCheck(agentState.state, state.target.HealthCheck.Enabled, time.Now())
	attempt := newPassiveHealthTraceAttempt(state.target, agentID, agentState.agentPublicID, "", err)
	trace := m.newHealthTraceLocked(
		state.target,
		p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_PASSIVE_FAILURE,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func (m *publicRouteTargetHealthMonitor) passiveFailuresEnabledLocked(state *publicRouteTargetHealthState) bool {
	return state != nil && state.target.HealthCheck.Enabled
}

func (m *publicRouteTargetHealthMonitor) markCheckPassiveFailureLocked(state *publicRouteTargetCheckState, err error) {
	state.passiveUnhealthyUntil = time.Now().Add(publicRouteTargetPassiveUnhealthyCooldown)
	if err != nil {
		state.lastError = err.Error()
	} else {
		state.lastError = "temporary upstream failure"
	}
}

func (m *publicRouteTargetHealthMonitor) beginRequest(targetID int64) func() {
	if m == nil || targetID <= 0 {
		return func() {}
	}
	m.mu.Lock()
	state := m.ensureStateLocked(targetID)
	m.mu.Unlock()
	state.activeRequests.Add(1)
	return func() {
		state.activeRequests.Add(-1)
	}
}

func (m *publicRouteTargetHealthMonitor) activeRequests(targetID int64) int64 {
	if m == nil || targetID <= 0 {
		return 0
	}
	m.mu.Lock()
	state := m.states[targetID]
	m.mu.Unlock()
	if state == nil {
		return 0
	}
	return state.activeRequests.Load()
}

func (m *publicRouteTargetHealthMonitor) listHealthTraces(targetID int64, agentID int64, limit int64, failuresOnly bool) ([]*p2pstreamv1.PublicRouteTargetHealthTrace, int64) {
	if limit <= 0 || limit > publicRouteTargetHealthTraceLimitPerTarget {
		limit = publicRouteTargetHealthTraceLimitPerTarget
	}
	if m == nil || targetID <= 0 {
		return nil, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	if state == nil {
		return nil, 0
	}
	traces := make([]*p2pstreamv1.PublicRouteTargetHealthTrace, 0)
	if state.target.Transport == publicRouteTargetTransportAgent {
		if agentID > 0 {
			if agentState := state.agentStates[agentID]; agentState != nil {
				traces = append(traces, agentState.traces...)
			}
		} else {
			for _, agentState := range state.agentStates {
				traces = append(traces, agentState.traces...)
			}
		}
	} else if agentID <= 0 {
		traces = append(traces, state.directTraces...)
	}
	retained := int64(len(traces))
	sort.Slice(traces, func(i int, j int) bool {
		return traces[i].Sequence > traces[j].Sequence
	})
	resp := make([]*p2pstreamv1.PublicRouteTargetHealthTrace, 0, minInt64(limit, retained))
	for _, trace := range traces {
		if failuresOnly &&
			trace.Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_FAILURE &&
			trace.Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SKIPPED {
			continue
		}
		resp = append(resp, cloneHealthTrace(trace))
		if int64(len(resp)) >= limit {
			break
		}
	}
	return resp, retained
}

func cloneHealthTrace(trace *p2pstreamv1.PublicRouteTargetHealthTrace) *p2pstreamv1.PublicRouteTargetHealthTrace {
	if trace == nil {
		return nil
	}
	copyTrace, ok := proto.Clone(trace).(*p2pstreamv1.PublicRouteTargetHealthTrace)
	if !ok {
		return &p2pstreamv1.PublicRouteTargetHealthTrace{}
	}
	return copyTrace
}

func minInt64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (m *publicRouteTargetHealthMonitor) available(target publicRouteTargetHealthConfig) bool {
	if m == nil {
		return true
	}
	if !target.Enabled {
		return false
	}
	if target.Transport == publicRouteTargetTransportAgent {
		return m.backendAgentPoolAvailable(target)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[target.ID]
	if state == nil {
		return true
	}
	return checkStateAvailable(state.direct, target.HealthCheck.Enabled, time.Now())
}

func (m *publicRouteTargetHealthMonitor) backendAgentPoolAvailable(target publicRouteTargetHealthConfig) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, assignment := range target.AgentAssignments {
		if !assignment.Enabled {
			continue
		}
		if !m.agentConnectedLocked(assignment.AgentID) && m.app != nil {
			continue
		}
		if m.agentAvailableLocked(target.ID, assignment.AgentID, target.HealthCheck.Enabled, now) {
			return true
		}
	}
	return false
}

func (m *publicRouteTargetHealthMonitor) agentAvailable(targetID int64, agentID int64) bool {
	if m == nil || targetID <= 0 || agentID <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[targetID]
	healthEnabled := false
	if state != nil {
		healthEnabled = state.target.HealthCheck.Enabled
	}
	return m.agentAvailableLocked(targetID, agentID, healthEnabled, time.Now())
}

func (m *publicRouteTargetHealthMonitor) agentAvailableLocked(targetID int64, agentID int64, healthEnabled bool, now time.Time) bool {
	state := m.states[targetID]
	if state == nil {
		return true
	}
	agentState := state.agentStates[agentID]
	if agentState == nil {
		return true
	}
	return checkStateAvailable(agentState.state, healthEnabled, now)
}

func checkStateAvailable(state publicRouteTargetCheckState, healthEnabled bool, now time.Time) bool {
	if !healthEnabled {
		return true
	}
	if state.passiveUnhealthyUntil.After(now) {
		return false
	}
	return state.explicitStatus != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY &&
		state.explicitStatus != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED
}

func (m *publicRouteTargetHealthMonitor) snapshot(target dbPublicRouteTargetLike) *publicRouteTargetHealthSnapshot {
	if m == nil || target.targetID() <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[target.targetID()]
	if state == nil {
		return nil
	}
	if state.target.Transport == publicRouteTargetTransportAgent {
		return m.agentPoolSnapshotLocked(state, target.backendEnabled())
	}
	return directHealthSnapshot(state, target.backendEnabled())
}

func (m *publicRouteTargetHealthMonitor) agentSnapshot(targetID int64, agentID int64, assignmentEnabled bool, agentEnabled bool) *publicRouteTargetAgentHealthSnapshot {
	if !assignmentEnabled || !agentEnabled {
		return &publicRouteTargetAgentHealthSnapshot{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED,
			Available: false,
		}
	}
	if m == nil || targetID <= 0 || agentID <= 0 {
		return &publicRouteTargetAgentHealthSnapshot{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN,
			Available: false,
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	conn := m.connectedAgentLocked(agentID)
	if conn == nil {
		return &publicRouteTargetAgentHealthSnapshot{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISCONNECTED,
			Available: false,
		}
	}

	activeRequests := conn.ActiveRequests.Load()
	state := m.states[targetID]
	healthEnabled := false
	if state != nil {
		healthEnabled = state.target.HealthCheck.Enabled
	}
	if state == nil {
		return &publicRouteTargetAgentHealthSnapshot{
			Status:         p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN,
			Connected:      true,
			Available:      true,
			ActiveRequests: activeRequests,
		}
	}

	agentState := state.agentStates[agentID]
	if agentState == nil {
		return &publicRouteTargetAgentHealthSnapshot{
			Status:         p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN,
			Connected:      true,
			Available:      true,
			ActiveRequests: activeRequests,
		}
	}

	now := time.Now()
	status, passiveUntil := checkStateSnapshotStatus(agentState.state, healthEnabled, now)
	lastChecked := unixMillis(agentState.state.lastCheckedAt)
	lastError := ""
	if healthEnabled {
		lastError = agentState.state.lastError
	}
	return &publicRouteTargetAgentHealthSnapshot{
		Status:                          status,
		Connected:                       true,
		Available:                       checkStateAvailable(agentState.state, healthEnabled, now),
		LastCheckedAtUnixMillis:         lastChecked,
		LastError:                       lastError,
		PassiveUnhealthyUntilUnixMillis: passiveUntil,
		ActiveRequests:                  activeRequests,
	}
}

func directHealthSnapshot(state *publicRouteTargetHealthState, enabled bool) *publicRouteTargetHealthSnapshot {
	now := time.Now()
	status, passiveUntil := checkStateSnapshotStatus(state.direct, state.target.HealthCheck.Enabled, now)
	lastChecked := unixMillis(state.direct.lastCheckedAt)
	lastError := state.direct.lastError
	if !state.target.HealthCheck.Enabled {
		lastChecked = 0
		lastError = ""
	}
	if !enabled {
		status = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED
		passiveUntil = 0
	}
	return &publicRouteTargetHealthSnapshot{
		Status:                          status,
		LastCheckedAtUnixMillis:         lastChecked,
		LastError:                       lastError,
		PassiveUnhealthyUntilUnixMillis: passiveUntil,
	}
}

func (m *publicRouteTargetHealthMonitor) agentPoolSnapshotLocked(state *publicRouteTargetHealthState, enabled bool) *publicRouteTargetHealthSnapshot {
	if !enabled {
		return &publicRouteTargetHealthSnapshot{Status: p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED}
	}
	if !state.target.HealthCheck.Enabled {
		return &publicRouteTargetHealthSnapshot{Status: p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN}
	}
	now := time.Now()
	enabledAssignments := 0
	connectedCount := 0
	unhealthyCount := 0
	healthyCount := 0
	var lastChecked time.Time
	var lastErrorAt time.Time
	lastError := ""
	var passiveUntil time.Time
	unhealthyDuePassive := false

	for _, assignment := range state.target.AgentAssignments {
		if !assignment.Enabled {
			continue
		}
		enabledAssignments++
		agentState := state.agentStates[assignment.AgentID]
		publicID := ""
		if agentState != nil {
			publicID = agentState.agentPublicID
		}
		if conn := m.connectedAgentLocked(assignment.AgentID); conn != nil {
			publicID = conn.PublicID
		} else if m.app != nil {
			continue
		} else if agentState != nil && !agentState.connected {
			continue
		}
		connectedCount++
		if agentState == nil {
			continue
		}
		status, agentPassiveUntil := checkStateSnapshotStatus(agentState.state, state.target.HealthCheck.Enabled, now)
		if !agentState.state.lastCheckedAt.IsZero() && agentState.state.lastCheckedAt.After(lastChecked) {
			lastChecked = agentState.state.lastCheckedAt
		}
		if state.target.HealthCheck.Enabled && agentState.state.lastError != "" && (lastErrorAt.IsZero() || agentState.state.lastCheckedAt.After(lastErrorAt)) {
			lastErrorAt = agentState.state.lastCheckedAt
			lastError = formatAgentHealthError(publicID, assignment.AgentID, agentState.state.lastError)
		}
		if agentPassiveUntil > 0 {
			if until := time.UnixMilli(agentPassiveUntil); until.After(passiveUntil) {
				passiveUntil = until
			}
		}
		switch status {
		case p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY:
			healthyCount++
		case p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY,
			p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED:
			unhealthyCount++
			if agentPassiveUntil > 0 {
				unhealthyDuePassive = true
			}
		}
	}

	status := p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN
	outputPassiveUntil := int64(0)
	if connectedCount == 0 && enabledAssignments > 0 {
		status = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISCONNECTED
	} else if healthyCount > 0 {
		status = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY
	} else if connectedCount > 0 && unhealthyCount == connectedCount {
		status = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY
		if unhealthyDuePassive && passiveUntil.After(now) {
			outputPassiveUntil = passiveUntil.UnixMilli()
		}
	}

	return &publicRouteTargetHealthSnapshot{
		Status:                          status,
		LastCheckedAtUnixMillis:         unixMillis(lastChecked),
		LastError:                       lastError,
		PassiveUnhealthyUntilUnixMillis: outputPassiveUntil,
	}
}

func checkStateSnapshotStatus(state publicRouteTargetCheckState, healthEnabled bool, now time.Time) (p2pstreamv1.PublicRouteTargetHealthStatus, int64) {
	if !healthEnabled {
		return p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN, 0
	}
	status := state.explicitStatus
	if status == p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNSPECIFIED {
		status = p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN
	}
	if state.passiveUnhealthyUntil.After(now) {
		return p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY, state.passiveUnhealthyUntil.UnixMilli()
	}
	return status, 0
}

func (m *publicRouteTargetHealthMonitor) connectedAgentLocked(agentID int64) *AgentConn {
	if m.app == nil || m.app.AgentHub == nil {
		return nil
	}
	return m.app.AgentHub.connectedByID(agentID)
}

func (m *publicRouteTargetHealthMonitor) agentConnectedLocked(agentID int64) bool {
	return m.connectedAgentLocked(agentID) != nil
}

func formatAgentHealthError(publicID string, agentID int64, err string) string {
	if publicID == "" {
		publicID = strconv.FormatInt(agentID, 10)
	}
	return fmt.Sprintf("agent %s: %s", publicID, err)
}

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (m *publicRouteTargetHealthMonitor) ensureStateLocked(targetID int64) *publicRouteTargetHealthState {
	state := m.states[targetID]
	if state == nil {
		state = newPublicRouteTargetHealthState(publicRouteTargetHealthConfig{ID: targetID, Enabled: true})
		m.states[targetID] = state
	}
	return state
}

type dbPublicRouteTargetLike interface {
	targetID() int64
	backendEnabled() bool
}

type publicRouteTargetHealthDBAdapter struct {
	id      int64
	enabled bool
}

func (a publicRouteTargetHealthDBAdapter) targetID() int64 {
	return a.id
}

func (a publicRouteTargetHealthDBAdapter) backendEnabled() bool {
	return a.enabled
}
