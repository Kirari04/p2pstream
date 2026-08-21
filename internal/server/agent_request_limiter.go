package server

import (
	"net"
	"sync"
	"time"

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
	readMu    sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func newAgentTunnelStreamConn(conn net.Conn, release func()) net.Conn {
	return &agentTunnelStreamConn{Conn: conn, release: release}
}

func (c *agentTunnelStreamConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	return c.Conn.Read(p)
}

func (c *agentTunnelStreamConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		if c.release != nil {
			go c.releaseAfterStreamClosed()
		}
	})
	return c.closeErr
}

func (c *agentTunnelStreamConn) releaseAfterStreamClosed() {
	// yamux.Stream.Close sends a local FIN. The stream remains in the session
	// (and can retain its receive window) until the peer replies with FIN or the
	// configured StreamCloseTimeout forces cleanup. Drain the read side until
	// either event is observable before returning the capacity slot.
	// Serialize with an existing HTTP transport reader. Yamux wakes one blocked
	// reader per state notification, so competing reads could otherwise leave
	// either the transport reader or this cleanup goroutine blocked forever.
	c.readMu.Lock()
	defer c.readMu.Unlock()
	_ = c.Conn.SetReadDeadline(time.Time{})
	var buf [4 * 1024]byte
	for {
		if _, err := c.Conn.Read(buf[:]); err != nil {
			c.release()
			return
		}
	}
}
