package yamux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestLegacyPendingCreditAcceptTransitionAndShutdownDisposal(t *testing.T) {
	cfg := testConf()
	cfg.MaxStreamWindowSize = 512 << 10
	cfg.AcceptBacklog = 64
	left, right := testConnPipe(t)
	sender, receiver := testClientServerConfig(t, left, right, cfg, cfg.Clone())
	defer sender.Close()
	defer receiver.Close()
	first, err := sender.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{23}, 256<<10)
	first.SetWriteDeadline(time.Now().Add(time.Second))
	if n, err := first.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("initial credit %d %v", n, err)
	}
	first.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	if n, err := first.Write([]byte{1}); n != 0 || err == nil {
		t.Fatalf("unaccepted stream exceeded 256KiB: %d %v", n, err)
	}
	accepted, err := receiver.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	first.SetWriteDeadline(time.Now().Add(time.Second))
	if n, err := first.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("acceptance credit %d %v", n, err)
	}
	// The pending queue refills while the full-window transition remains owned.
	var pending []*Stream
	for i := 0; i < cfg.AcceptBacklog; i++ {
		peer, err := sender.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		peer.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := peer.Write(payload[:4096]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sender.Ping(); err != nil {
		t.Fatal(err)
	}
	receiver.streamLock.Lock()
	for _, s := range receiver.streams {
		if s != accepted {
			pending = append(pending, s)
		}
	}
	receiver.streamLock.Unlock()
	if len(pending) != cfg.AcceptBacklog {
		t.Fatalf("pending count %d", len(pending))
	}
	for _, s := range pending {
		if b, a := s.ReceiveBufferStats(); b != 4096 || a > ReceiveWindowMemory(initialStreamWindow) {
			t.Fatalf("pending storage %d %d", b, a)
		}
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	for _, s := range pending {
		if b, a := s.ReceiveBufferStats(); b != 0 || a != 0 {
			t.Fatalf("unowned pending buffer survived physical close: %d %d", b, a)
		}
		if _, err := s.Read(make([]byte, 1)); !errors.Is(err, ErrConnectionReset) {
			t.Fatalf("discard hidden as EOF: %v", err)
		}
	}
	// Accepted data remains the application's ownership, preserving ordinary
	// drain-after-close. The application must explicitly dispose it on abandon.
	got, err := io.ReadAll(accepted)
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte{23}, 512<<10)) {
		t.Fatalf("accepted buffer drain %d %v", len(got), err)
	}
	accepted.Reset()
}

type legacyPendingClosedWire struct{ bytes.Buffer }

func (*legacyPendingClosedWire) Close() error { return nil }

func TestLegacyFailedAcceptDisposesUntransferredOwnership(t *testing.T) {
	for _, withContext := range []bool{false, true} {
		session := newLegacyUnitSession(bytes.NewReader([]byte("pending")))
		session.config.ConnectionWriteTimeout = 2 * time.Millisecond
		session.conn = &legacyPendingClosedWire{}
		joined := make(chan struct{})
		close(joined)
		session.recvDoneCh, session.sendDoneCh, session.pingReplyDoneCh = joined, joined, joined
		stream := newStream(session, 1, streamSYNReceived)
		session.streams[1] = stream
		hdr := header(make([]byte, headerSize))
		hdr.encode(typeData, 0, 1, 7)
		if err := session.handleStreamMessage(hdr); err != nil {
			t.Fatal(err)
		}
		session.acceptCh <- stream
		session.sendCh <- &sendReady{} // deterministic pre-enqueue ACK rejection
		var got *Stream
		var err error
		if withContext {
			got, err = session.AcceptStreamWithContext(context.Background())
		} else {
			got, err = session.AcceptStream()
		}
		session.Close() // joins the asynchronous fatal credit-publication cleanup
		if got != nil || !errors.Is(err, ErrConnectionWriteTimeout) {
			t.Fatalf("failed accept: %v %v", got, err)
		}
		if b, a := stream.ReceiveBufferStats(); b != 0 || a != 0 || session.NumStreams() != 0 {
			t.Fatalf("failed accept retained buffer/map: %d %d", b, a)
		}
		if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, ErrConnectionReset) {
			t.Fatalf("failed accept retained readable credit: %v", err)
		}
	}
}
