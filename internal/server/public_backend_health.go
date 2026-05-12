package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
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
	states map[int64]*publicBackendHealthState
}

type publicBackendHealthState struct {
	backend               publicBackendConfig
	cancel                context.CancelFunc
	checkRunning          bool
	explicitStatus        p2pstreamv1.PublicBackendHealthStatus
	lastCheckedAt         time.Time
	lastError             string
	passiveUnhealthyUntil time.Time
	healthyStreak         int64
	unhealthyStreak       int64
	activeRequests        atomic.Int64
}

func newPublicBackendHealthMonitor() *publicBackendHealthMonitor {
	return &publicBackendHealthMonitor{states: make(map[int64]*publicBackendHealthState)}
}

func (m *publicBackendHealthMonitor) reconcile(snap *publicProxySnapshot, active bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if snap == nil {
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
			state = &publicBackendHealthState{backend: backend, explicitStatus: p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN}
			m.states[backend.ID] = state
		}
		state.backend = backend
		shouldRun := active &&
			backend.Enabled &&
			backend.BackendType == publicBackendTypeProxyForward &&
			backend.HealthCheck.Enabled &&
			backend.ParsedOrigin != nil
		if shouldRun && !state.checkRunning {
			ctx, cancel := context.WithCancel(context.Background())
			state.cancel = cancel
			state.checkRunning = true
			go m.healthLoop(ctx, backend.ID)
		}
		if !shouldRun {
			state.stopLocked()
			if !backend.Enabled {
				state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED
			} else if !backend.HealthCheck.Enabled && state.explicitStatus == p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED {
				state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
			}
		}
	}
}

func (s *publicBackendHealthState) stopLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.checkRunning = false
}

func (m *publicBackendHealthMonitor) healthLoop(ctx context.Context, backendID int64) {
	for {
		backend, ok := m.backendForCheck(backendID)
		if !ok {
			return
		}
		m.recordExplicitCheck(backendID, checkPublicBackendHealth(ctx, backend))
		interval := backend.HealthCheck.Interval
		if interval <= 0 {
			interval = time.Duration(defaultBackendHealthCheckIntervalMillis) * time.Millisecond
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *publicBackendHealthMonitor) backendForCheck(backendID int64) (publicBackendConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil || !state.checkRunning {
		return publicBackendConfig{}, false
	}
	return state.backend, true
}

func checkPublicBackendHealth(parent context.Context, backend publicBackendConfig) error {
	if backend.ParsedOrigin == nil {
		return errors.New("backend origin is not configured")
	}
	checkURL := *backend.ParsedOrigin
	checkURL.Path = backend.HealthCheck.Path
	checkURL.RawPath = ""
	checkURL.RawQuery = ""
	checkURL.Fragment = ""
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
	client := &http.Client{Transport: directProxyTransport(backend.TLSSkipVerify)}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if int64(resp.StatusCode) < backend.HealthCheck.ExpectedStatusMin || int64(resp.StatusCode) > backend.HealthCheck.ExpectedStatusMax {
		return fmt.Errorf("health check status %d outside expected range %d-%d", resp.StatusCode, backend.HealthCheck.ExpectedStatusMin, backend.HealthCheck.ExpectedStatusMax)
	}
	return nil
}

func (m *publicBackendHealthMonitor) recordExplicitCheck(backendID int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backendID]
	if state == nil {
		return
	}
	state.lastCheckedAt = time.Now()
	if err == nil {
		state.healthyStreak++
		state.unhealthyStreak = 0
		state.lastError = ""
		state.passiveUnhealthyUntil = time.Time{}
		if state.healthyStreak >= normalizedThreshold(state.backend.HealthCheck.HealthyThreshold, defaultBackendHealthCheckHealthyThreshold) {
			state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY
		}
		return
	}
	state.unhealthyStreak++
	state.healthyStreak = 0
	state.lastError = err.Error()
	if state.unhealthyStreak >= normalizedThreshold(state.backend.HealthCheck.UnhealthyThreshold, defaultBackendHealthCheckUnhealthyThreshold) {
		state.explicitStatus = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[backend.ID]
	if state == nil {
		return true
	}
	now := time.Now()
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
	status := state.explicitStatus
	if status == p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNSPECIFIED {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
	}
	passiveUntil := int64(0)
	if state.passiveUnhealthyUntil.After(time.Now()) {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY
		passiveUntil = state.passiveUnhealthyUntil.UnixMilli()
	}
	if !backend.backendEnabled() {
		status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED
	}
	lastChecked := int64(0)
	if !state.lastCheckedAt.IsZero() {
		lastChecked = state.lastCheckedAt.UnixMilli()
	}
	return &publicBackendHealthSnapshot{
		Status:                          status,
		LastCheckedAtUnixMillis:         lastChecked,
		LastError:                       state.lastError,
		PassiveUnhealthyUntilUnixMillis: passiveUntil,
	}
}

func (m *publicBackendHealthMonitor) ensureStateLocked(backendID int64) *publicBackendHealthState {
	state := m.states[backendID]
	if state == nil {
		state = &publicBackendHealthState{backend: publicBackendConfig{ID: backendID, Enabled: true}, explicitStatus: p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN}
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

func healthCheckURLForTest(origin *url.URL, path string) string {
	if origin == nil {
		return ""
	}
	next := *origin
	next.Path = path
	next.RawQuery = ""
	next.Fragment = ""
	return next.String()
}
