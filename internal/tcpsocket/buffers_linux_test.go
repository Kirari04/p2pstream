package tcpsocket

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := ln.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func socketSizes(t *testing.T, conn *net.TCPConn) [2]int {
	t.Helper()
	var sizes [2]int
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		for i, option := range []int{unix.SO_RCVBUF, unix.SO_SNDBUF} {
			sizes[i], socketErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, option)
			if socketErr != nil {
				return
			}
		}
	})
	if err != nil || socketErr != nil {
		t.Fatalf("socket sizes: %v, %v", err, socketErr)
	}
	return sizes
}

func TestAutoPreservesTCPBuffersAndReservesKernelAllowance(t *testing.T) {
	client, server := tcpPair(t)
	for _, conn := range []*net.TCPConn{client, server} {
		before := socketSizes(t, conn)
		memory, err := Configure(conn, 0)
		if err != nil {
			t.Fatal(err)
		}
		if after := socketSizes(t, conn); before != after {
			t.Fatalf("autotuning overwritten: %v -> %v", before, after)
		}
		receive, err := readTCPMemoryMaximum("/proc/sys/net/ipv4/tcp_rmem")
		if err != nil {
			t.Fatal(err)
		}
		send, err := readTCPMemoryMaximum("/proc/sys/net/ipv4/tcp_wmem")
		if err != nil {
			t.Fatal(err)
		}
		if want := max(receive, int64(before[0])) + max(send, int64(before[1])); memory != want {
			t.Fatalf("charge %d, want %d", memory, want)
		}
	}
}

func TestExplicitTCPBufferOverrideRemainsBounded(t *testing.T) {
	conn, _ := tcpPair(t)
	memory, err := Configure(conn, 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	sizes := socketSizes(t, conn)
	if memory != 512<<10 || int64(sizes[0])+int64(sizes[1]) > memory {
		t.Fatalf("buffers %v exceed reservation %d", sizes, memory)
	}
}

func TestAccountedTCPCloseReleasesOnlyOnceAndKeepsHalfCloseCredit(t *testing.T) {
	conn, _ := tcpPair(t)
	var released atomic.Int64
	tracked := &AccountedConn{TCPConn: conn, Release: func() { released.Add(1) }}
	if err := tracked.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if released.Load() != 0 {
		t.Fatal("half-close released live socket credit")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _ = tracked.Close() })
	}
	wg.Wait()
	if released.Load() != 1 {
		t.Fatal("physical close did not release exactly once")
	}
}

func TestTCPMemoryAllowanceRejectsMissingOrInvalidSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tcp_rmem")
	if _, err := readTCPMemoryMaximum(path); err == nil {
		t.Fatal("missing signal accepted")
	}
	for _, text := range []string{"", "1 2", "1 2 3 4", "1 -2 3", "1 2 2147483648", "1 two 3", "0 2 3"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readTCPMemoryMaximum(path); err == nil {
			t.Fatalf("invalid signal %q accepted", text)
		}
	}
	if err := os.WriteFile(path, []byte("4096\t131072\t65536\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if size, err := readTCPMemoryMaximum(path); err != nil || size != 131072 {
		t.Fatalf("default larger than max under-accounted: %d %v", size, err)
	}
}
