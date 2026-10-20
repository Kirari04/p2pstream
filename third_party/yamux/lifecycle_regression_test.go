package yamux

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type failedOpenPayloadRaceConn struct {
	reader       io.Reader
	readRelease  <-chan struct{}
	writeRelease <-chan struct{}
	closeOnce    sync.Once
	closed       chan struct{}
}

func (c *failedOpenPayloadRaceConn) Read(p []byte) (int, error) {
	<-c.readRelease
	return c.reader.Read(p)
}

func (c *failedOpenPayloadRaceConn) Write([]byte) (int, error) {
	<-c.writeRelease
	return 0, errors.New("synthetic open write failure")
}

func (c *failedOpenPayloadRaceConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func TestFailedOpenTerminatesWireAndReleasesTracking(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	config := testConfNoKeepAlive()
	config.ConnectionWriteTimeout = 30 * time.Millisecond
	client, err := Client(conn, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// No reader consumes the SYN before the write timeout.
	if _, err := client.OpenStream(); !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("open error = %v", err)
	}
	client.streamLock.Lock()
	streams, inflight := len(client.streams), len(client.inflight)
	client.streamLock.Unlock()
	if streams != 0 || inflight != 0 || len(client.synCh) != 0 {
		t.Fatalf("failed open retained streams=%d inflight=%d tokens=%d", streams, inflight, len(client.synCh))
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := peer.Read(make([]byte, headerSize)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("timed-out SYN remained deliverable: n=%d err=%v", n, err)
	}
}

func TestFailedOpenDiscardsPayloadAlreadyAcceptedByReader(t *testing.T) {
	payload := []byte("payload crossing failed Open")
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, 1, uint32(len(payload)))
	gated := &gatedPayloadReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		data:    append([]byte(nil), payload...),
	}
	readRelease := make(chan struct{})
	writeRelease := make(chan struct{})
	var releaseReadOnce, releaseWriteOnce sync.Once
	releaseRead := func() { releaseReadOnce.Do(func() { close(readRelease) }) }
	releaseWrite := func() { releaseWriteOnce.Do(func() { close(writeRelease) }) }
	conn := &failedOpenPayloadRaceConn{
		reader:       io.MultiReader(bytes.NewReader(hdr), gated),
		readRelease:  readRelease,
		writeRelease: writeRelease,
		closed:       make(chan struct{}),
	}
	config := testConfNoKeepAlive()
	client, err := Client(conn, config)
	if err != nil {
		t.Fatal(err)
	}
	var releasePayloadOnce sync.Once
	releasePayload := func() { releasePayloadOnce.Do(func() { close(gated.release) }) }
	defer func() {
		releaseRead()
		releaseWrite()
		releasePayload()
		_ = client.Close()
	}()
	openDone := make(chan error, 1)
	go func() {
		_, openErr := client.OpenStream()
		openDone <- openErr
	}()
	var stream *Stream
	deadline := time.Now().Add(time.Second)
	for stream == nil && time.Now().Before(deadline) {
		client.streamLock.Lock()
		stream = client.streams[1]
		client.streamLock.Unlock()
		if stream == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if stream == nil {
		t.Fatal("Open did not publish its stream before writing the SYN")
	}
	releaseRead()
	select {
	case <-gated.entered:
	case <-time.After(time.Second):
		t.Fatal("reader did not accept forged payload during Open")
	}
	releaseWrite()
	select {
	case err := <-openDone:
		t.Fatalf("Open returned before accepted payload was fenced: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	releasePayload()
	if err := <-openDone; err == nil {
		t.Fatal("Open succeeded after synthetic write failure")
	}
	stream.recvLock.Lock()
	defer stream.recvLock.Unlock()
	if stream.recvBuf != nil {
		t.Fatalf("failed Open retained %d payload bytes", stream.recvBuf.Len())
	}
}

func TestResetWaitsForAcceptedPayloadAfterTerminalState(t *testing.T) {
	payload := []byte("payload crossing an idempotent reset")
	gated := &gatedPayloadReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		data:    append([]byte(nil), payload...),
	}
	var releaseOnce sync.Once
	releasePayload := func() { releaseOnce.Do(func() { close(gated.release) }) }
	defer releasePayload()

	session := &Session{config: testConfNoKeepAlive()}
	stream := newStream(session, 1, streamEstablished)
	readDone := make(chan error, 1)
	go func() {
		_, accepted, err := stream.readDataPayload(uint32(len(payload)), gated)
		if !accepted && err == nil {
			err = errors.New("accepted payload was rejected")
		}
		readDone <- err
	}()
	select {
	case <-gated.entered:
	case <-time.After(time.Second):
		t.Fatal("payload reader did not enter")
	}

	// Publish the terminal state while readDataPayload still owns recvLock.
	// The first cleanup would normally be waiting to discard that payload.
	stream.forceClose()
	resetDone := make(chan error, 1)
	go func() { resetDone <- stream.Reset() }()
	select {
	case err := <-resetDone:
		t.Fatalf("idempotent Reset returned before accepted payload was fenced: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releasePayload()
	if err := <-readDone; err != nil {
		t.Fatalf("payload read: %v", err)
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("Reset: %v", err)
	}
	stream.recvLock.Lock()
	defer stream.recvLock.Unlock()
	if stream.recvBuf != nil {
		t.Fatalf("idempotent Reset retained %d payload bytes", stream.recvBuf.Len())
	}
}

func TestReceiveWindowTimeoutTerminatesSession(t *testing.T) {
	config := testConfNoKeepAlive()
	config.ConnectionWriteTimeout = 30 * time.Millisecond
	clientConn, serverConn := testConnPipe(t)
	client, server := testClientServerConfig(t, clientConn, serverConn, config, config.Clone())
	stream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.AcceptStream(); err != nil {
		t.Fatal(err)
	}
	conn := clientConn.(*pipeConn)
	conn.writeBlocker.Lock()
	err = stream.GrowReceiveWindow(2 * stream.MaxReceiveWindow())
	conn.writeBlocker.Unlock()
	if !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("growth error = %v", err)
	}
	select {
	case <-client.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("uncertain receive credit left the session usable")
	}
	select {
	case <-server.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("peer wire remained usable after failed credit update")
	}
}

func TestRepeatedResetBeforeACKReleasesInflight(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	config := testConfNoKeepAlive()
	client, err := Client(conn, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const attempts = 128
	done := make(chan error, 1)
	go func() {
		for i := 0; i < attempts; i++ {
			hdr := header(make([]byte, headerSize))
			if _, err := io.ReadFull(peer, hdr); err != nil {
				done <- err
				return
			}
			hdr.encode(typeWindowUpdate, flagRST, hdr.StreamID(), 0)
			if _, err := peer.Write(hdr); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < attempts; i++ {
		if _, err := client.OpenStream(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for client.NumStreams() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		client.streamLock.Lock()
		streams, inflight := len(client.streams), len(client.inflight)
		client.streamLock.Unlock()
		if streams != 0 || inflight != 0 || len(client.synCh) != 0 {
			t.Fatalf("reset %d retained streams=%d inflight=%d tokens=%d", i, streams, inflight, len(client.synCh))
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestVerifyConfigRejectsNonPositiveKeepAlive(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		config := DefaultConfig()
		config.KeepAliveInterval = interval
		if err := VerifyConfig(config); err == nil {
			t.Fatalf("accepted keep-alive interval %v", interval)
		}
	}
}
