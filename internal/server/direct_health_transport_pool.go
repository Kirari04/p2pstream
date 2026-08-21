package server

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type directHealthTransportKey struct {
	RouteTargetID               int64
	TargetOrigin                string
	TLSSkipVerify               bool
	ResponseHeaderTimeoutMillis int64
}

type pooledDirectHealthTransport struct {
	key       directHealthTransportKey
	transport *http.Transport
	createdAt time.Time
}

type directHealthTransportPool struct {
	mu      sync.Mutex
	entries map[directHealthTransportKey]*pooledDirectHealthTransport
}

func newDirectHealthTransportPool() *directHealthTransportPool {
	return &directHealthTransportPool{entries: make(map[directHealthTransportKey]*pooledDirectHealthTransport)}
}

func (p *directHealthTransportPool) transport(target publicRouteTargetHealthConfig, timeout time.Duration) http.RoundTripper {
	if p == nil {
		return newDirectProxyHTTPTransport(target.TLSSkipVerify, timeout)
	}
	key := directHealthTransportKeyForTarget(target, timeout)
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.entries[key]; existing != nil && existing.transport != nil {
		return existing.transport
	}
	transport := newDirectProxyHTTPTransport(target.TLSSkipVerify, timeout)
	p.entries[key] = &pooledDirectHealthTransport{
		key:       key,
		transport: transport,
		createdAt: time.Now(),
	}
	return transport
}

func (p *directHealthTransportPool) closeTarget(targetID int64) {
	p.closeWhere(func(key directHealthTransportKey) bool {
		return key.RouteTargetID == targetID
	})
}

func (p *directHealthTransportPool) closeStaleTargetTransports(target publicRouteTargetHealthConfig, timeout time.Duration) {
	want := directHealthTransportKeyForTarget(target, timeout)
	p.closeWhere(func(key directHealthTransportKey) bool {
		return key.RouteTargetID == target.ID && key != want
	})
}

func (p *directHealthTransportPool) closeAll() {
	p.closeWhere(func(directHealthTransportKey) bool { return true })
}

func (p *directHealthTransportPool) len() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

func (p *directHealthTransportPool) closeWhere(match func(directHealthTransportKey) bool) {
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

func directHealthTransportKeyForTarget(target publicRouteTargetHealthConfig, timeout time.Duration) directHealthTransportKey {
	timeout = normalizeUpstreamResponseHeaderTimeout(timeout)
	return directHealthTransportKey{
		RouteTargetID:               target.ID,
		TargetOrigin:                directHealthTransportOrigin(target),
		TLSSkipVerify:               target.TLSSkipVerify,
		ResponseHeaderTimeoutMillis: int64(timeout / time.Millisecond),
	}
}

func directHealthTransportOrigin(target publicRouteTargetHealthConfig) string {
	origin := target.ParsedOrigin
	if origin == nil && target.TargetOrigin != "" {
		parsed, err := url.Parse(target.TargetOrigin)
		if err == nil {
			origin = parsed
		}
	}
	if origin == nil {
		return target.TargetOrigin
	}
	return (&url.URL{Scheme: origin.Scheme, Host: origin.Host}).String()
}

func newDirectProxyHTTPTransport(tlsSkipVerify bool, responseHeaderTimeout time.Duration) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = normalizeUpstreamResponseHeaderTimeout(responseHeaderTimeout)
	if tlsSkipVerify {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	return transport
}
