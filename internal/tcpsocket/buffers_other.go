//go:build !linux

package tcpsocket

import "net"

func autoMemory(conn *net.TCPConn) (int64, error) {
	// Retain bounded queues on platforms where kernel autotuning allowances
	// are not yet available to the resource controller.
	return fixedMemory(conn, BaseMemoryBytes/4)
}
