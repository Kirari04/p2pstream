package server

import (
	"context"
	"errors"
	"net"
	"net/http"

	"p2pstream/internal/tunnel"
)

var errDirectUpstreamResourcePressure = errors.New("direct upstream resource pressure")

func (p *directTransportPool) accountConnections(transport *http.Transport) {
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		release, ok, _ := p.app.agentStreamCapacity.tryReserveAdaptiveExternal(tunnel.AdaptivePerStreamOverheadBytes, 2)
		if !ok {
			// Idle sockets are reclaimable. Free them before reporting real
			// resource exhaustion; no per-target connection ceiling is imposed.
			p.closeIdleConnections()
			release, ok, _ = p.app.agentStreamCapacity.tryReserveAdaptiveExternal(tunnel.AdaptivePerStreamOverheadBytes, 2)
		}
		if !ok {
			return nil, errDirectUpstreamResourcePressure
		}
		conn, err := dial(ctx, network, address)
		if err != nil {
			release()
			return nil, err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			err = tcp.SetReadBuffer(int(tunnel.DefaultUpstreamSocketBufferBytes))
			if err == nil {
				err = tcp.SetWriteBuffer(int(tunnel.DefaultUpstreamSocketBufferBytes))
			}
			if err != nil {
				_ = conn.Close()
				release()
				return nil, err
			}
		}
		bounded := &resourceBoundedPublicConn{Conn: conn, release: release}
		if tcp, ok := conn.(*net.TCPConn); ok {
			return &resourceBoundedPublicTCPConn{resourceBoundedPublicConn: bounded, tcp: tcp}, nil
		}
		return bounded, nil
	}
}

func (p *directTransportPool) closeIdleConnections() {
	p.mu.Lock()
	transports := make([]*http.Transport, 0, len(p.entries))
	for _, entry := range p.entries {
		transports = append(transports, entry.transport)
	}
	p.mu.Unlock()
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
}
