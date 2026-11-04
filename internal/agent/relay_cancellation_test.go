package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

type relayTestConn struct {
	name        string
	payload     []byte
	terminalErr error
	blockRead   bool
	events      chan<- string

	readMu       sync.Mutex
	readOffset   int
	blocked      chan struct{}
	blockedOnce  sync.Once
	release      chan struct{}
	releaseOnce  sync.Once
	readReturned chan struct{}
	returnedOnce sync.Once
}

func newRelayTestConn(name string, payload []byte, terminalErr error, blockRead bool, events chan<- string) *relayTestConn {
	return &relayTestConn{
		name:         name,
		payload:      payload,
		terminalErr:  terminalErr,
		blockRead:    blockRead,
		events:       events,
		blocked:      make(chan struct{}),
		release:      make(chan struct{}),
		readReturned: make(chan struct{}),
	}
}

func (c *relayTestConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	if c.readOffset < len(c.payload) {
		n := copy(p, c.payload[c.readOffset:])
		c.readOffset += n
		c.readMu.Unlock()
		return n, nil
	}
	if !c.blockRead {
		err := c.terminalErr
		c.readMu.Unlock()
		return 0, err
	}
	c.blockedOnce.Do(func() { close(c.blocked) })
	release := c.release
	err := c.terminalErr
	c.readMu.Unlock()

	<-release
	c.returnedOnce.Do(func() { close(c.readReturned) })
	return 0, err
}

func (c *relayTestConn) Write(p []byte) (int, error) {
	return len(p), nil
}

func (c *relayTestConn) Close() error {
	if c.events != nil {
		c.events <- c.name + ".close"
	}
	return nil
}

func (c *relayTestConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (c *relayTestConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }

func (c *relayTestConn) SetDeadline(time.Time) error {
	if c.events != nil {
		c.events <- c.name + ".deadline"
	}
	return nil
}

func (c *relayTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *relayTestConn) SetWriteDeadline(time.Time) error { return nil }

func (c *relayTestConn) releaseRead() {
	c.releaseOnce.Do(func() { close(c.release) })
}

type relayTestResult struct {
	received uint64
	sent     uint64
	err      error
}

type relayTestContext struct {
	context.Context
	done chan struct{}
	err  error
}

func newRelayTestContext() *relayTestContext {
	return &relayTestContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *relayTestContext) Done() <-chan struct{} { return c.done }

func (c *relayTestContext) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *relayTestContext) finish(err error) {
	c.err = err
	close(c.done)
}

func TestRelayTunnelStreamCancellationJoinsBothCopies(t *testing.T) {
	events := make(chan string, 16)
	stream := newRelayTestConn("stream", []byte("received"), net.ErrClosed, true, events)
	upstream := newRelayTestConn("upstream", []byte("sent"), net.ErrClosed, true, events)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh, done := startRelayTest(ctx, stream, upstream)
	t.Cleanup(func() {
		stream.releaseRead()
		upstream.releaseRead()
		waitRelayTestDone(t, done)
	})

	waitRelayTestSignal(t, stream.blocked, "stream copy to reach its barrier")
	waitRelayTestSignal(t, upstream.blocked, "upstream copy to reach its barrier")
	cancel()

	wantRelayTestEvents(t, events, "stream.deadline", "stream.close", "upstream.close")
	assertRelayTestBlocked(t, resultCh)

	stream.releaseRead()
	waitRelayTestSignal(t, stream.readReturned, "stream copy to leave its barrier")
	assertRelayTestBlocked(t, resultCh)

	upstream.releaseRead()
	result := waitRelayTestResult(t, resultCh)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("relay error = %v, want context.Canceled", result.err)
	}
	if result.received != uint64(len(stream.payload)) || result.sent != uint64(len(upstream.payload)) {
		t.Fatalf("relay totals = (%d received, %d sent), want (%d, %d)", result.received, result.sent, len(stream.payload), len(upstream.payload))
	}
}

func TestRelayTunnelStreamDeadlineWhileDrainingJoinsRemainingCopy(t *testing.T) {
	events := make(chan string, 16)
	stream := newRelayTestConn("stream", []byte("received"), io.EOF, false, events)
	upstream := newRelayTestConn("upstream", []byte("sent"), net.ErrClosed, true, events)
	ctx := newRelayTestContext()

	resultCh, done := startRelayTest(ctx, stream, upstream)
	t.Cleanup(func() {
		upstream.releaseRead()
		waitRelayTestDone(t, done)
	})

	waitRelayTestSignal(t, upstream.blocked, "remaining copy to reach its barrier")
	wantRelayTestEvents(t, events, "stream.close", "upstream.close")
	ctx.finish(context.DeadlineExceeded)
	wantRelayTestEvents(t, events, "stream.deadline", "stream.close", "upstream.close")
	assertRelayTestBlocked(t, resultCh)

	upstream.releaseRead()
	result := waitRelayTestResult(t, resultCh)
	if !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("relay error = %v, want context.DeadlineExceeded", result.err)
	}
	if result.received != uint64(len(stream.payload)) || result.sent != uint64(len(upstream.payload)) {
		t.Fatalf("relay totals = (%d received, %d sent), want (%d, %d)", result.received, result.sent, len(stream.payload), len(upstream.payload))
	}
}

func TestRelayTunnelStreamPreservesNormalErrorSelection(t *testing.T) {
	wantErr := errors.New("upstream read failed")
	events := make(chan string, 8)
	stream := newRelayTestConn("stream", []byte("received"), io.EOF, false, events)
	upstream := newRelayTestConn("upstream", []byte("sent"), wantErr, true, events)

	resultCh, done := startRelayTest(context.Background(), stream, upstream)
	t.Cleanup(func() {
		upstream.releaseRead()
		waitRelayTestDone(t, done)
	})

	waitRelayTestSignal(t, upstream.blocked, "remaining copy to reach its barrier")
	wantRelayTestEvents(t, events, "stream.close", "upstream.close")
	upstream.releaseRead()
	result := waitRelayTestResult(t, resultCh)
	if !errors.Is(result.err, wantErr) {
		t.Fatalf("relay error = %v, want %v", result.err, wantErr)
	}
	if result.received != uint64(len(stream.payload)) || result.sent != uint64(len(upstream.payload)) {
		t.Fatalf("relay totals = (%d received, %d sent), want (%d, %d)", result.received, result.sent, len(stream.payload), len(upstream.payload))
	}
}

func TestRelayTunnelStreamCancellationUnblocksYamuxOverTCP(t *testing.T) {
	clientConn, serverConn := relayTestTCPPair(t)
	clientSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("create yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		_ = clientSession.Close()
		t.Fatalf("create yamux server: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	remoteStream, err := clientSession.Open()
	if err != nil {
		t.Fatalf("open yamux stream: %v", err)
	}
	relayStream, err := serverSession.Accept()
	if err != nil {
		_ = remoteStream.Close()
		t.Fatalf("accept yamux stream: %v", err)
	}
	upstream, upstreamPeer := relayTestTCPPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh, done := startRelayTest(ctx, relayStream, upstream)
	t.Cleanup(func() {
		_ = remoteStream.Close()
		_ = upstreamPeer.Close()
		_ = relayStream.SetDeadline(time.Now())
		_ = relayStream.Close()
		_ = upstream.Close()
		waitRelayTestDone(t, done)
	})

	deadline := time.Now().Add(2 * time.Second)
	if err := remoteStream.SetDeadline(deadline); err != nil {
		t.Fatalf("set remote stream deadline: %v", err)
	}
	if err := upstreamPeer.SetDeadline(deadline); err != nil {
		t.Fatalf("set upstream peer deadline: %v", err)
	}
	if _, err := remoteStream.Write([]byte("r")); err != nil {
		t.Fatalf("write remote stream: %v", err)
	}
	if _, err := io.ReadFull(upstreamPeer, make([]byte, 1)); err != nil {
		t.Fatalf("read relayed remote byte: %v", err)
	}
	if _, err := upstreamPeer.Write([]byte("s")); err != nil {
		t.Fatalf("write upstream peer: %v", err)
	}
	if _, err := io.ReadFull(remoteStream, make([]byte, 1)); err != nil {
		t.Fatalf("read relayed upstream byte: %v", err)
	}
	_ = remoteStream.SetDeadline(time.Time{})
	_ = upstreamPeer.SetDeadline(time.Time{})

	cancel()
	result := waitRelayTestResult(t, resultCh)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("relay error = %v, want context.Canceled", result.err)
	}
	if result.received != 1 || result.sent != 1 {
		t.Fatalf("relay totals = (%d received, %d sent), want (1, 1)", result.received, result.sent)
	}
}

func startRelayTest(ctx context.Context, stream net.Conn, upstream net.Conn) (<-chan relayTestResult, <-chan struct{}) {
	resultCh := make(chan relayTestResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		received, sent, err := relayTunnelStream(ctx, stream, upstream)
		resultCh <- relayTestResult{received: received, sent: sent, err: err}
	}()
	return resultCh, done
}

func waitRelayTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func wantRelayTestEvents(t *testing.T, events <-chan string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("relay event = %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for relay event %q", want)
		}
	}
}

func assertRelayTestBlocked(t *testing.T, resultCh <-chan relayTestResult) {
	t.Helper()
	select {
	case result := <-resultCh:
		t.Fatalf("relay returned before all copies completed: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitRelayTestResult(t *testing.T, resultCh <-chan relayTestResult) relayTestResult {
	t.Helper()
	select {
	case result := <-resultCh:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for relay result")
		return relayTestResult{}
	}
}

func waitRelayTestDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("relay goroutine did not stop during cleanup")
	}
}

func relayTestTCPPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("dial loopback listener: %v", err)
	}
	defer listener.Close()
	select {
	case server := <-accepted:
		return client, server
	case err := <-acceptErr:
		_ = client.Close()
		t.Fatalf("accept loopback connection: %v", err)
	case <-time.After(2 * time.Second):
		_ = client.Close()
		t.Fatal("timed out accepting loopback connection")
	}
	return nil, nil
}
