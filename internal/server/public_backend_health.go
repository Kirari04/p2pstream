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
	"p2pstream/httpmsg"
	"p2pstream/msg"
)

const (
	publicBackendPassiveUnhealthyCooldown  = 30 * time.Second
	publicBackendHealthTraceLimitPerTarget = 100
)

type publicBackendHealthSnapshot struct {
	Status                          p2pstreamv1.PublicBackendHealthStatus
	LastCheckedAtUnixMillis         int64
	LastError                       string
	PassiveUnhealthyUntilUnixMillis int64
}

type publicBackendAgentHealthSnapshot struct {
	Status                          p2pstreamv1.PublicBackendHealthStatus
	Connected                       bool
	Available                       bool
	LastCheckedAtUnixMillis         int64
	LastError                       string
	PassiveUnhealthyUntilUnixMillis int64
	ActiveRequests                  int64
}

type publicBackendHealthMonitor struct {
	mu       sync.Mutex
	app      *App
	sequence uint64
	states   map[int64]*publicBackendHealthState
}

type publicBackendHealthState struct {
	backend            publicBackendConfig
	directCancel       context.CancelFunc
	directCheckRunning bool
	direct             publicBackendCheckState
	directTraces       []*p2pstreamv1.PublicBackendHealthTrace
	agentStates        map[int64]*publicBackendAgentHealthState
	activeRequests     atomic.Int64
}

type publicBackendAgentHealthState struct {
	agentID       int64
	agentPublicID string
	connected     bool
	cancel        context.CancelFunc
	checkRunning  bool
	state         publicBackendCheckState
	traces        []*p2pstreamv1.PublicBackendHealthTrace
}

type publicBackendCheckState struct {
	explicitStatus        p2pstreamv1.PublicBackendHealthStatus
	lastCheckedAt         time.Time
	lastError             string
	passiveUnhealthyUntil time.Time
	healthyStreak         int64
	unhealthyStreak       int64
}

type publicBackendHealthCheckAttempt struct {
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
	DebugAttributes map[string]string
}

type publicBackendHealthTraceState struct {
	status          p2pstreamv1.PublicBackendHealthStatus
	available       bool
	healthyStreak   int64
	unhealthyStreak int64
	passiveUntil    int64
}

func newPublicBackendHealthMonitor() *publicBackendHealthMonitor {
	return &publicBackendHealthMonitor{states: make(map[int64]*publicBackendHealthState)}
}

func (m *publicBackendHealthMonitor) reconcile(app *App, snap *publicProxySnapshot, active bool) {
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
	for id, state := range m.states {
		if _, ok := snap.Backends[id]; !ok {
			state.stopLocked()
			delete(m.states, id)
		}
	}
	for _, backend := range snap.Backends {
		state := m.states[backend.ID]
		if state == nil {
			state = newPublicBackendHealthState(backend)
			m.states[backend.ID] = state
		}
		state.backend = backend

		shouldRunDirect := active &&
			backend.Enabled &&
			backend.BackendType == publicBackendTypeProxyForward &&
			backend.ForwardMode != publicBackendForwardModeAgentPool &&
			backend.HealthCheck.Enabled &&
			backend.ParsedOrigin != nil
		if shouldRunDirect && !state.directCheckRunning {
			ctx, cancel := context.WithCancel(context.Background())
			state.directCancel = cancel
			state.directCheckRunning = true
			go m.directHealthLoop(ctx, backend.ID)
		}
		if !shouldRunDirect {
			state.stopDirectLocked()
		}

		shouldRunAgents := active &&
			backend.Enabled &&
			backend.BackendType == publicBackendTypeProxyForward &&
			backend.ForwardMode == publicBackendForwardModeAgentPool &&
			backend.HealthCheck.Enabled &&
			backend.ParsedOrigin != nil
		desiredAgents := make(map[int64]struct{})
		if shouldRunAgents {
			for _, assignment := range backend.AgentAssignments {
				if !assignment.Enabled {
					continue
				}
				desiredAgents[assignment.AgentID] = struct{}{}
				agentState := state.ensureAgentStateLocked(assignment.AgentID)
				if !agentState.checkRunning {
					ctx, cancel := context.WithCancel(context.Background())
					agentState.cancel = cancel
					agentState.checkRunning = true
					go m.agentHealthLoop(ctx, app, backend.ID, assignment.AgentID)
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
		if !backend.HealthCheck.Enabled {
			state.clearHealthStateLocked()
		}
	}
}

func newPublicBackendHealthState(backend publicBackendConfig) *publicBackendHealthState {
	return &publicBackendHealthState{
		backend:     backend,
		direct:      unknownPublicBackendCheckState(),
		agentStates: make(map[int64]*publicBackendAgentHealthState),
	}
}

func unknownPublicBackendCheckState() publicBackendCheckState {
	return publicBackendCheckState{explicitStatus: p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN}
}

func (s *publicBackendHealthState) stopLocked() {
	s.stopDirectLocked()
	for _, agentState := range s.agentStates {
		agentState.stopLocked()
	}
}

func (s *publicBackendHealthState) stopDirectLocked() {
	if s.directCancel != nil {
		s.directCancel()
		s.directCancel = nil
	}
	s.directCheckRunning = false
}

func (s *publicBackendHealthState) ensureAgentStateLocked(agentID int64) *publicBackendAgentHealthState {
	if s.agentStates == nil {
		s.agentStates = make(map[int64]*publicBackendAgentHealthState)
	}
	agentState := s.agentStates[agentID]
	if agentState == nil {
		agentState = &publicBackendAgentHealthState{
			agentID: agentID,
			state:   unknownPublicBackendCheckState(),
		}
		s.agentStates[agentID] = agentState
	}
	return agentState
}

func (s *publicBackendAgentHealthState) stopLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.checkRunning = false
}

func (s *publicBackendHealthState) clearHealthStateLocked() {
	s.direct = unknownPublicBackendCheckState()
	for _, agentState := range s.agentStates {
		agentState.state = unknownPublicBackendCheckState()
	}
}

func (m *publicBackendHealthMonitor) directHealthLoop(ctx context.Context, backendID int64) {
	for {
		backend, ok := m.backendForDirectCheck(backendID)
		if !ok {
			return
		}
		m.recordDirectExplicitCheck(backendID, runPublicBackendHealthCheck(ctx, backend))
		if !waitPublicBackendHealthInterval(ctx, backend.HealthCheck.Interval) {
			return
		}
	}
}

func (m *publicBackendHealthMonitor) agentHealthLoop(ctx context.Context, app *App, backendID int64, agentID int64) {
	for {
		backend, ok := m.backendForAgentCheck(backendID, agentID)
		if !ok {
			return
		}
		agent := (*AgentConn)(nil)
		if app != nil && app.AgentHub != nil {
			agent = app.AgentHub.connectedByID(agentID)
		}
		if agent == nil {
			m.recordAgentDisconnected(backendID, agentID)
			m.recordAgentActiveCheckSkipped(backendID, agentID, backend, "agent_disconnected", errAgentDisconnected)
		} else {
			m.recordAgentConnected(backendID, agentID, agent.PublicID)
			m.recordAgentExplicitCheck(backendID, agentID, app.runPublicBackendHealthCheckViaAgent(ctx, backend, agent))
		}
		if !waitPublicBackendHealthInterval(ctx, backend.HealthCheck.Interval) {
			return
		}
	}
}

func waitPublicBackendHealthInterval(ctx context.Context, interval time.Duration) bool {
	if interval <= 0 {
		interval = time.Duration(defaultBackendHealthCheckIntervalMillis) * time.Millisecond
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

func (m *publicBackendHealthMonitor) backendForDirectCheck(backendID int64) (publicBackendConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil || !state.directCheckRunning {
		return publicBackendConfig{}, false
	}
	return state.backend, true
}

func (m *publicBackendHealthMonitor) backendForAgentCheck(backendID int64, agentID int64) (publicBackendConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return publicBackendConfig{}, false
	}
	agentState := state.agentStates[agentID]
	if agentState == nil || !agentState.checkRunning {
		return publicBackendConfig{}, false
	}
	return state.backend, true
}

func publicBackendHealthCheckURL(backend publicBackendConfig) (*url.URL, error) {
	if backend.ParsedOrigin == nil {
		return nil, errors.New("backend origin is not configured")
	}
	checkURL := *backend.ParsedOrigin
	checkURL.Path = backend.HealthCheck.Path
	checkURL.RawPath = ""
	checkURL.RawQuery = ""
	checkURL.Fragment = ""
	return &checkURL, nil
}

func checkPublicBackendHealth(parent context.Context, backend publicBackendConfig) error {
	return runPublicBackendHealthCheck(parent, backend).Err
}

func runPublicBackendHealthCheck(parent context.Context, backend publicBackendConfig) publicBackendHealthCheckAttempt {
	attempt := newPublicBackendHealthCheckAttempt(backend)
	defer finishPublicBackendHealthCheckAttempt(&attempt)

	checkURL, err := publicBackendHealthCheckURL(backend)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	attempt.URL = redactSensitiveTraceURL(checkURL.String())
	timeout := backend.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultBackendHealthCheckTimeoutMillis) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, backend.HealthCheck.Method, checkURL.String(), nil)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	applyUpstreamRequestConfig(req, backend)
	client := &http.Client{Transport: directProxyTransport(backend.TLSSkipVerify, timeout)}
	resp, err := client.Do(req)
	if err != nil {
		attempt.fail(classifyHealthCheckErrorKind(ctx, err), err)
		return attempt
	}
	defer resp.Body.Close()
	attempt.StatusCode = int64(resp.StatusCode)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		attempt.fail("response_body_read_failed", fmt.Errorf("drain health check response: %w", err))
		return attempt
	}
	if int64(resp.StatusCode) < backend.HealthCheck.ExpectedStatusMin || int64(resp.StatusCode) > backend.HealthCheck.ExpectedStatusMax {
		attempt.fail("unexpected_status", fmt.Errorf("health check status %d outside expected range %d-%d", resp.StatusCode, backend.HealthCheck.ExpectedStatusMin, backend.HealthCheck.ExpectedStatusMax))
		return attempt
	}
	attempt.ErrorKind = "success"
	return attempt
}

func (a *App) checkPublicBackendHealthViaAgent(parent context.Context, backend publicBackendConfig, agent *AgentConn) error {
	return a.runPublicBackendHealthCheckViaAgent(parent, backend, agent).Err
}

func (a *App) runPublicBackendHealthCheckViaAgent(parent context.Context, backend publicBackendConfig, agent *AgentConn) publicBackendHealthCheckAttempt {
	attempt := newPublicBackendHealthCheckAttempt(backend)
	attempt.DebugAttributes["transport"] = "agent_pool"
	defer finishPublicBackendHealthCheckAttempt(&attempt)

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
	checkURL, err := publicBackendHealthCheckURL(backend)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	attempt.URL = redactSensitiveTraceURL(checkURL.String())
	timeout := backend.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultBackendHealthCheckTimeoutMillis) * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(parent, agentResponseWaitTimeout(timeout))
	defer cancel()
	req, err := http.NewRequestWithContext(waitCtx, backend.HealthCheck.Method, checkURL.String(), nil)
	if err != nil {
		attempt.fail("request_failed", err)
		return attempt
	}
	applyUpstreamRequestConfig(req, backend)

	id, err := uuid.NewV7()
	if err != nil {
		attempt.fail("request_failed", fmt.Errorf("generate health check request id: %w", err))
		return attempt
	}
	attempt.DebugAttributes["agent_request_id"] = id.String()
	attempt.DebugAttributes["response_header_timeout_ms"] = strconv.FormatInt(int64(timeout/time.Millisecond), 10)
	pendingCtx, pendingCancel := context.WithCancel(waitCtx)
	defer pendingCancel()
	pending := &pendingAgentRequest{
		AgentID:       agent.AgentID,
		AgentPublicID: agent.PublicID,
		ResponseCh:    make(chan *msg.Request, 100),
		ErrorCh:       make(chan error, 1),
		ctx:           pendingCtx,
		cancel:        pendingCancel,
	}
	a.PendingRequests.Store(id, pending)
	pendingFinishReason := "health_check_completed"
	defer func() {
		attempt.DebugAttributes["pending_finish_reason"] = pendingFinishReason
		a.finishPendingAgentRequest(id, pendingFinishReason)
	}()

	enc := httpmsg.NewRequestEncoderWithMetadata(id, req, map[string]string{
		httpmsg.MetadataTLSSkipVerify:               strconv.FormatBool(backend.TLSSkipVerify),
		httpmsg.MetadataHealthCheck:                 "true",
		httpmsg.MetadataResponseHeaderTimeoutMillis: strconv.FormatInt(int64(timeout/time.Millisecond), 10),
	})
	for {
		chunk, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			attempt.fail("request_encode_failed", fmt.Errorf("encode health check request: %w", err))
			return attempt
		}
		select {
		case agent.WriteCh <- chunk:
		case <-agent.Done:
			pendingFinishReason = "agent_disconnected"
			attempt.fail("agent_disconnected", errAgentDisconnected)
			return attempt
		case <-waitCtx.Done():
			pendingFinishReason = "health_check_timeout"
			attempt.fail("timeout", waitCtx.Err())
			return attempt
		}
	}

	var firstMsg *msg.Request
	select {
	case <-waitCtx.Done():
		pendingFinishReason = "health_check_timeout"
		attempt.fail("timeout", waitCtx.Err())
		return attempt
	case err := <-pending.ErrorCh:
		pendingFinishReason = "agent_failed"
		attempt.fail("agent_failed", err)
		return attempt
	case firstMsg = <-pending.ResponseCh:
		if firstMsg == nil {
			pendingFinishReason = "agent_disconnected"
			attempt.fail("agent_disconnected", errAgentDisconnected)
			return attempt
		}
	}
	stream := &httpmsg.ChannelStream{Ctx: pendingCtx, Ch: pending.ResponseCh}
	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		pendingFinishReason = "health_check_decode_failed"
		attempt.fail("response_decode_failed", fmt.Errorf("decode health check response: %w", err))
		return attempt
	}
	defer resp.Body.Close()
	attempt.StatusCode = int64(resp.StatusCode)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		attempt.fail("response_body_read_failed", fmt.Errorf("drain health check response: %w", err))
		return attempt
	}
	if int64(resp.StatusCode) < backend.HealthCheck.ExpectedStatusMin || int64(resp.StatusCode) > backend.HealthCheck.ExpectedStatusMax {
		attempt.fail("unexpected_status", fmt.Errorf("health check status %d outside expected range %d-%d", resp.StatusCode, backend.HealthCheck.ExpectedStatusMin, backend.HealthCheck.ExpectedStatusMax))
		return attempt
	}
	attempt.ErrorKind = "success"
	return attempt
}

func newPublicBackendHealthCheckAttempt(backend publicBackendConfig) publicBackendHealthCheckAttempt {
	timeout := backend.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultBackendHealthCheckTimeoutMillis) * time.Millisecond
	}
	attempt := publicBackendHealthCheckAttempt{
		StartedAt:     time.Now(),
		Method:        backend.HealthCheck.Method,
		ExpectedMin:   backend.HealthCheck.ExpectedStatusMin,
		ExpectedMax:   backend.HealthCheck.ExpectedStatusMax,
		Timeout:       timeout,
		TLSSkipVerify: backend.TLSSkipVerify,
		ErrorKind:     "success",
		DebugAttributes: map[string]string{
			"transport":    "direct",
			"timeout_ms":   strconv.FormatInt(int64(timeout/time.Millisecond), 10),
			"check_method": backend.HealthCheck.Method,
			"check_path":   backend.HealthCheck.Path,
		},
	}
	if checkURL, err := publicBackendHealthCheckURL(backend); err == nil {
		attempt.URL = redactSensitiveTraceURL(checkURL.String())
	}
	return attempt
}

func finishPublicBackendHealthCheckAttempt(attempt *publicBackendHealthCheckAttempt) {
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

func (a *publicBackendHealthCheckAttempt) fail(kind string, err error) {
	if kind == "" {
		kind = "request_failed"
	}
	a.ErrorKind = kind
	a.Err = err
}

func newPassiveHealthTraceAttempt(backend publicBackendConfig, agentID int64, agentPublicID string, agentName string, err error) publicBackendHealthCheckAttempt {
	if err == nil {
		err = errors.New("temporary upstream failure")
	}
	attempt := newPublicBackendHealthCheckAttempt(backend)
	finishPublicBackendHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.AgentPublicID = agentPublicID
	attempt.AgentName = agentName
	attempt.ErrorKind = "passive_failure"
	attempt.Err = err
	attempt.DebugAttributes["passive_cooldown_ms"] = strconv.FormatInt(int64(publicBackendPassiveUnhealthyCooldown/time.Millisecond), 10)
	if agentID > 0 {
		attempt.DebugAttributes["transport"] = "agent_pool"
	}
	return attempt
}

func classifyHealthCheckErrorKind(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return "timeout"
	}
	return "request_failed"
}

func (m *publicBackendHealthMonitor) recordDirectExplicitCheck(backendID int64, attempt publicBackendHealthCheckAttempt) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	now := time.Now()
	before := publicBackendHealthTraceStateFromCheck(state.direct, state.backend.HealthCheck.Enabled, now)
	recordPublicBackendCheckResult(&state.direct, state.backend.HealthCheck, attempt.Err)
	after := publicBackendHealthTraceStateFromCheck(state.direct, state.backend.HealthCheck.Enabled, time.Now())
	trace := m.newHealthTraceLocked(
		state.backend,
		p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&state.directTraces, trace)
}

func (m *publicBackendHealthMonitor) recordAgentExplicitCheck(backendID int64, agentID int64, attempt publicBackendHealthCheckAttempt) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
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
	before := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, now)
	recordPublicBackendCheckResult(&agentState.state, state.backend.HealthCheck, attempt.Err)
	after := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	trace := m.newHealthTraceLocked(
		state.backend,
		p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func (m *publicBackendHealthMonitor) recordAgentActiveCheckSkipped(backendID int64, agentID int64, backend publicBackendConfig, errorKind string, err error) {
	if m == nil {
		return
	}
	attempt := newPublicBackendHealthCheckAttempt(backend)
	finishPublicBackendHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.ErrorKind = errorKind
	attempt.Err = err
	attempt.DebugAttributes["transport"] = "agent_pool"
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	agentState := state.ensureAgentStateLocked(agentID)
	attempt.AgentPublicID = agentState.agentPublicID
	before := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	after := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	trace := m.newHealthTraceLocked(
		state.backend,
		p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_ACTIVE_CHECK,
		attempt,
		before,
		after,
	)
	trace.Outcome = p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_SKIPPED
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func recordPublicBackendCheckResult(state *publicBackendCheckState, config publicBackendHealthCheckConfig, err error) {
	if state == nil {
		return
	}
	state.lastCheckedAt = time.Now()
	if err == nil {
		state.healthyStreak++
		state.unhealthyStreak = 0
		state.lastError = ""
		state.passiveUnhealthyUntil = time.Time{}
		if state.healthyStreak >= normalizedThreshold(config.HealthyThreshold, defaultBackendHealthCheckHealthyThreshold) {
			state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY
		}
		return
	}
	state.unhealthyStreak++
	state.healthyStreak = 0
	state.lastError = err.Error()
	if state.unhealthyStreak >= normalizedThreshold(config.UnhealthyThreshold, defaultBackendHealthCheckUnhealthyThreshold) {
		state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY
	}
}

func publicBackendHealthTraceStateFromCheck(state publicBackendCheckState, healthEnabled bool, now time.Time) publicBackendHealthTraceState {
	status, passiveUntil := checkStateSnapshotStatus(state, healthEnabled, now)
	return publicBackendHealthTraceState{
		status:          status,
		available:       checkStateAvailable(state, healthEnabled, now),
		healthyStreak:   state.healthyStreak,
		unhealthyStreak: state.unhealthyStreak,
		passiveUntil:    passiveUntil,
	}
}

func (m *publicBackendHealthMonitor) newHealthTraceLocked(
	backend publicBackendConfig,
	source p2pstreamv1.PublicBackendHealthTraceSource,
	attempt publicBackendHealthCheckAttempt,
	before publicBackendHealthTraceState,
	after publicBackendHealthTraceState,
) *p2pstreamv1.PublicBackendHealthTrace {
	outcome := p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_SUCCESS
	if attempt.Err != nil {
		outcome = p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_FAILURE
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
	trace := &p2pstreamv1.PublicBackendHealthTrace{
		Sequence:                        m.nextHealthTraceSequenceLocked(),
		BackendId:                       backend.ID,
		BackendName:                     backend.Name,
		ForwardMode:                     protoForwardModeFromString(backend.ForwardMode),
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

func (m *publicBackendHealthMonitor) nextHealthTraceSequenceLocked() uint64 {
	m.sequence++
	return m.sequence
}

func (m *publicBackendHealthMonitor) appendHealthTraceLocked(target *[]*p2pstreamv1.PublicBackendHealthTrace, trace *p2pstreamv1.PublicBackendHealthTrace) {
	if trace == nil {
		return
	}
	*target = append(*target, trace)
	if len(*target) <= publicBackendHealthTraceLimitPerTarget {
		return
	}
	copy(*target, (*target)[len(*target)-publicBackendHealthTraceLimitPerTarget:])
	*target = (*target)[:publicBackendHealthTraceLimitPerTarget]
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

func (m *publicBackendHealthMonitor) recordAgentConnected(backendID int64, agentID int64, publicID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	m.recordAgentConnectedLocked(state, agentID, publicID)
}

func (m *publicBackendHealthMonitor) recordAgentConnectedForAll(agentID int64, publicID string) {
	if m == nil || agentID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, state := range m.states {
		if !backendHasAgentAssignment(state.backend, agentID) {
			continue
		}
		m.recordAgentConnectedLocked(state, agentID, publicID)
	}
}

func (m *publicBackendHealthMonitor) recordAgentConnectedLocked(state *publicBackendHealthState, agentID int64, publicID string) {
	agentState := state.ensureAgentStateLocked(agentID)
	wasConnected := agentState.connected
	before := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	agentState.connected = true
	agentState.agentPublicID = publicID
	if !wasConnected {
		agentState.state = unknownPublicBackendCheckState()
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
	after := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	attempt := newPublicBackendHealthCheckAttempt(state.backend)
	finishPublicBackendHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.AgentPublicID = publicID
	attempt.AgentName = agentName
	attempt.ErrorKind = "agent_connected"
	attempt.DebugAttributes["transport"] = "agent_pool"
	attempt.DebugAttributes["agent_event"] = "connected"
	trace := m.newHealthTraceLocked(
		state.backend,
		p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_AGENT_CONNECTIVITY,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func (m *publicBackendHealthMonitor) recordAgentDisconnected(backendID int64, agentID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	m.recordAgentDisconnectedLocked(state, agentID)
}

func (m *publicBackendHealthMonitor) recordAgentDisconnectedForAll(agentID int64) {
	if m == nil || agentID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, state := range m.states {
		if !backendHasAgentAssignment(state.backend, agentID) {
			continue
		}
		m.recordAgentDisconnectedLocked(state, agentID)
	}
}

func (m *publicBackendHealthMonitor) recordAgentDisconnectedLocked(state *publicBackendHealthState, agentID int64) {
	agentState := state.ensureAgentStateLocked(agentID)
	wasConnected := agentState.connected
	before := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	agentState.connected = false
	agentState.state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
	agentState.state.healthyStreak = 0
	agentState.state.unhealthyStreak = 0
	if !wasConnected {
		return
	}
	after := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	attempt := newPublicBackendHealthCheckAttempt(state.backend)
	finishPublicBackendHealthCheckAttempt(&attempt)
	attempt.AgentID = agentID
	attempt.AgentPublicID = agentState.agentPublicID
	attempt.ErrorKind = "agent_disconnected_event"
	attempt.Err = errAgentDisconnected
	attempt.DebugAttributes["transport"] = "agent_pool"
	attempt.DebugAttributes["agent_event"] = "disconnected"
	trace := m.newHealthTraceLocked(
		state.backend,
		p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_AGENT_CONNECTIVITY,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func backendHasAgentAssignment(backend publicBackendConfig, agentID int64) bool {
	for _, assignment := range backend.AgentAssignments {
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

func (m *publicBackendHealthMonitor) markPassiveFailure(backendID int64, err error) {
	if m == nil || backendID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	if !m.passiveFailuresEnabledLocked(state) {
		state.direct = unknownPublicBackendCheckState()
		return
	}
	before := publicBackendHealthTraceStateFromCheck(state.direct, state.backend.HealthCheck.Enabled, time.Now())
	m.markCheckPassiveFailureLocked(&state.direct, err)
	after := publicBackendHealthTraceStateFromCheck(state.direct, state.backend.HealthCheck.Enabled, time.Now())
	attempt := newPassiveHealthTraceAttempt(state.backend, 0, "", "", err)
	trace := m.newHealthTraceLocked(
		state.backend,
		p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_PASSIVE_FAILURE,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&state.directTraces, trace)
}

func (m *publicBackendHealthMonitor) markAgentPassiveFailure(backendID int64, agentID int64, err error) {
	if m == nil || backendID <= 0 || agentID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	agentState := state.agentStates[agentID]
	if !m.passiveFailuresEnabledLocked(state) {
		if agentState != nil {
			agentState.state = unknownPublicBackendCheckState()
		}
		return
	}
	if agentState == nil {
		agentState = state.ensureAgentStateLocked(agentID)
	}
	before := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	m.markCheckPassiveFailureLocked(&agentState.state, err)
	after := publicBackendHealthTraceStateFromCheck(agentState.state, state.backend.HealthCheck.Enabled, time.Now())
	attempt := newPassiveHealthTraceAttempt(state.backend, agentID, agentState.agentPublicID, "", err)
	trace := m.newHealthTraceLocked(
		state.backend,
		p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_PASSIVE_FAILURE,
		attempt,
		before,
		after,
	)
	m.appendHealthTraceLocked(&agentState.traces, trace)
}

func (m *publicBackendHealthMonitor) passiveFailuresEnabledLocked(state *publicBackendHealthState) bool {
	return state != nil && state.backend.HealthCheck.Enabled
}

func (m *publicBackendHealthMonitor) markCheckPassiveFailureLocked(state *publicBackendCheckState, err error) {
	state.passiveUnhealthyUntil = time.Now().Add(publicBackendPassiveUnhealthyCooldown)
	if err != nil {
		state.lastError = err.Error()
	} else {
		state.lastError = "temporary upstream failure"
	}
}

func (m *publicBackendHealthMonitor) beginRequest(backendID int64) func() {
	if m == nil || backendID <= 0 {
		return func() {}
	}
	m.mu.Lock()
	state := m.ensureStateLocked(backendID)
	m.mu.Unlock()
	state.activeRequests.Add(1)
	return func() {
		state.activeRequests.Add(-1)
	}
}

func (m *publicBackendHealthMonitor) activeRequests(backendID int64) int64 {
	if m == nil || backendID <= 0 {
		return 0
	}
	m.mu.Lock()
	state := m.states[backendID]
	m.mu.Unlock()
	if state == nil {
		return 0
	}
	return state.activeRequests.Load()
}

func (m *publicBackendHealthMonitor) listHealthTraces(backendID int64, agentID int64, limit int64, failuresOnly bool) ([]*p2pstreamv1.PublicBackendHealthTrace, int64) {
	if limit <= 0 || limit > publicBackendHealthTraceLimitPerTarget {
		limit = publicBackendHealthTraceLimitPerTarget
	}
	if m == nil || backendID <= 0 {
		return nil, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return nil, 0
	}
	traces := make([]*p2pstreamv1.PublicBackendHealthTrace, 0)
	if state.backend.ForwardMode == publicBackendForwardModeAgentPool {
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
	resp := make([]*p2pstreamv1.PublicBackendHealthTrace, 0, minInt64(limit, retained))
	for _, trace := range traces {
		if failuresOnly &&
			trace.Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_FAILURE &&
			trace.Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_SKIPPED {
			continue
		}
		resp = append(resp, cloneHealthTrace(trace))
		if int64(len(resp)) >= limit {
			break
		}
	}
	return resp, retained
}

func cloneHealthTrace(trace *p2pstreamv1.PublicBackendHealthTrace) *p2pstreamv1.PublicBackendHealthTrace {
	if trace == nil {
		return nil
	}
	copyTrace, ok := proto.Clone(trace).(*p2pstreamv1.PublicBackendHealthTrace)
	if !ok {
		return &p2pstreamv1.PublicBackendHealthTrace{}
	}
	return copyTrace
}

func minInt64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (m *publicBackendHealthMonitor) available(backend publicBackendConfig) bool {
	if m == nil {
		return true
	}
	if !backend.Enabled {
		return false
	}
	if backend.ForwardMode == publicBackendForwardModeAgentPool {
		return m.backendAgentPoolAvailable(backend)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backend.ID]
	if state == nil {
		return true
	}
	return checkStateAvailable(state.direct, backend.HealthCheck.Enabled, time.Now())
}

func (m *publicBackendHealthMonitor) backendAgentPoolAvailable(backend publicBackendConfig) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, assignment := range backend.AgentAssignments {
		if !assignment.Enabled {
			continue
		}
		if !m.agentConnectedLocked(assignment.AgentID) && m.app != nil {
			continue
		}
		if m.agentAvailableLocked(backend.ID, assignment.AgentID, backend.HealthCheck.Enabled, now) {
			return true
		}
	}
	return false
}

func (m *publicBackendHealthMonitor) agentAvailable(backendID int64, agentID int64) bool {
	if m == nil || backendID <= 0 || agentID <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	healthEnabled := false
	if state != nil {
		healthEnabled = state.backend.HealthCheck.Enabled
	}
	return m.agentAvailableLocked(backendID, agentID, healthEnabled, time.Now())
}

func (m *publicBackendHealthMonitor) agentAvailableLocked(backendID int64, agentID int64, healthEnabled bool, now time.Time) bool {
	state := m.states[backendID]
	if state == nil {
		return true
	}
	agentState := state.agentStates[agentID]
	if agentState == nil {
		return true
	}
	return checkStateAvailable(agentState.state, healthEnabled, now)
}

func checkStateAvailable(state publicBackendCheckState, healthEnabled bool, now time.Time) bool {
	if !healthEnabled {
		return true
	}
	if state.passiveUnhealthyUntil.After(now) {
		return false
	}
	return state.explicitStatus != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY &&
		state.explicitStatus != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED
}

func (m *publicBackendHealthMonitor) snapshot(backend dbPublicBackendLike) *publicBackendHealthSnapshot {
	if m == nil || backend.backendID() <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backend.backendID()]
	if state == nil {
		return nil
	}
	if state.backend.ForwardMode == publicBackendForwardModeAgentPool {
		return m.agentPoolSnapshotLocked(state, backend.backendEnabled())
	}
	return directHealthSnapshot(state, backend.backendEnabled())
}

func (m *publicBackendHealthMonitor) agentSnapshot(backendID int64, agentID int64, assignmentEnabled bool, agentEnabled bool) *publicBackendAgentHealthSnapshot {
	if !assignmentEnabled || !agentEnabled {
		return &publicBackendAgentHealthSnapshot{
			Status:    p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED,
			Available: false,
		}
	}
	if m == nil || backendID <= 0 || agentID <= 0 {
		return &publicBackendAgentHealthSnapshot{
			Status:    p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN,
			Available: false,
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	conn := m.connectedAgentLocked(agentID)
	if conn == nil {
		return &publicBackendAgentHealthSnapshot{
			Status:    p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISCONNECTED,
			Available: false,
		}
	}

	activeRequests := conn.ActiveRequests.Load()
	state := m.states[backendID]
	healthEnabled := false
	if state != nil {
		healthEnabled = state.backend.HealthCheck.Enabled
	}
	if state == nil {
		return &publicBackendAgentHealthSnapshot{
			Status:         p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN,
			Connected:      true,
			Available:      true,
			ActiveRequests: activeRequests,
		}
	}

	agentState := state.agentStates[agentID]
	if agentState == nil {
		return &publicBackendAgentHealthSnapshot{
			Status:         p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN,
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
	return &publicBackendAgentHealthSnapshot{
		Status:                          status,
		Connected:                       true,
		Available:                       checkStateAvailable(agentState.state, healthEnabled, now),
		LastCheckedAtUnixMillis:         lastChecked,
		LastError:                       lastError,
		PassiveUnhealthyUntilUnixMillis: passiveUntil,
		ActiveRequests:                  activeRequests,
	}
}

func directHealthSnapshot(state *publicBackendHealthState, enabled bool) *publicBackendHealthSnapshot {
	now := time.Now()
	status, passiveUntil := checkStateSnapshotStatus(state.direct, state.backend.HealthCheck.Enabled, now)
	lastChecked := unixMillis(state.direct.lastCheckedAt)
	lastError := state.direct.lastError
	if !state.backend.HealthCheck.Enabled {
		lastChecked = 0
		lastError = ""
	}
	if !enabled {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED
		passiveUntil = 0
	}
	return &publicBackendHealthSnapshot{
		Status:                          status,
		LastCheckedAtUnixMillis:         lastChecked,
		LastError:                       lastError,
		PassiveUnhealthyUntilUnixMillis: passiveUntil,
	}
}

func (m *publicBackendHealthMonitor) agentPoolSnapshotLocked(state *publicBackendHealthState, enabled bool) *publicBackendHealthSnapshot {
	if !enabled {
		return &publicBackendHealthSnapshot{Status: p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED}
	}
	if !state.backend.HealthCheck.Enabled {
		return &publicBackendHealthSnapshot{Status: p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN}
	}
	now := time.Now()
	connectedCount := 0
	unhealthyCount := 0
	healthyCount := 0
	var lastChecked time.Time
	var lastErrorAt time.Time
	lastError := ""
	var passiveUntil time.Time
	unhealthyDuePassive := false

	for _, assignment := range state.backend.AgentAssignments {
		if !assignment.Enabled {
			continue
		}
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
		status, agentPassiveUntil := checkStateSnapshotStatus(agentState.state, state.backend.HealthCheck.Enabled, now)
		if !agentState.state.lastCheckedAt.IsZero() && agentState.state.lastCheckedAt.After(lastChecked) {
			lastChecked = agentState.state.lastCheckedAt
		}
		if state.backend.HealthCheck.Enabled && agentState.state.lastError != "" && (lastErrorAt.IsZero() || agentState.state.lastCheckedAt.After(lastErrorAt)) {
			lastErrorAt = agentState.state.lastCheckedAt
			lastError = formatAgentHealthError(publicID, assignment.AgentID, agentState.state.lastError)
		}
		if agentPassiveUntil > 0 {
			if until := time.UnixMilli(agentPassiveUntil); until.After(passiveUntil) {
				passiveUntil = until
			}
		}
		switch status {
		case p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY:
			healthyCount++
		case p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY,
			p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED:
			unhealthyCount++
			if agentPassiveUntil > 0 {
				unhealthyDuePassive = true
			}
		}
	}

	status := p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
	outputPassiveUntil := int64(0)
	if healthyCount > 0 {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY
	} else if connectedCount > 0 && unhealthyCount == connectedCount {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY
		if unhealthyDuePassive && passiveUntil.After(now) {
			outputPassiveUntil = passiveUntil.UnixMilli()
		}
	}

	return &publicBackendHealthSnapshot{
		Status:                          status,
		LastCheckedAtUnixMillis:         unixMillis(lastChecked),
		LastError:                       lastError,
		PassiveUnhealthyUntilUnixMillis: outputPassiveUntil,
	}
}

func checkStateSnapshotStatus(state publicBackendCheckState, healthEnabled bool, now time.Time) (p2pstreamv1.PublicBackendHealthStatus, int64) {
	if !healthEnabled {
		return p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN, 0
	}
	status := state.explicitStatus
	if status == p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNSPECIFIED {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
	}
	if state.passiveUnhealthyUntil.After(now) {
		return p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY, state.passiveUnhealthyUntil.UnixMilli()
	}
	return status, 0
}

func (m *publicBackendHealthMonitor) connectedAgentLocked(agentID int64) *AgentConn {
	if m.app == nil || m.app.AgentHub == nil {
		return nil
	}
	return m.app.AgentHub.connectedByID(agentID)
}

func (m *publicBackendHealthMonitor) agentConnectedLocked(agentID int64) bool {
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

func (m *publicBackendHealthMonitor) ensureStateLocked(backendID int64) *publicBackendHealthState {
	state := m.states[backendID]
	if state == nil {
		state = newPublicBackendHealthState(publicBackendConfig{ID: backendID, Enabled: true})
		m.states[backendID] = state
	}
	return state
}

type dbPublicBackendLike interface {
	backendID() int64
	backendEnabled() bool
}

type publicBackendHealthDBAdapter struct {
	id      int64
	enabled bool
}

func (a publicBackendHealthDBAdapter) backendID() int64 {
	return a.id
}

func (a publicBackendHealthDBAdapter) backendEnabled() bool {
	return a.enabled
}
