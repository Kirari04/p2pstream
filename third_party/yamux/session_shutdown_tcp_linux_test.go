package yamux

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

// shutdownReadGate fixes scheduling at a protocol boundary. Close releases
// the blocked worker only after closing the underlying socket.
type halfcloseGate struct {
	io.ReadWriteCloser
	remaining              int
	entered, release       chan struct{}
	enterOnce, releaseOnce sync.Once
}

func (c *halfcloseGate) Read(p []byte) (int, error) {
	if c.remaining == 0 {
		c.enterOnce.Do(func() { close(c.entered) })
		<-c.release
		c.remaining = -1
	}
	if c.remaining > 0 && len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, e := c.ReadWriteCloser.Read(p)
	if c.remaining > 0 {
		c.remaining -= n
	}
	return n, e
}
func (c *halfcloseGate) unblock()     { c.releaseOnce.Do(func() { close(c.release) }) }
func (c *halfcloseGate) Close() error { e := c.ReadWriteCloser.Close(); c.unblock(); return e }

// Linux closes TCP abortively when unread control data remains in the socket.
// A completed Write does not guarantee queued payload reached the peer. Preserve
// this separate premature-close case instead of weakening the graceful oracle.
func TestHalfCloseUnreadControlResetsQueuedPayload(t *testing.T) {
	l, r := testConnTCP(t)
	left, right := l.(*net.TCPConn), r.(*net.TCPConn)
	if e := right.SetReadBuffer(4096); e != nil {
		t.Fatal(e)
	}
	cg := &halfcloseGate{ReadWriteCloser: left, entered: make(chan struct{}), release: make(chan struct{})}
	sg := &halfcloseGate{ReadWriteCloser: right, remaining: headerSize, entered: make(chan struct{}), release: make(chan struct{})}
	trace := &halfcloseTraceConn{ReadWriteCloser: sg}
	client, server := testClientServerConfig(t, cg, trace, testConfNoKeepAlive(), testConfNoKeepAlive())
	sent, e := client.OpenStream()
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-sg.entered:
	case <-time.After(time.Second):
		t.Fatal("receiver did not reach DATA gate")
	}
	data := bytes.Repeat([]byte{0x4d}, int(initialStreamWindow))
	n, e := sent.Write(data)
	if n != len(data) || e != nil {
		t.Fatal(n, e)
	}
	received, e := server.AcceptStream()
	if e != nil {
		t.Fatal(e)
	}
	if e = sent.Close(); e != nil {
		t.Fatal(e)
	}
	// ACK must actually be unread in the sender socket, not merely enqueued at peer.
	left.SetReadDeadline(time.Now().Add(time.Second))
	raw, e := left.SyscallConn()
	if e != nil {
		t.Fatal(e)
	}
	var peekErr error
	var peekN int
	e = raw.Read(func(fd uintptr) bool {
		var b [1]byte
		peekN, _, peekErr = syscall.Recvfrom(int(fd), b[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		return peekErr != syscall.EAGAIN && peekErr != syscall.EWOULDBLOCK
	})
	if e != nil || peekErr != nil || peekN != 1 {
		t.Fatal("unread ACK", e, peekErr, peekN)
	}
	before, _ := received.ReceiveBufferStats()
	if before != 0 {
		t.Fatal("gate accepted payload", before)
	}
	client.Close()
	sg.unblock()
	select {
	case <-server.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("no natural reset shutdown")
	}
	server.Close()
	buffered, _ := received.ReceiveBufferStats()
	got := make([]byte, len(data))
	drained, re := io.ReadFull(received, got)
	wirePayload := len(trace.received) - 2*headerSize
	if wirePayload < 0 || uint64(wirePayload) != buffered || drained != wirePayload || drained >= len(data) || re != io.ErrUnexpectedEOF || !bytes.Equal(got[:drained], data[:drained]) {
		t.Fatalf("sent=%d wire=%d buffer=%d read=%d err=%v session=%v", n, wirePayload, buffered, drained, re, server.shutdownErr)
	}
	if !errors.Is(server.shutdownErr, syscall.ECONNRESET) {
		t.Fatalf("expected peer transport reset: %v", server.shutdownErr)
	}
	received.Reset()
	if b, a := received.ReceiveBufferStats(); b != 0 || a != 0 {
		t.Fatalf("disposed prefix retained buffer=%d allocation=%d", b, a)
	}
	t.Logf(" sender_Write=%d receiver_before_close=%d unread_control=%d receiver_DATA=%d retained=%d drain=%d err=%v session_error=%v", n, before, peekN, wirePayload, buffered, drained, re, server.shutdownErr)
}

// Record plaintext delivered to the Yamux receiver, independently of its
// application buffer. Only the receive worker writes; inspect after Close joins.
type halfcloseTraceConn struct {
	io.ReadWriteCloser
	received []byte
}

func (c *halfcloseTraceConn) Read(p []byte) (int, error) {
	n, e := c.ReadWriteCloser.Read(p)
	c.received = append(c.received, p[:n]...)
	return n, e
}
