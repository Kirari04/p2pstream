package yamux

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"runtime"
	"sync"
	"testing"
	"time"
)

func newLegacyUnitSession(body io.Reader) *Session {
	config := testConfNoKeepAlive()
	if body == nil {
		body = bytes.NewReader(nil)
	}
	return &Session{
		config:     config,
		logger:     log.New(io.Discard, "", 0),
		bufRead:    bufio.NewReader(body),
		streams:    make(map[uint32]*Stream),
		inflight:   make(map[uint32]struct{}),
		synCh:      make(chan struct{}, config.AcceptBacklog),
		acceptCh:   make(chan *Stream, config.AcceptBacklog),
		shutdownCh: make(chan struct{}),
		sendCh:     make(chan *sendReady, 1),
	}
}

func TestLegacyFinalDataBufferedBeforeFINIsPublished(t *testing.T) {
	payload := []byte("final legacy payload")
	reader := &gatedPayloadReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		data:    append([]byte(nil), payload...),
	}
	var releaseOnce sync.Once
	releaseReader := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer releaseReader()
	session := newLegacyUnitSession(reader)
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream

	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, flagFIN, stream.id, uint32(len(payload)))
	handleDone := make(chan error, 1)
	go func() { handleDone <- session.handleStreamMessage(hdr) }()
	<-reader.entered
	stream.stateLock.Lock()
	stateWhilePayloadBlocked := stream.state
	stream.stateLock.Unlock()
	if stateWhilePayloadBlocked != streamEstablished {
		t.Fatalf("state while final payload blocked=%d want established", stateWhilePayloadBlocked)
	}

	type readResult struct {
		n   int
		err error
		buf []byte
	}
	readDone := make(chan readResult, 1)
	go func() {
		buf := make([]byte, len(payload))
		n, err := stream.Read(buf)
		readDone <- readResult{n: n, err: err, buf: buf}
	}()
	select {
	case result := <-readDone:
		t.Fatalf("Read returned before final payload arrived: n=%d err=%v", result.n, result.err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseReader()
	if err := <-handleDone; err != nil {
		t.Fatalf("handle final DATA: %v", err)
	}
	result := <-readDone
	if result.err != nil || result.n != len(payload) || !bytes.Equal(result.buf[:result.n], payload) {
		t.Fatalf("final Read n=%d err=%v payload=%q", result.n, result.err, result.buf[:result.n])
	}
	if n, err := stream.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("post-FIN Read n=%d err=%v", n, err)
	}
}

func TestLegacyTerminalBeforePayloadReadDiscardsWithoutRetainingBuffer(t *testing.T) {
	payload := []byte("late payload")
	session := newLegacyUnitSession(nil)
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream

	if err := stream.Reset(); err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(payload)
	if copied, accepted, err := stream.readDataPayload(uint32(len(payload)), reader); copied != 0 || accepted || err != nil {
		t.Fatalf("terminal payload copied=%d accepted=%t err=%v", copied, accepted, err)
	}
	if reader.Len() != len(payload) {
		t.Fatalf("terminal stream consumed %d payload bytes before discard", len(payload)-reader.Len())
	}
	if err := stream.discardDataPayload(uint32(len(payload)), reader); err != nil {
		t.Fatal(err)
	}
	stream.recvLock.Lock()
	defer stream.recvLock.Unlock()
	if stream.recvBuf != nil {
		t.Fatalf("terminal stream retained receive buffer: %d bytes", stream.recvBuf.Len())
	}
}

func TestLegacyResetCrossingPayloadReadDiscardsBeforeReturning(t *testing.T) {
	payload := []byte("payload crossing reset")
	reader := &gatedPayloadReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		data:    append([]byte(nil), payload...),
	}
	var releaseOnce sync.Once
	releaseReader := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer releaseReader()
	session := newLegacyUnitSession(reader)
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, stream.id, uint32(len(payload)))

	handleDone := make(chan error, 1)
	go func() { handleDone <- session.handleStreamMessage(hdr) }()
	<-reader.entered
	resetDone := make(chan error, 1)
	go func() { resetDone <- stream.Reset() }()
	deadline := time.Now().Add(time.Second)
	for {
		// Reset publishes streamReset while holding stateLock, then waits for
		// the in-flight payload's recvLock before it returns. Observing that
		// lock held proves Reset has crossed the state transition without
		// creating a test-side stateLock -> recvLock deadlock.
		if !stream.stateLock.TryLock() {
			break
		}
		stream.stateLock.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("Reset did not reach terminal cleanup")
		}
		runtime.Gosched()
	}
	releaseReader()
	if err := <-handleDone; err != nil {
		t.Fatalf("DATA crossing Reset: %v", err)
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("Reset: %v", err)
	}
	stream.recvLock.Lock()
	defer stream.recvLock.Unlock()
	if stream.recvBuf != nil {
		t.Fatalf("Reset returned with retained receive buffer: %d bytes", stream.recvBuf.Len())
	}
}

func TestLegacyDataAfterRemoteFINIsProtocolError(t *testing.T) {
	payload := []byte("late")
	session := newLegacyUnitSession(nil)
	stream := newStream(session, 1, streamRemoteClose)
	session.streams[stream.id] = stream
	reader := bytes.NewReader(payload)
	if copied, accepted, err := stream.readDataPayload(uint32(len(payload)), reader); copied != 0 || accepted || !errors.Is(err, ErrUnexpectedFlag) {
		t.Fatalf("post-FIN payload copied=%d accepted=%t err=%v", copied, accepted, err)
	}
	if reader.Len() != len(payload) {
		t.Fatalf("post-FIN DATA consumed %d bytes", len(payload)-reader.Len())
	}
}

func TestLegacyFinalFINCrossingLocalTimeoutDoesNotKillSession(t *testing.T) {
	payload := []byte("final payload crossing timeout")
	reader := &gatedPayloadReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		data:    append([]byte(nil), payload...),
	}
	var releaseOnce sync.Once
	releaseReader := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer releaseReader()
	session := newLegacyUnitSession(reader)
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, flagFIN, stream.id, uint32(len(payload)))

	handleDone := make(chan error, 1)
	go func() { handleDone <- session.handleStreamMessage(hdr) }()
	<-reader.entered
	timeoutDone := make(chan struct{})
	go func() {
		stream.closeTimeout()
		close(timeoutDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		stream.stateLock.Lock()
		state := stream.state
		stream.stateLock.Unlock()
		if state == streamClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close timeout did not publish terminal state")
		}
		runtime.Gosched()
	}
	releaseReader()
	if err := <-handleDone; err != nil {
		t.Fatalf("final DATA|FIN crossing timeout: %v", err)
	}
	<-timeoutDone
	stream.recvLock.Lock()
	defer stream.recvLock.Unlock()
	if stream.recvBuf != nil {
		t.Fatalf("close timeout returned with retained receive buffer: %d bytes", stream.recvBuf.Len())
	}
}

func TestLegacyTerminalDataStillEnforcesAdvertisedWindow(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags uint16
		reset bool
	}{
		{name: "already reset", reset: true},
		{name: "RST frame", flags: flagRST},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newLegacyUnitSession(nil)
			stream := newStream(session, 1, streamEstablished)
			session.streams[stream.id] = stream
			if test.reset {
				if err := stream.Reset(); err != nil {
					t.Fatal(err)
				}
			}
			payload := bytes.Repeat([]byte{0x7a}, int(stream.recvWindow)+1)
			reader := bytes.NewReader(payload)
			if err := stream.readData(headerWithLength(uint32(len(payload))), test.flags, reader); !errors.Is(err, ErrRecvWindowExceeded) {
				t.Fatalf("oversized terminal DATA error=%v", err)
			}
			if reader.Len() != len(payload) {
				t.Fatalf("oversized terminal DATA consumed %d bytes", len(payload)-reader.Len())
			}
		})
	}
}

func headerWithLength(length uint32) header {
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, 1, length)
	return hdr
}
