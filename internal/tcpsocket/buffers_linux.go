package tcpsocket

import (
	"net"
)

func autoMemory(conn *net.TCPConn) (int64, error) {
	// Linux owns autotuning growth. The adaptive controller observes committed
	// memory at the process, cgroup, and host boundaries, so reserving both TCP
	// maxima for every socket's entire lifetime would reject ordinary idle-pool
	// traffic long before measured pressure. Do not touch either socket option.
	return BaseMemoryBytes, nil
}
