package yamux

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ownedSendFixture(t *testing.T, state streamState) (*Session, *Stream, *priorityRecordingConn) {
	t.Helper()
	conn := newPriorityRecordingConn()
	session := newGatherSendSession(conn, 30*time.Millisecond)
	session.config = smallAggregateTestConfig()
	session.config.InitialSessionWindowSize = 2 << 20
	session.config.MaxSessionWindowSize = 8 << 20
	session.config.InitialStreamAllocationWindowSize = 512 << 10
	session.config.MaxStreamAllocationWindowSize = 2 << 20
	session.config.ConnectionWriteTimeout = 30 * time.Millisecond
	session.aggregate = newAggregateWindow(session.config)
	session.streams = make(map[uint32]*Stream)
	session.recvDoneCh = closedGatherChannel()
	session.pingReplyDoneCh = closedGatherChannel()
	close(session.aggregate.publishDone) // no receiver or publisher in this sender fixture
	stream := newStream(session, 1, state)
	session.streams[1] = stream
	return session, stream, conn
}

func assertOwnedSendCredit(t *testing.T, s *Stream, bytes, units uint32) {
	t.Helper()
	a := s.session.aggregate
	a.mu.Lock()
	defer a.mu.Unlock()
	if got := atomic.LoadUint32(&s.sendWindow); got != initialStreamWindow-bytes {
		t.Fatalf("byte credit %d, consumed %d", got, bytes)
	}
	if got := a.sendWindow; got != s.session.config.InitialSessionWindowSize-units*AggregateWindowUnitSize {
		t.Fatalf("session credit %d, consumed units %d", got, units)
	}
	if got := s.allocationSendWindow; got != s.session.config.InitialStreamAllocationWindowSize-units*AggregateWindowUnitSize {
		t.Fatalf("stream credit %d, consumed units %d", got, units)
	}
}

func TestAggregateSendPrequeueRejectionPreservesCreditAndFlags(t *testing.T) {
	for _, state := range []streamState{streamInit, streamSYNReceived} {
		for _, shutdown := range []bool{false, true} {
			s, stream, conn := ownedSendFixture(t, state)
			for i := 0; i < cap(s.sendCh); i++ {
				s.sendCh <- &sendReady{}
			}
			if shutdown {
				close(s.shutdownCh)
			}
			n, err := stream.Write([]byte("rejected"))
			expected := ErrConnectionWriteTimeout
			if shutdown {
				expected = ErrSessionShutdown
			}
			if n != 0 || !errors.Is(err, expected) {
				t.Fatalf("rejected Write = %d, %v", n, err)
			}
			assertOwnedSendCredit(t, stream, 0, 0)
			if stream.state != state || stream.establishmentFlags == 0 || len(s.aggregate.sendSlots) != 0 {
				t.Fatal("rejection consumed flags or retained frame")
			}
			for len(s.sendCh) > 0 {
				<-s.sendCh
			}
			if shutdown {
				continue
			}
			close(conn.releaseFirst)
			go s.send()
			// A control can now consume the untouched SYN/ACK. The next DATA must
			// neither restore nor duplicate a flag already published by that control.
			if err := stream.sendWindowUpdate(); err != nil {
				t.Fatal(err)
			}
			if n, err := stream.Write([]byte("accepted")); n != 8 || err != nil {
				t.Fatalf("retry = %d,%v", n, err)
			}
			s.Close()
			conn.mu.Lock()
			frames := conn.writes
			conn.mu.Unlock()
			want := uint16(flagSYN)
			if state == streamSYNReceived {
				want = flagACK
			}
			if len(frames) != 2 || header(frames[0]).Flags() != want || header(frames[1]).Flags() != 0 {
				t.Fatalf("flags/order: %x", frames)
			}
			assertOwnedSendCredit(t, stream, 8, 1)
		}
	}
}

func TestAggregateSendTimeoutOwnsPayloadAndCompletion(t *testing.T) {
	s, stream, conn := ownedSendFixture(t, streamEstablished)
	go s.send()
	t.Cleanup(func() { s.Close() })
	payload := []byte("original payload")
	if n, err := stream.Write(payload); n != len(payload) || !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("first = %d,%v", n, err)
	}
	copy(payload, bytes.Repeat([]byte("z"), len(payload)))
	if n, err := stream.Write([]byte("second")); n != 6 || !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("second = %d,%v", n, err)
	}
	if s.IsClosed() {
		t.Fatal("caller timeout killed session")
	}
	assertOwnedSendCredit(t, stream, uint32(len(payload)+6), 2)
	close(conn.releaseFirst)
	for i := 0; i < 2; i++ {
		select {
		case <-conn.wrote:
		case <-time.After(time.Second):
			t.Fatal("accepted send lost")
		}
	}
	// A stale completion must not acknowledge this new frame.
	if n, err := stream.Write([]byte("third")); n != 5 || err != nil {
		t.Fatalf("third = %d,%v", n, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	conn.mu.Lock()
	frames := conn.writes
	conn.mu.Unlock()
	if len(frames) != 4 || string(frames[0][headerSize:]) != "original payload" || string(frames[1][headerSize:]) != "second" || string(frames[2][headerSize:]) != "third" || header(frames[3]).Flags()&flagFIN == 0 {
		t.Fatalf("payload/FIN order: %x", frames)
	}
	assertOwnedSendCredit(t, stream, uint32(len(payload)+11), 3)
	if len(s.aggregate.sendSlots) != 0 {
		t.Fatal("completed frames retained sender slots")
	}
}

func TestAggregateSendBoundAndShutdownDiscard(t *testing.T) {
	s, stream, conn := ownedSendFixture(t, streamEstablished)
	go s.send()
	for i := 0; i < aggregateSendSlots; i++ {
		if n, err := stream.Write([]byte{byte(i)}); n != 1 || !errors.Is(err, ErrConnectionWriteTimeout) {
			t.Fatalf("queued %d = %d,%v", i, n, err)
		}
	}
	if n, err := stream.Write([]byte("no slot")); n != 0 || !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("bound = %d,%v", n, err)
	}
	assertOwnedSendCredit(t, stream, aggregateSendSlots, aggregateSendSlots)
	if len(s.aggregate.sendSlots) != aggregateSendSlots {
		t.Fatal("unbounded or prematurely released frame ownership")
	}
	s.Close() // Close unblocks physical Write and joins/discards the remaining FIFO.
	if len(s.aggregate.sendSlots) != 0 || len(s.sendCh) != 0 {
		t.Fatal("shutdown retained accepted frame memory")
	}
	if n, err := stream.Write([]byte("after close")); n != 0 || err == nil {
		t.Fatalf("shutdown Write = %d,%v", n, err)
	}
	_ = conn
}

func TestAggregateSendRSTFollowsTimedOutData(t *testing.T) {
	s, stream, conn := ownedSendFixture(t, streamEstablished)
	go s.send()
	if n, err := stream.Write([]byte("before RST")); n != 10 || !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("Write=%d,%v", n, err)
	}
	if err := stream.Reset(); err != nil {
		t.Fatal(err)
	}
	close(conn.releaseFirst)
	for i := 0; i < 2; i++ {
		select {
		case <-conn.wrote:
		case <-time.After(time.Second):
			t.Fatal("missing DATA/RST")
		}
	}
	s.Close()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.writes) != 2 || header(conn.writes[0]).MsgType() != typeData || header(conn.writes[1]).Flags()&flagRST == 0 {
		t.Fatalf("RST order=%x", conn.writes)
	}
}

// Gate actual wire writes without blocking Close. This is the success-oriented
// version of the audit's lane-abort reproduction, with another live stream.
type ownedSendGate struct {
	net.Conn
	mu     sync.Mutex
	gate   chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (g *ownedSendGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	gate := g.gate
	g.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-g.closed:
			return 0, io.ErrClosedPipe
		}
	}
	return g.Conn.Write(p)
}
func (g *ownedSendGate) Close() error { g.once.Do(func() { close(g.closed) }); return g.Conn.Close() }

func TestAggregateWriteTimeoutPreservesUnrelatedStream(t *testing.T) {
	left, right := net.Pipe()
	gate := &ownedSendGate{Conn: left, closed: make(chan struct{})}
	cfg := smallAggregateTestConfig()
	cfg.ConnectionWriteTimeout = 40 * time.Millisecond
	client, err := Client(gate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := Server(right, cfg)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	victim, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	victimPeer, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	other, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	otherPeer, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	gate.mu.Lock()
	gate.gate = blocked
	gate.mu.Unlock()
	payload := []byte("accepted before timeout")
	if n, err := victim.Write(payload); n != len(payload) || !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("Write=%d,%v", n, err)
	}
	copy(payload, bytes.Repeat([]byte("z"), len(payload)))
	close(blocked)
	victimPeer.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(victimPeer, got); err != nil || string(got) != "accepted before timeout" {
		t.Fatalf("payload %q,%v", got, err)
	}
	if n, err := other.Write([]byte("alive")); n != 5 || err != nil {
		t.Fatalf("unrelated Write=%d,%v", n, err)
	}
	otherPeer.SetReadDeadline(time.Now().Add(time.Second))
	got = make([]byte, 5)
	if _, err := io.ReadFull(otherPeer, got); err != nil || string(got) != "alive" {
		t.Fatalf("unrelated read=%q,%v", got, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		client.aggregate.mu.Lock()
		conserved := client.aggregate.sendWindow == cfg.InitialSessionWindowSize &&
			victim.allocationSendWindow == cfg.InitialStreamAllocationWindowSize &&
			other.allocationSendWindow == cfg.InitialStreamAllocationWindowSize
		client.aggregate.mu.Unlock()
		if conserved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("read refunds did not restore exact session and stream credit")
		}
		time.Sleep(time.Millisecond)
	}
	if client.IsClosed() || server.IsClosed() {
		t.Fatal("healthy lane aborted")
	}
}

func TestAggregateSendCompletionAtDeadline(t *testing.T) {
	// Both signals are ready: regardless of select choice, a published physical
	// completion wins. Each send has a distinct channel, including failures.
	s := &Session{shutdownCh: make(chan struct{})}
	close(s.shutdownCh)
	timeout := make(chan time.Time, 1)
	timeout <- time.Now()
	close(timeout)
	for i := 0; i < 100; i++ {
		ready := &sendReady{Err: make(chan error, 1)}
		ready.Err <- nil
		state, err := s.waitAggregateSendCompletion(ready, timeout)
		if state != aggregateSendCompleted || err != nil {
			t.Fatalf("completed at deadline: %v,%v", state, err)
		}
	}
}

func TestAggregateSendRejectedBeforeConcurrentCloseKeepsACK(t *testing.T) {
	s, stream, conn := ownedSendFixture(t, streamSYNReceived)
	for i := 0; i < cap(s.sendCh); i++ {
		s.sendCh <- &sendReady{}
	}
	wrote := make(chan error, 1)
	go func() {
		n, err := stream.Write([]byte("rejected"))
		if n != 0 {
			wrote <- errors.New("prequeue write accepted")
			return
		}
		wrote <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(s.aggregate.sendSlots) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.aggregate.sendSlots) == 0 {
		t.Fatal("write never started")
	}
	closed := make(chan struct{})
	go func() { stream.Close(); close(closed) }()
	if err := <-wrote; !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatal(err)
	}
	for i := 0; i < cap(s.sendCh); i++ {
		<-s.sendCh
	}
	close(conn.releaseFirst)
	go s.send()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close stuck")
	}
	s.Close()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.writes) != 1 || header(conn.writes[0]).Flags() != (flagACK|flagFIN) {
		t.Fatalf("Close lost ACK or emitted rejected DATA: %x", conn.writes)
	}
	assertOwnedSendCredit(t, stream, 0, 0)
}

func TestAggregateSendShutdownRacesAcceptedWrite(t *testing.T) {
	for i := 0; i < 20; i++ {
		s, stream, conn := ownedSendFixture(t, streamEstablished)
		go s.send()
		done := make(chan struct{})
		var n int
		var err error
		go func() { n, err = stream.Write([]byte("owned")); close(done) }()
		select {
		case <-conn.firstStarted:
		case <-time.After(time.Second):
			t.Fatal("physical send not started")
		}
		s.Close()
		<-done
		if n != 5 || err == nil {
			t.Fatalf("shutdown write=%d,%v", n, err)
		}
		assertOwnedSendCredit(t, stream, 5, 1)
		if len(s.aggregate.sendSlots) != 0 || len(s.sendCh) != 0 {
			t.Fatal("shutdown race leaked frame")
		}
	}
}
