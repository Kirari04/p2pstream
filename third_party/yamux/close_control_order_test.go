package yamux

import (
	"errors"
	"io"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Model a Write that has passed its state check but has not queued DATA yet.
// Holding the actual writer mutex makes this interleaving explicit without a
// production hook in the gap between state validation and frame publication.
func TestLegacyCloseOrdersFINAfterAdmittedWriter(t *testing.T) {
	session := newLegacyUnitSession(nil)
	session.config.StreamCloseTimeout = 0
	session.config.ConnectionWriteTimeout = time.Second
	stream := newStream(session, 1, streamEstablished)
	session.streams[1] = stream
	stream.sendLock.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(stream.sendLock.Unlock) }
	closed := make(chan error, 1)
	go func() { closed <- stream.Close() }()
	defer func() {
		unlock()
		close(session.shutdownCh)
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Error("Close did not join")
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		stream.stateLock.Lock()
		state := stream.state
		stream.stateLock.Unlock()
		if state == streamLocalClose {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not publish terminal state")
		}
		runtime.Gosched()
	}
	// A forbidden FIN is a positive failure observation. This bounded quiet
	// interval only gives the old Close path an opportunity to publish it.
	select {
	case frame := <-session.sendCh:
		asyncSendErr(frame.Err, nil)
		t.Fatalf("FIN crossed admitted writer: type=%d flags=%d", header(frame.Hdr).MsgType(), header(frame.Hdr).Flags())
	case <-time.After(25 * time.Millisecond):
	}
	data := header(make([]byte, headerSize))
	data.encode(typeData, 0, 1, 1)
	sent := make(chan error, 1)
	sendJoined := make(chan struct{})
	go func() { defer close(sendJoined); sent <- session.waitForSend(data, []byte{42}) }()
	defer func() {
		select {
		case <-sendJoined:
		case <-time.After(2 * time.Second):
			t.Error("DATA sender did not join cleanup")
		}
	}()
	select {
	case frame := <-session.sendCh:
		if header(frame.Hdr).MsgType() != typeData {
			t.Errorf("first frame type=%d, want DATA", header(frame.Hdr).MsgType())
		}
		asyncSendErr(frame.Err, nil)
	case <-time.After(time.Second):
		t.Fatal("DATA was not queued")
	}
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DATA sender did not join")
	}
	unlock()
	select {
	case frame := <-session.sendCh:
		if header(frame.Hdr).MsgType() != typeWindowUpdate || header(frame.Hdr).Flags()&flagFIN == 0 {
			t.Errorf("terminal frame type=%d flags=%d", header(frame.Hdr).MsgType(), header(frame.Hdr).Flags())
		}
		asyncSendErr(frame.Err, nil)
	case <-time.After(time.Second):
		t.Fatal("FIN was not queued after DATA")
	}
}

func TestLegacyCloseWakesCreditBlockedWriter(t *testing.T) {
	session := newLegacyUnitSession(nil)
	session.config.StreamCloseTimeout = 0
	session.config.ConnectionWriteTimeout = time.Second
	stream := newStream(session, 1, streamEstablished)
	atomic.StoreUint32(&stream.sendWindow, 0)
	written := make(chan error, 1)
	writeJoined := make(chan struct{})
	go func() { defer close(writeJoined); _, err := stream.Write([]byte{1}); written <- err }()
	var closeJoined chan struct{}
	defer func() {
		close(session.shutdownCh)
		for _, joined := range []chan struct{}{writeJoined, closeJoined} {
			if joined == nil {
				continue
			}
			select {
			case <-joined:
			case <-time.After(2 * time.Second):
				t.Error("stream worker did not join cleanup")
			}
		}
	}()
	deadline := time.Now().Add(time.Second)
	for stream.sendLock.TryLock() {
		stream.sendLock.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("Write did not enter")
		}
		runtime.Gosched()
	}
	closed := make(chan error, 1)
	closeJoined = make(chan struct{})
	go func() { defer close(closeJoined); closed <- stream.Close() }()
	select {
	case err := <-written:
		if !errors.Is(err, ErrStreamClosed) {
			t.Errorf("Write error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake Write")
	}
	select {
	case frame := <-session.sendCh:
		asyncSendErr(frame.Err, nil)
	case <-time.After(time.Second):
		t.Fatal("Close did not queue FIN")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not join")
	}
}

func TestAggregateOrdinaryPingControlWaitsForQueueCapacity(t *testing.T) {
	done := func() chan struct{} { ch := make(chan struct{}); close(ch); return ch }
	session := &Session{
		config: aggregateTestConfig(), logger: log.New(io.Discard, "", 0),
		conn: newPriorityRecordingConn(), streams: make(map[uint32]*Stream),
		aggregate: &aggregateWindow{publishDone: done()},
		controlCh: make(chan *sendReady, aggregateControlQueueSize), sendCh: make(chan *sendReady, 1),
		shutdownCh: make(chan struct{}), recvDoneCh: done(), sendDoneCh: done(), pingReplyDoneCh: done(),
	}
	session.config.ConnectionWriteTimeout = 25 * time.Millisecond
	defer session.Close()
	for i := 0; i < cap(session.controlCh); i++ {
		session.controlCh <- &sendReady{}
	}
	hdr := header(make([]byte, headerSize))
	hdr.encode(typePing, flagACK, 0, 7)
	// The same path used by the dedicated ping-reply worker. Ordinary queue
	// congestion may time out its publication, but must not abort the reader.
	if err := session.sendNoWait(hdr); !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("full priority queue: %v, want bounded write timeout", err)
	}
	if session.IsClosed() {
		t.Fatal("ordinary ping publication aborted session")
	}
	select {
	case <-session.shutdownCh:
		t.Fatal("session shut down")
	default:
	}
	<-session.controlCh
	if err := session.sendNoWait(hdr); err != nil {
		t.Fatalf("available priority slot: %v", err)
	}
	if len(session.sendCh) != 0 {
		t.Fatal("ping bypassed priority queue")
	}
	for i := 0; i < cap(session.controlCh)-1; i++ {
		<-session.controlCh
	}
	if queued := <-session.controlCh; header(queued.Hdr).MsgType() != typePing || header(queued.Hdr).Length() != 7 {
		t.Fatal("ping reply was not retained in priority queue")
	}
}
