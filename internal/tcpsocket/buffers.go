// Package tcpsocket keeps TCP flow control independent of application admission.
package tcpsocket

import (
	"net"
	"sync"
)

// BaseMemoryBytes is the socket allowance already included in stream overhead.
const BaseMemoryBytes = int64(512 * 1024)

// Configure returns the possible queue memory to reserve before using conn.
// Zero leaves Linux TCP autotuning enabled; a positive value is an explicit
// operator override per direction. Do not set SO_RCVBUF/SO_SNDBUF in auto mode:
// even a seemingly generous value disables autotuning and imposes a WAN cap.
func Configure(conn *net.TCPConn, requested int64) (int64, error) {
	if requested == 0 {
		return autoMemory(conn)
	}
	return fixedMemory(conn, requested)
}

func fixedMemory(conn *net.TCPConn, requested int64) (int64, error) {
	if err := conn.SetReadBuffer(int(requested)); err != nil {
		return 0, err
	}
	if err := conn.SetWriteBuffer(int(requested)); err != nil {
		return 0, err
	}
	// Linux can double each requested buffer; clamping can only reduce it.
	return 4 * requested, nil
}

// AccountedConn retains socket credit until the physical connection closes,
// including while an HTTP transport keeps it idle. Embedding TCPConn preserves
// half-close, deadlines and syscall access used by tunnel relays.
type AccountedConn struct {
	*net.TCPConn
	Release func()
	once    sync.Once
}

func (c *AccountedConn) Close() error {
	err := c.TCPConn.Close()
	c.once.Do(func() {
		if c.Release != nil {
			c.Release()
		}
	})
	return err
}
