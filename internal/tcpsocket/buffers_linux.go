package tcpsocket

import (
	"net"

	"golang.org/x/sys/unix"
)

func autoMemory(conn *net.TCPConn) (int64, error) {
	// Linux owns autotuning growth. The adaptive controller observes committed
	// memory at the process, cgroup, and host boundaries, so reserving both TCP
	// maxima for every socket's entire lifetime would reject ordinary idle-pool
	// traffic long before measured pressure. Do not touch either socket option.
	return BaseMemoryBytes, nil
}

func configureNotSentLowat(conn *net.TCPConn, requested int) {
	if conn == nil || requested <= 0 {
		return
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, requested)
	}); err != nil || socketErr != nil {
		// Older kernels and restricted platforms may not expose this optional
		// hint. Preserve the usable connection and its default queue behavior.
		return
	}
}
