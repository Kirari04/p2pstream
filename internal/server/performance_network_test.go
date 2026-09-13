package server

import (
	"net"
	"sync"
	"time"
)

// delayedTestListener simulates propagation delay in both directions over real
// TCP. Chunks are pipelined, not independently slept in Read/Write (which would
// impose an accidental bandwidth limit). The queues are bounded to 8 MiB per
// direction. This models latency, not packet loss or kernel TCP congestion.
type delayedTestListener struct {
	net.Listener
	delay time.Duration
}

func newDelayedTestListener(listener net.Listener, delay time.Duration) net.Listener {
	return &delayedTestListener{Listener: listener, delay: delay}
}

func (l *delayedTestListener) Accept() (net.Conn, error) {
	raw, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	front, bridge := net.Pipe()
	c := &delayedTestConn{Conn: front, raw: raw, bridge: bridge, done: make(chan struct{})}
	c.forward(raw, bridge, l.delay)
	c.forward(bridge, raw, l.delay)
	return c, nil
}

type delayedTestConn struct {
	net.Conn
	raw, bridge net.Conn
	once        sync.Once
	done        chan struct{}
}

func (c *delayedTestConn) LocalAddr() net.Addr  { return c.raw.LocalAddr() }
func (c *delayedTestConn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }
func (c *delayedTestConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		_ = c.Conn.Close()
		_ = c.bridge.Close()
		_ = c.raw.Close()
	})
	return nil
}

func (c *delayedTestConn) forward(src, dst net.Conn, delay time.Duration) {
	type chunk struct {
		data  []byte
		ready time.Time
		err   error
	}
	queue := make(chan chunk, 256)
	go func() {
		for {
			data := make([]byte, 32<<10)
			n, err := src.Read(data)
			select {
			case queue <- chunk{data[:n], time.Now().Add(delay), err}:
			case <-c.done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer c.Close()
		for {
			var item chunk
			select {
			case item = <-queue:
			case <-c.done:
				return
			}
			if wait := time.Until(item.ready); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-timer.C:
				case <-c.done:
					timer.Stop()
					return
				}
			}
			if len(item.data) > 0 {
				if _, err := dst.Write(item.data); err != nil {
					return
				}
			}
			if item.err != nil {
				return
			}
		}
	}()
}
