package server

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type directTransportKey struct {
	RouteTargetID               int64
	TargetOrigin                string
	TLSSkipVerify               bool
	ResponseHeaderTimeoutMillis int64
}

type pooledDirectTransport struct {
	key       directTransportKey
	transport *http.Transport
	createdAt time.Time
}

type directTransportPool struct {
	mu      sync.Mutex
	entries map[directTransportKey]*pooledDirectTransport
}

func newDirectTransportPool() *directTransportPool {
	return &directTransportPool{entries: make(map[directTransportKey]*pooledDirectTransport)}
}

func (p *directTransportPool) publicRouteTargetTransport(target publicRouteTargetConfig) http.RoundTripper {
	timeout := normalizeUpstreamResponseHeaderTimeout(target.UpstreamResponseHeaderTimeout)
	key := directTransportKey{
		RouteTargetID:               target.ID,
		TargetOrigin:                routeTargetTransportOrigin(target),
		TLSSkipVerify:               target.TLSSkipVerify,
		ResponseHeaderTimeoutMillis: int64(timeout / time.Millisecond),
	}
	return p.getOrCreate(key, target.TLSSkipVerify, timeout)
}

func (p *directTransportPool) getOrCreate(key directTransportKey, tlsSkipVerify bool, timeout time.Duration) http.RoundTripper {
	if p == nil {
		return newDirectPooledHTTPTransport(tlsSkipVerify, timeout)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.entries[key]; existing != nil && existing.transport != nil {
		return existing.transport
	}
	transport := newDirectPooledHTTPTransport(tlsSkipVerify, timeout)
	p.entries[key] = &pooledDirectTransport{
		key:       key,
		transport: transport,
		createdAt: time.Now(),
	}
	return transport
}

func newDirectPooledHTTPTransport(tlsSkipVerify bool, timeout time.Duration) *http.Transport {
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

func (p *directTransportPool) closeRouteTarget(targetID int64) {
	p.closeWhere(func(key directTransportKey) bool {
		return key.RouteTargetID == targetID
	})
}

func (p *directTransportPool) closeAll() {
	p.closeWhere(func(directTransportKey) bool { return true })
}

func (p *directTransportPool) len() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

func (p *directTransportPool) closeWhere(match func(directTransportKey) bool) {
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

func routeTargetTransportOrigin(target publicRouteTargetConfig) string {
	if target.ParsedURL != nil && target.ParsedURL.Scheme != "" && target.ParsedURL.Host != "" {
		return target.ParsedURL.Scheme + "://" + target.ParsedURL.Host
	}
	parsed, err := url.Parse(target.URL)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.Scheme + "://" + parsed.Host
	}
	return target.URL
}
