package tcpsocket

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func autoMemory(conn *net.TCPConn) (int64, error) {
	// Read in the process's network namespace. These are actual kernel buffer
	// sizes, not setsockopt requests: do not double them. Reserve the full
	// allowance so simultaneous growth between memory samples is covered.
	receive, err := readTCPMemoryMaximum("/proc/sys/net/ipv4/tcp_rmem")
	if err != nil {
		return 0, err
	}
	send, err := readTCPMemoryMaximum("/proc/sys/net/ipv4/tcp_wmem")
	if err != nil {
		return 0, err
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		for _, option := range []struct {
			name    int
			maximum *int64
		}{{unix.SO_RCVBUF, &receive}, {unix.SO_SNDBUF, &send}} {
			current, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, option.name)
			if err != nil {
				socketErr = err
				return
			}
			*option.maximum = max(*option.maximum, int64(current))
		}
	})
	if err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	return receive + send, nil
}

func readTCPMemoryMaximum(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read TCP autotuning allowance: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return 0, fmt.Errorf("invalid TCP autotuning allowance in %s", path)
	}
	var maximum int64
	for _, field := range fields {
		value, err := strconv.ParseInt(field, 10, 32)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("invalid TCP autotuning allowance in %s", path)
		}
		maximum = max(maximum, value)
	}
	return maximum, nil
}
