// Package tcpsocket keeps TCP flow control independent of application admission.
package tcpsocket

import (
	"net"
	"sync"
)

// BaseMemoryBytes is the socket allowance already included in stream overhead.
const BaseMemoryBytes = int64(512 * 1024)

// DefaultNotSentLowatBytes bounds unsent payload copied into a Linux TCP write
// queue beyond what the network has accepted. It matches the relay copy buffer
// while leaving send and receive buffer autotuning untouched. This is not a
// bound on total kernel memory owned by the socket.
const DefaultNotSentLowatBytes = 32 * 1024

// Configure returns the socket allowance to reserve before using conn. Zero
// leaves Linux TCP autotuning enabled and returns a baseline overhead allowance;
// real auto-grown queue memory remains governed by measured memory pressure. A
// positive value is an explicit operator override per direction and receives a
// full bounded charge. Do not set SO_RCVBUF/SO_SNDBUF in auto mode: even a
// seemingly generous value disables autotuning and imposes a WAN cap.
func Configure(conn *net.TCPConn, requested int64) (int64, error) {
	var (
		memoryBytes int64
		err         error
	)
	if requested == 0 {
		memoryBytes, err = autoMemory(conn)
	} else {
		memoryBytes, err = fixedMemory(conn, requested)
	}
	if err != nil {
		return 0, err
	}
	// TCP_NOTSENT_LOWAT is an optional performance hint. An unavailable
	// platform or kernel must not reject an otherwise usable connection.
	configureNotSentLowat(conn, DefaultNotSentLowatBytes)
	return memoryBytes, nil
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
	Release  func()
	once     sync.Once
	closeErr error
}

// Own physical Close and refund together: a repeated raw TCP Close can return
// before the first FD destruction finishes. All callers receive the first result.
func (c *AccountedConn) Close() error {
	c.once.Do(func() {
		c.closeErr = c.TCPConn.Close()
		if c.Release != nil {
			c.Release()
		}
	})
	return c.closeErr
}
