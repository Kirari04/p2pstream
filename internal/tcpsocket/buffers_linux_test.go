package tcpsocket

import (
	"net"
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

func TestAutoPreservesTCPBuffersAndChargesBaselineAllowance(t *testing.T) {
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
		if memory != BaseMemoryBytes {
			t.Fatalf("charge %d, want baseline %d", memory, BaseMemoryBytes)
		}
	}
}

func TestAutoPreservesLargerExistingTCPBuffers(t *testing.T) {
	conn, _ := tcpPair(t)
	if err := conn.SetReadBuffer(1 << 20); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetWriteBuffer(1 << 20); err != nil {
		t.Fatal(err)
	}
	before := socketSizes(t, conn)
	memory, err := Configure(conn, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after := socketSizes(t, conn); before != after {
		t.Fatalf("autotuning buffers overwritten: %v -> %v", before, after)
	}
	if memory != BaseMemoryBytes {
		t.Fatalf("charge %d, want baseline %d", memory, BaseMemoryBytes)
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
