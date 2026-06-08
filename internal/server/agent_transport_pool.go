package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
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
	key       agentTransportKey
	agent     *AgentConn
	transport *http.Transport
	createdAt time.Time
}

type agentTransportPool struct {
	mu      sync.Mutex
	entries map[agentTransportKey]*pooledAgentTransport
}

type agentDialRequestIDContextKey struct{}

func newAgentTransportPool() *agentTransportPool {
	return &agentTransportPool{entries: make(map[agentTransportKey]*pooledAgentTransport)}
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
		TargetOrigin:                target.URL,
		TLSSkipVerify:               target.TLSSkipVerify,
		ResponseHeaderTimeoutMillis: int64(timeout / time.Millisecond),
	}
	var tlsConfig *tls.Config
	if target.TLSSkipVerify {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return p.getOrCreate(app, agent, key, tlsConfig, timeout)
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
	return p.getOrCreate(app, agent, key, tlsConfig, timeout)
}

func (p *agentTransportPool) getOrCreate(app *App, agent *AgentConn, key agentTransportKey, tlsConfig *tls.Config, timeout time.Duration) http.RoundTripper {
	if p == nil {
		return newAgentPooledHTTPTransport(app, agent, tlsConfig, timeout)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.entries[key]; existing != nil {
		if existing.agent == agent && existing.transport != nil {
			return existing.transport
		}
		existing.transport.CloseIdleConnections()
		delete(p.entries, key)
	}
	transport := newAgentPooledHTTPTransport(app, agent, tlsConfig, timeout)
	p.entries[key] = &pooledAgentTransport{
		key:       key,
		agent:     agent,
		transport: transport,
		createdAt: time.Now(),
	}
	return transport
}

func newAgentPooledHTTPTransport(app *App, agent *AgentConn, tlsConfig *tls.Config, timeout time.Duration) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.DisableKeepAlives = false
	transport.MaxIdleConns = 1024
	transport.MaxIdleConnsPerHost = 32
	transport.MaxConnsPerHost = 0
	if transport.IdleConnTimeout < 90*time.Second {
		transport.IdleConnTimeout = 90 * time.Second
	}
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
		return app.dialViaAgent(ctx, agent, network, address, requestID)
	}
	return transport
}

func (p *agentTransportPool) closeAgent(agentID int64) {
	p.closeWhere(func(key agentTransportKey) bool {
		return key.AgentID == agentID
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
	return len(p.entries)
}

func (p *agentTransportPool) closeWhere(match func(agentTransportKey) bool) {
	if p == nil {
		return
	}
	var transports []*http.Transport
	p.mu.Lock()
	for key, entry := range p.entries {
		if !match(key) {
			continue
		}
		if entry.transport != nil {
			transports = append(transports, entry.transport)
		}
		delete(p.entries, key)
	}
	p.mu.Unlock()
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
}
