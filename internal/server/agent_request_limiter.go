package server

import (
	"net"
	"sync"

	"p2pstream/internal/tunnel"
)

func newAgentRequestLimiter(limit int64) *tunnel.StreamLimiter {
	limiter, err := tunnel.NewStreamLimiter(limit)
	if err != nil {
		limiter, _ = tunnel.NewStreamLimiter(tunnel.DefaultMaxConcurrentAgentRequests)
	}
	return limiter
}

// agentTunnelStreamConn keeps a server stream slot for the complete lifetime
// of a Yamux stream, including while an HTTP transport holds it idle.
type agentTunnelStreamConn struct {
	net.Conn
	release   func()
	closeOnce sync.Once
	closeErr  error
}

func newAgentTunnelStreamConn(conn net.Conn, release func()) net.Conn {
	return &agentTunnelStreamConn{Conn: conn, release: release}
}

func (c *agentTunnelStreamConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		if c.release != nil {
			c.release()
		}
	})
	return c.closeErr
}
