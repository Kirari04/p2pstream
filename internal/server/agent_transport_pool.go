package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"p2pstream/internal/db"
)

type agentTransportKind string

const (
	agentTransportKindRouteTarget agentTransportKind = "route_target"
	agentTransportKindEnvironment agentTransportKind = "environment"
)

type agentTransportKey struct {
	Kind                        agentTransportKind
	AgentID                     int64
	RouteTargetID               int64
	EnvironmentID               int64
	TargetOrigin                string
	ManagementURL               string
	TLSSkipVerify               bool
	TrustedCertificateSHA256    string
	ResponseHeaderTimeoutMillis int64
}

type pooledAgentTransport struct {
	pool      *agentTransportPool
	key       agentTransportKey
	agent     *AgentConn
	transport *http.Transport
	oneShot   http.RoundTripper
	createdAt time.Time
	lastUsed  time.Time
	inFlight  int
	idleHint  bool
	retired   bool
}

type agentTransportPool struct {
	mu               sync.Mutex
	entries          map[agentTransportKey]*pooledAgentTransport
	draining         map[*pooledAgentTransport]struct{}
	maxEntries       int
	limitInitialized bool

	reclaimAttempts         atomic.Uint64
	reclaimSuccesses        atomic.Uint64
	reclaimNoCandidate      atomic.Uint64
	fallbackAttempts        atomic.Uint64
	fallbackRecovered       atomic.Uint64
	fallbackFailed          atomic.Uint64
	terminalCapacityFailure atomic.Uint64
}

type agentTransportPoolStats struct {
	ReclaimAttempts         uint64
	ReclaimSuccesses        uint64
	ReclaimNoCandidate      uint64
	FallbackAttempts        uint64
	FallbackRecovered       uint64
	FallbackFailed          uint64
	TerminalCapacityFailure uint64
}

const maxAgentTransportPoolEntries = 256

type agentDialRequestIDContextKey struct{}

func newAgentTransportPool() *agentTransportPool {
	return &agentTransportPool{
		entries:  make(map[agentTransportKey]*pooledAgentTransport),
		draining: make(map[*pooledAgentTransport]struct{}),
	}
}

func (a *App) CloseAgentTransports() {
	if a == nil || a.AgentTransports == nil {
		return
	}
	a.AgentTransports.closeAll()
}

func withAgentDialRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, agentDialRequestIDContextKey{}, requestID)
}

func agentDialRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(agentDialRequestIDContextKey{}).(string)
	return requestID
}

func (p *agentTransportPool) publicRouteTargetTransport(app *App, agent *AgentConn, target publicRouteTargetConfig) http.RoundTripper {
	timeout := normalizeUpstreamResponseHeaderTimeout(target.UpstreamResponseHeaderTimeout)
	key := agentTransportKey{
		Kind:                        agentTransportKindRouteTarget,
		AgentID:                     agent.AgentID,
		RouteTargetID:               target.ID,
		TargetOrigin:                routeTargetTransportOrigin(target),
		TLSSkipVerify:               target.TLSSkipVerify,
		ResponseHeaderTimeoutMillis: int64(timeout / time.Millisecond),
	}
	var tlsConfig *tls.Config
	if target.TLSSkipVerify {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return p.getOrCreate(app, agent, key, tlsConfig, timeout)
}

func (p *agentTransportPool) publicRouteTargetOneShotTransport(app *App, agent *AgentConn, target publicRouteTargetConfig) http.RoundTripper {
	return newAgentRouteTargetOneShotTransport(app, agent, target, agentStreamCapacityPublicOneShot)
}

func (p *agentTransportPool) publicRouteTargetHealthTransport(app *App, agent *AgentConn, target publicRouteTargetConfig) http.RoundTripper {
	return newAgentRouteTargetOneShotTransport(app, agent, target, agentStreamCapacityTrustedHealth)
}

func newAgentRouteTargetOneShotTransport(app *App, agent *AgentConn, target publicRouteTargetConfig, class agentStreamCapacityClass) http.RoundTripper {
	timeout := normalizeUpstreamResponseHeaderTimeout(target.UpstreamResponseHeaderTimeout)
	var tlsConfig *tls.Config
	if target.TLSSkipVerify {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}
	queueKey := fmt.Sprintf("route-target:%d", target.ID)
	return newAgentHTTPTransport(app, agent, tlsConfig, timeout, queueKey, class, true)
}

func (p *agentTransportPool) environmentTransport(app *App, agent *AgentConn, env db.Environment, tlsConfig *tls.Config) http.RoundTripper {
	timeout := environmentResponseHeaderTimeout(env)
	key := agentTransportKey{
		Kind:                        agentTransportKindEnvironment,
		AgentID:                     agent.AgentID,
		EnvironmentID:               env.ID,
		ManagementURL:               env.ManagementUrl,
		TrustedCertificateSHA256:    normalizeEnvironmentCertificateFingerprint(env.TrustedCertificateSha256),
		ResponseHeaderTimeoutMillis: int64(timeout / time.Millisecond),
	}
	// Environment proxy requests may carry non-replayable bodies. Select a
	// one-shot transport before RoundTrip so pooled admission can never require
	// replaying a body that net/http may already have closed or consumed.
	return newAgentHTTPTransport(app, agent, tlsConfig, timeout, agentTransportCapacityQueueKey(key), agentStreamCapacityPublicOneShot, true)
}

func (p *agentTransportPool) getOrCreate(app *App, agent *AgentConn, key agentTransportKey, tlsConfig *tls.Config, timeout time.Duration) http.RoundTripper {
	queueKey := agentTransportCapacityQueueKey(key)
	if p == nil {
		return newAgentPooledHTTPTransport(app, agent, tlsConfig, timeout, queueKey)
	}
	oneShot := newAgentHTTPTransport(app, agent, tlsConfig, timeout, queueKey, agentStreamCapacityPublicOneShot, true)
	var closeIdle []*http.Transport
	p.mu.Lock()
	if existing := p.entries[key]; existing != nil {
		if existing.agent == agent && existing.transport != nil && !existing.retired {
			p.mu.Unlock()
			return existing
		}
		if transport := p.retireLocked(key, existing); transport != nil {
			closeIdle = append(closeIdle, transport)
		}
	}
	p.initializeLimitLocked(app)
	for p.retainedEntriesLocked() >= p.maxEntries && p.maxEntries > 0 {
		candidateKey, candidate := p.oldestIdleEntryLocked()
		if candidate == nil {
			break
		}
		if transport := p.retireLocked(candidateKey, candidate); transport != nil {
			closeIdle = append(closeIdle, transport)
		}
	}
	if p.maxEntries == 0 || p.retainedEntriesLocked() >= p.maxEntries {
		p.mu.Unlock()
		closeAgentIdleTransports(closeIdle)
		return oneShot
	}
	now := time.Now()
	entry := &pooledAgentTransport{
		pool:      p,
		key:       key,
		agent:     agent,
		transport: newAgentPooledHTTPTransport(app, agent, tlsConfig, timeout, queueKey),
		oneShot:   oneShot,
		createdAt: now,
		lastUsed:  now,
	}
	p.entries[key] = entry
	p.mu.Unlock()
	closeAgentIdleTransports(closeIdle)
	return entry
}

func (p *agentTransportPool) initializeLimitLocked(app *App) {
	if p.limitInitialized {
		return
	}
	p.limitInitialized = true
	if app == nil || app.agentStreamCapacity == nil {
		return
	}
	p.maxEntries = app.agentStreamCapacity.snapshot().Pooled.Capacity
	if p.maxEntries > maxAgentTransportPoolEntries {
		p.maxEntries = maxAgentTransportPoolEntries
	}
}

func (p *agentTransportPool) retainedEntriesLocked() int {
	return len(p.entries) + len(p.draining)
}

func (p *agentTransportPool) oldestIdleEntryLocked() (agentTransportKey, *pooledAgentTransport) {
	var oldestKey agentTransportKey
	var oldest *pooledAgentTransport
	for key, entry := range p.entries {
		if entry == nil || entry.retired || entry.inFlight != 0 {
			continue
		}
		if oldest == nil || entry.lastUsed.Before(oldest.lastUsed) {
			oldestKey, oldest = key, entry
		}
	}
	return oldestKey, oldest
}

// reclaimOldestIdle retires at most one shard when pooled admission is under
// pressure. idleHint is deliberately advisory: net/http does not expose exact
// global idle ownership. The zero in-flight condition makes CloseIdleConnections
// safe, and the capacity manager remains the source of truth because a Yamux
// permit is not reusable until peer FIN is observed.
func (p *agentTransportPool) reclaimOldestIdle(preferredAgent *AgentConn, allowOtherAgents bool) bool {
	if p == nil {
		return false
	}
	p.reclaimAttempts.Add(1)
	var candidateKey agentTransportKey
	var candidate *pooledAgentTransport
	p.mu.Lock()
	selectCandidate := func(match func(*pooledAgentTransport) bool) {
		for key, entry := range p.entries {
			if entry == nil || entry.retired || entry.inFlight != 0 || !entry.idleHint || !match(entry) {
				continue
			}
			if candidate == nil || entry.lastUsed.Before(candidate.lastUsed) {
				candidateKey, candidate = key, entry
			}
		}
	}
	if preferredAgent != nil {
		selectCandidate(func(entry *pooledAgentTransport) bool { return entry.agent == preferredAgent })
	}
	if candidate == nil && allowOtherAgents {
		selectCandidate(func(*pooledAgentTransport) bool { return true })
	}
	if candidate == nil {
		p.mu.Unlock()
		p.reclaimNoCandidate.Add(1)
		return false
	}
	transport := p.retireLocked(candidateKey, candidate)
	p.mu.Unlock()
	if transport != nil {
		transport.CloseIdleConnections()
		p.reclaimSuccesses.Add(1)
		return true
	}
	p.reclaimNoCandidate.Add(1)
	return false
}

func (p *agentTransportPool) recordFallbackResult(recovered bool) {
	if p == nil {
		return
	}
	p.fallbackAttempts.Add(1)
	if recovered {
		p.fallbackRecovered.Add(1)
		return
	}
	p.fallbackFailed.Add(1)
}

func (p *agentTransportPool) recordTerminalCapacityFailure() {
	if p != nil {
		p.terminalCapacityFailure.Add(1)
	}
}

func (p *agentTransportPool) stats() agentTransportPoolStats {
	if p == nil {
		return agentTransportPoolStats{}
	}
	return agentTransportPoolStats{
		ReclaimAttempts:         p.reclaimAttempts.Load(),
		ReclaimSuccesses:        p.reclaimSuccesses.Load(),
		ReclaimNoCandidate:      p.reclaimNoCandidate.Load(),
		FallbackAttempts:        p.fallbackAttempts.Load(),
		FallbackRecovered:       p.fallbackRecovered.Load(),
		FallbackFailed:          p.fallbackFailed.Load(),
		TerminalCapacityFailure: p.terminalCapacityFailure.Load(),
	}
}

func (p *agentTransportPool) retireLocked(key agentTransportKey, entry *pooledAgentTransport) *http.Transport {
	if entry == nil || entry.retired {
		return nil
	}
	if p.entries[key] == entry {
		delete(p.entries, key)
	}
	entry.retired = true
	if entry.inFlight > 0 {
		p.draining[entry] = struct{}{}
	}
	return entry.transport
}

func closeAgentIdleTransports(transports []*http.Transport) {
	for _, transport := range transports {
		if transport != nil {
			transport.CloseIdleConnections()
		}
	}
}

func (entry *pooledAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if entry == nil || entry.pool == nil {
		return nil, fmt.Errorf("agent transport entry is unavailable")
	}
	entry.pool.mu.Lock()
	if entry.retired || entry.transport == nil {
		oneShot := entry.oneShot
		entry.pool.mu.Unlock()
		if oneShot == nil {
			return nil, fmt.Errorf("retired agent transport has no one-shot fallback")
		}
		return oneShot.RoundTrip(req)
	}
	entry.inFlight++
	entry.idleHint = false
	entry.lastUsed = time.Now()
	transport := entry.transport
	entry.pool.mu.Unlock()

	resp, err := transport.RoundTrip(req)
	if err != nil || resp == nil {
		entry.roundTripDone(false)
		return resp, err
	}
	if resp.Body == nil {
		entry.roundTripDone(resp.StatusCode != http.StatusSwitchingProtocols)
		return resp, nil
	}
	tracked := &agentTransportTrackedBody{
		body:     resp.Body,
		done:     entry.roundTripDone,
		canReuse: resp.StatusCode != http.StatusSwitchingProtocols,
	}
	if writer, ok := resp.Body.(io.Writer); ok {
		resp.Body = &agentTransportTrackedReadWriteBody{agentTransportTrackedBody: tracked, writer: writer}
	} else {
		resp.Body = tracked
	}
	return resp, nil
}

func (entry *pooledAgentTransport) roundTripDone(reusable bool) {
	if entry == nil || entry.pool == nil {
		return
	}
	var closeAgain *http.Transport
	entry.pool.mu.Lock()
	if entry.inFlight > 0 {
		entry.inFlight--
	}
	if reusable && !entry.retired {
		entry.idleHint = true
	}
	if entry.retired && entry.inFlight == 0 {
		delete(entry.pool.draining, entry)
		closeAgain = entry.transport
	}
	entry.pool.mu.Unlock()
	if closeAgain != nil {
		// A connection that was active during retirement may have become idle
		// after the first sweep. The second close makes retirement complete.
		closeAgain.CloseIdleConnections()
	}
}

type agentTransportTrackedBody struct {
	body     io.ReadCloser
	done     func(bool)
	canReuse bool
	doneOnce sync.Once
}

func (b *agentTransportTrackedBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		b.finish(err == io.EOF && b.canReuse)
	}
	return n, err
}

func (b *agentTransportTrackedBody) Close() error {
	err := b.body.Close()
	b.finish(false)
	return err
}

type agentTransportTrackedReadWriteBody struct {
	*agentTransportTrackedBody
	writer io.Writer
}

func (b *agentTransportTrackedReadWriteBody) Write(p []byte) (int, error) {
	n, err := b.writer.Write(p)
	if err != nil {
		b.finish(false)
	}
	return n, err
}

func (b *agentTransportTrackedBody) finish(reusable bool) {
	b.doneOnce.Do(func() {
		if b.done != nil {
			b.done(reusable)
		}
	})
}

func newAgentPooledHTTPTransport(app *App, agent *AgentConn, tlsConfig *tls.Config, timeout time.Duration, queueKey string) *http.Transport {
	return newAgentHTTPTransport(app, agent, tlsConfig, timeout, queueKey, agentStreamCapacityPublicPooled, false)
}

func newAgentHTTPTransport(
	app *App,
	agent *AgentConn,
	tlsConfig *tls.Config,
	timeout time.Duration,
	queueKey string,
	class agentStreamCapacityClass,
	disableKeepAlives bool,
) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.DisableKeepAlives = disableKeepAlives
	maxIdleConnections := 1
	if !disableKeepAlives && class == agentStreamCapacityPublicPooled && app != nil && app.agentStreamCapacity != nil {
		maxIdleConnections = app.agentStreamCapacity.snapshot().Pooled.Capacity
		if maxIdleConnections < 1 {
			maxIdleConnections = 1
		}
		if maxIdleConnections > maxAgentTransportPoolEntries {
			maxIdleConnections = maxAgentTransportPoolEntries
		}
	}
	// The manager's pooled lifetime budget is the authoritative global idle
	// bound. Let a hot shard retain concurrent connections up to that bound so
	// steady parallel traffic does not turn into a dial/TLS/Yamux storm.
	transport.MaxIdleConns = maxIdleConnections
	transport.MaxIdleConnsPerHost = maxIdleConnections
	transport.MaxConnsPerHost = 0
	transport.IdleConnTimeout = 30 * time.Second
	transport.ResponseHeaderTimeout = timeout
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig.Clone()
	}
	transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		requestID := agentDialRequestID(ctx)
		if requestID == "" {
			if id, err := uuid.NewV7(); err == nil {
				requestID = id.String()
			}
		}
		return app.dialViaAgentWithCapacity(ctx, agent, network, address, requestID, class, queueKey)
	}
	return transport
}

func agentTransportCapacityQueueKey(key agentTransportKey) string {
	switch key.Kind {
	case agentTransportKindRouteTarget:
		return fmt.Sprintf("route-target:%d", key.RouteTargetID)
	case agentTransportKindEnvironment:
		return fmt.Sprintf("environment:%d", key.EnvironmentID)
	default:
		return "agent-transport"
	}
}

func (p *agentTransportPool) closeAgent(agentID int64) {
	p.closeWhere(func(key agentTransportKey) bool {
		return key.AgentID == agentID
	})
}

func (p *agentTransportPool) closeAgentConnection(agent *AgentConn) {
	if agent == nil {
		return
	}
	p.closeEntriesWhere(func(_ agentTransportKey, entry *pooledAgentTransport) bool {
		return entry != nil && entry.agent == agent
	})
}

func (p *agentTransportPool) closeRouteTarget(targetID int64) {
	p.closeWhere(func(key agentTransportKey) bool {
		return key.Kind == agentTransportKindRouteTarget && key.RouteTargetID == targetID
	})
}

func (p *agentTransportPool) closeEnvironment(environmentID int64) {
	p.closeWhere(func(key agentTransportKey) bool {
		return key.Kind == agentTransportKindEnvironment && key.EnvironmentID == environmentID
	})
}

func (p *agentTransportPool) closeAll() {
	p.closeWhere(func(agentTransportKey) bool { return true })
}

func (p *agentTransportPool) len() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.retainedEntriesLocked()
}

func (p *agentTransportPool) closeWhere(match func(agentTransportKey) bool) {
	p.closeEntriesWhere(func(key agentTransportKey, _ *pooledAgentTransport) bool {
		return match(key)
	})
}

func (p *agentTransportPool) closeEntriesWhere(match func(agentTransportKey, *pooledAgentTransport) bool) {
	if p == nil {
		return
	}
	var transports []*http.Transport
	p.mu.Lock()
	for key, entry := range p.entries {
		if !match(key, entry) {
			continue
		}
		if transport := p.retireLocked(key, entry); transport != nil {
			transports = append(transports, transport)
		}
	}
	p.mu.Unlock()
	closeAgentIdleTransports(transports)
}
