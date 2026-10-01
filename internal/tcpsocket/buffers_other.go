//go:build !linux

package tcpsocket

import "net"

func autoMemory(conn *net.TCPConn) (int64, error) {
	// Retain bounded queues on platforms where automatic queue growth is not
	// yet covered by the Linux measured-pressure contract.
	return fixedMemory(conn, BaseMemoryBytes/4)
}
