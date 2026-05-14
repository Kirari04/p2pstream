package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/httpmsg"
	"p2pstream/msg"
)

const publicBackendPassiveUnhealthyCooldown = 30 * time.Second

type publicBackendHealthSnapshot struct {
	Status                          p2pstreamv1.PublicBackendHealthStatus
	LastCheckedAtUnixMillis         int64
	LastError                       string
	PassiveUnhealthyUntilUnixMillis int64
}

type publicBackendHealthMonitor struct {
	mu     sync.Mutex
	app    *App
	states map[int64]*publicBackendHealthState
}

type publicBackendHealthState struct {
	backend            publicBackendConfig
	directCancel       context.CancelFunc
	directCheckRunning bool
	direct             publicBackendCheckState
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
}

type publicBackendCheckState struct {
	explicitStatus        p2pstreamv1.PublicBackendHealthStatus
	lastCheckedAt         time.Time
	lastError             string
	passiveUnhealthyUntil time.Time
	healthyStreak         int64
	unhealthyStreak       int64
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

func (m *publicBackendHealthMonitor) directHealthLoop(ctx context.Context, backendID int64) {
	for {
		backend, ok := m.backendForDirectCheck(backendID)
		if !ok {
			return
		}
		m.recordDirectExplicitCheck(backendID, checkPublicBackendHealth(ctx, backend))
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
		} else {
			m.recordAgentConnected(backendID, agentID, agent.PublicID)
			m.recordAgentExplicitCheck(backendID, agentID, app.checkPublicBackendHealthViaAgent(ctx, backend, agent))
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
	checkURL, err := publicBackendHealthCheckURL(backend)
	if err != nil {
		return err
	}
	timeout := backend.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultBackendHealthCheckTimeoutMillis) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, backend.HealthCheck.Method, checkURL.String(), nil)
	if err != nil {
		return err
	}
	applyUpstreamRequestConfig(req, backend)
	client := &http.Client{Transport: directProxyTransport(backend.TLSSkipVerify)}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("drain health check response: %w", err)
	}
	if int64(resp.StatusCode) < backend.HealthCheck.ExpectedStatusMin || int64(resp.StatusCode) > backend.HealthCheck.ExpectedStatusMax {
		return fmt.Errorf("health check status %d outside expected range %d-%d", resp.StatusCode, backend.HealthCheck.ExpectedStatusMin, backend.HealthCheck.ExpectedStatusMax)
	}
	return nil
}

func (a *App) checkPublicBackendHealthViaAgent(parent context.Context, backend publicBackendConfig, agent *AgentConn) error {
	if a == nil {
		return errors.New("app is not configured")
	}
	if agent == nil {
		return errAgentDisconnected
	}
	checkURL, err := publicBackendHealthCheckURL(backend)
	if err != nil {
		return err
	}
	timeout := backend.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultBackendHealthCheckTimeoutMillis) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, backend.HealthCheck.Method, checkURL.String(), nil)
	if err != nil {
		return err
	}
	applyUpstreamRequestConfig(req, backend)

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate health check request id: %w", err)
	}
	pendingCtx, pendingCancel := context.WithCancel(ctx)
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
	defer a.PendingRequests.Delete(id)

	enc := httpmsg.NewRequestEncoderWithMetadata(id, req, map[string]string{
		httpmsg.MetadataTLSSkipVerify: strconv.FormatBool(backend.TLSSkipVerify),
		httpmsg.MetadataHealthCheck:   "true",
	})
	for {
		chunk, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("encode health check request: %w", err)
		}
		select {
		case agent.WriteCh <- chunk:
		case <-agent.Done:
			return errAgentDisconnected
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var firstMsg *msg.Request
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-pending.ErrorCh:
		return err
	case firstMsg = <-pending.ResponseCh:
		if firstMsg == nil {
			return errAgentDisconnected
		}
	}
	stream := &httpmsg.ChannelStream{Ctx: pendingCtx, Ch: pending.ResponseCh}
	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		return fmt.Errorf("decode health check response: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("drain health check response: %w", err)
	}
	if int64(resp.StatusCode) < backend.HealthCheck.ExpectedStatusMin || int64(resp.StatusCode) > backend.HealthCheck.ExpectedStatusMax {
		return fmt.Errorf("health check status %d outside expected range %d-%d", resp.StatusCode, backend.HealthCheck.ExpectedStatusMin, backend.HealthCheck.ExpectedStatusMax)
	}
	return nil
}

func (m *publicBackendHealthMonitor) recordDirectExplicitCheck(backendID int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	recordPublicBackendCheckResult(&state.direct, state.backend.HealthCheck, err)
}

func (m *publicBackendHealthMonitor) recordAgentExplicitCheck(backendID int64, agentID int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	agentState := state.ensureAgentStateLocked(agentID)
	recordPublicBackendCheckResult(&agentState.state, state.backend.HealthCheck, err)
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

func (m *publicBackendHealthMonitor) recordAgentConnected(backendID int64, agentID int64, publicID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	agentState := state.ensureAgentStateLocked(agentID)
	agentState.connected = true
	agentState.agentPublicID = publicID
}

func (m *publicBackendHealthMonitor) recordAgentDisconnected(backendID int64, agentID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	agentState := state.ensureAgentStateLocked(agentID)
	agentState.connected = false
	agentState.state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
	agentState.state.healthyStreak = 0
	agentState.state.unhealthyStreak = 0
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
	state := m.ensureStateLocked(backendID)
	m.markCheckPassiveFailureLocked(&state.direct, err)
}

func (m *publicBackendHealthMonitor) markAgentPassiveFailure(backendID int64, agentID int64, err error) {
	if m == nil || backendID <= 0 || agentID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.ensureStateLocked(backendID)
	agentState := state.ensureAgentStateLocked(agentID)
	m.markCheckPassiveFailureLocked(&agentState.state, err)
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
	if state.passiveUnhealthyUntil.After(now) {
		return false
	}
	if !healthEnabled {
		return true
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

func directHealthSnapshot(state *publicBackendHealthState, enabled bool) *publicBackendHealthSnapshot {
	now := time.Now()
	status, passiveUntil := checkStateSnapshotStatus(state.direct, state.backend.HealthCheck.Enabled, now)
	if !enabled {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED
		passiveUntil = 0
	}
	return &publicBackendHealthSnapshot{
		Status:                          status,
		LastCheckedAtUnixMillis:         unixMillis(state.direct.lastCheckedAt),
		LastError:                       state.direct.lastError,
		PassiveUnhealthyUntilUnixMillis: passiveUntil,
	}
}

func (m *publicBackendHealthMonitor) agentPoolSnapshotLocked(state *publicBackendHealthState, enabled bool) *publicBackendHealthSnapshot {
	if !enabled {
		return &publicBackendHealthSnapshot{Status: p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED}
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
		if agentState.state.lastError != "" && (lastErrorAt.IsZero() || agentState.state.lastCheckedAt.After(lastErrorAt)) {
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
	status := state.explicitStatus
	if status == p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNSPECIFIED {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
	}
	if state.passiveUnhealthyUntil.After(now) {
		return p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY, state.passiveUnhealthyUntil.UnixMilli()
	}
	if !healthEnabled {
		return p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN, 0
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
