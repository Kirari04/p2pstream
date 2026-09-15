package tunnel

import (
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// GrowingConn retains all granted credit until Release, which the caller must
// invoke only after peer FIN or forced Yamux cleanup. Denied growth leaves the
// stream usable at its existing window instead of failing the request.
type GrowingConn struct {
	net.Conn
	stream   *yamux.Stream
	maximum  uint32
	reserve  func(int64) (func(), bool)
	mu       sync.Mutex
	growth   receiveWindowGrowth
	closing  bool
	releases []func()
}

func NewGrowingConn(conn net.Conn, maximum int64, reserve func(int64) (func(), bool)) *GrowingConn {
	window, err := NormalizeMaxStreamWindowSizeBytes(maximum)
	stream, _ := conn.(*yamux.Stream)
	if err != nil && stream != nil {
		// Runtime callers validate configuration before opening a session. If
		// another caller supplies an invalid ceiling, grant no extra credit.
		window = stream.MaxReceiveWindow()
	}
	c := &GrowingConn{Conn: conn, stream: stream, maximum: window, reserve: reserve}
	c.growth.start = time.Now()
	if stream != nil && reserve != nil && stream.MaxReceiveWindow() < window {
		stream.Session().RequestRTT()
	}
	return c
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
	return c.growMeasured(n, time.Now(), c.stream.Session().RTT())
}

func (c *GrowingConn) growMeasured(n uint64, now time.Time, rtt time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.stream.MaxReceiveWindow()
	if c.closing || current >= c.maximum {
		return nil
	}
	next, sampled := c.growth.observe(now, n, current, c.maximum, rtt)
	if sampled {
		c.stream.Session().RequestRTT()
	}
	if next <= current {
		return nil
	}
	// The initial stream charge includes chunk slack. Charge every additional
	// chunk and its bookkeeping before publishing the new receive credit.
	extra := yamux.ReceiveWindowMemory(next) - yamux.ReceiveWindowMemory(current)
	release, ok := c.reserve(int64(extra))
	if !ok {
		c.growth.deniedUntil = now.Add(time.Second)
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
