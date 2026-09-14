package tunnel

import (
	"net"
	"sync"

	"github.com/hashicorp/yamux"
)

// GrowingConn retains all granted credit until Release, which the caller must
// invoke only after peer FIN or forced Yamux cleanup. Denied growth leaves the
// stream usable at its existing window instead of failing the request.
type GrowingConn struct {
	net.Conn
	stream    *yamux.Stream
	maximum   uint32
	reserve   func(int64) (func(), bool)
	mu        sync.Mutex
	readBytes uint64
	closing   bool
	releases  []func()
}

func NewGrowingConn(conn net.Conn, maximum int64, reserve func(int64) (func(), bool)) *GrowingConn {
	window, _ := NormalizeMaxStreamWindowSizeBytes(maximum)
	stream, _ := conn.(*yamux.Stream)
	return &GrowingConn{Conn: conn, stream: stream, maximum: window, reserve: reserve}
}

func (c *GrowingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && err == nil && c.stream != nil && c.reserve != nil {
		if growErr := c.grow(uint64(n)); growErr != nil {
			return n, growErr
		}
	}
	return n, err
}

func (c *GrowingConn) grow(n uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.stream.MaxReceiveWindow()
	if c.closing || current >= c.maximum {
		return nil
	}
	c.readBytes += n
	if c.readBytes < uint64(current) {
		return nil
	}
	c.readBytes = 0
	next := uint32(min(uint64(current)*2, uint64(c.maximum)))
	release, ok := c.reserve(int64(next - current))
	if !ok {
		return nil
	}
	c.releases = append(c.releases, release)
	// Keep the reservation even if the write fails: credit might have reached
	// the peer before the error became observable locally.
	return c.stream.GrowReceiveWindow(next)
}

func (c *GrowingConn) Close() error {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *GrowingConn) Release() {
	c.mu.Lock()
	c.closing = true
	releases := c.releases
	c.releases = nil
	c.mu.Unlock()
	for _, release := range releases {
		if release != nil {
			release()
		}
	}
}
