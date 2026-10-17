package yamux

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gatherRecordingConn struct {
	mu        sync.Mutex
	writes    [][]byte
	writeCaps []int
	committed *atomic.Bool
	wrote     chan struct{}
}

func newGatherRecordingConn() *gatherRecordingConn {
	return &gatherRecordingConn{wrote: make(chan struct{}, 8)}
}

func (*gatherRecordingConn) Read([]byte) (int, error) { return 0, io.EOF }
func (*gatherRecordingConn) Close() error             { return nil }
func (c *gatherRecordingConn) Write(p []byte) (int, error) {
	if c.committed != nil && !c.committed.Load() {
		return 0, errors.New("write preceded commit")
	}
	copyOfP := append([]byte(nil), p...)
	c.mu.Lock()
	c.writes = append(c.writes, copyOfP)
	c.writeCaps = append(c.writeCaps, cap(p))
	c.mu.Unlock()
	c.wrote <- struct{}{}
	return len(p), nil
}

func newGatherSendSession(conn io.ReadWriteCloser, timeout time.Duration) *Session {
	config := DefaultConfig()
	config.ConnectionWriteTimeout = timeout
	return &Session{
		config:     config,
		logger:     log.New(io.Discard, "", 0),
		conn:       conn,
		sendCh:     make(chan *sendReady, 8),
		controlCh:  make(chan *sendReady, 8),
		sendDoneCh: make(chan struct{}),
		shutdownCh: make(chan struct{}),
	}
}

func stopGatherSendLoop(t *testing.T, session *Session) {
	t.Helper()
	close(session.shutdownCh)
	select {
	case <-session.sendDoneCh:
	case <-time.After(time.Second):
		t.Fatal("send loop did not stop")
	}
}

func TestSendLoopGathersDataAfterCopyAndCommit(t *testing.T) {
	conn := newGatherRecordingConn()
	var committed atomic.Bool
	conn.committed = &committed
	session := newGatherSendSession(conn, time.Second)
	go func() { _ = session.sendLoop() }()

	body := []byte("gathered payload")
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, flagSYN, 7, uint32(len(body)))
	errCh := make(chan error, 1)
	if err := session.waitForSendErrCommit(hdr, body, errCh, func() { committed.Store(true) }); err != nil {
		t.Fatal(err)
	}
	stopGatherSendLoop(t, session)

	conn.mu.Lock()
	writes := append([][]byte(nil), conn.writes...)
	conn.mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("physical writes=%d want=1", len(writes))
	}
	want := append(append([]byte(nil), hdr...), body...)
	if !bytes.Equal(writes[0], want) {
		t.Fatalf("frame=%x want=%x", writes[0], want)
	}
}

func TestSendLoopLeavesHeaderOnlyControlSeparate(t *testing.T) {
	conn := newGatherRecordingConn()
	session := newGatherSendSession(conn, time.Second)
	go func() { _ = session.sendLoop() }()
	hdr := header(make([]byte, headerSize))
	hdr.encode(typePing, flagACK, 0, 19)
	if err := session.waitForSend(hdr, nil); err != nil {
		t.Fatal(err)
	}
	stopGatherSendLoop(t, session)

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.writes) != 1 || !bytes.Equal(conn.writes[0], hdr) {
		t.Fatalf("control writes=%d frame=%x", len(conn.writes), conn.writes)
	}
}

type blockingGatherConn struct {
	started chan struct{}
	release chan struct{}
	wrote   chan []byte
}

func newBlockingGatherConn() *blockingGatherConn {
	return &blockingGatherConn{
		started: make(chan struct{}),
		release: make(chan struct{}),
		wrote:   make(chan []byte, 1),
	}
}

func (*blockingGatherConn) Read([]byte) (int, error) { return 0, io.EOF }
func (*blockingGatherConn) Close() error             { return nil }
func (c *blockingGatherConn) Write(p []byte) (int, error) {
	close(c.started)
	<-c.release
	frame := append([]byte(nil), p...)
	c.wrote <- frame
	return len(p), nil
}

func TestSendLoopSelectedTimeoutOwnsGatheredFrame(t *testing.T) {
	conn := newBlockingGatherConn()
	session := newGatherSendSession(conn, 20*time.Millisecond)
	go func() { _ = session.sendLoop() }()
	body := []byte("selected timeout payload")
	wantBody := append([]byte(nil), body...)
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, 5, uint32(len(body)))
	wantHdr := append([]byte(nil), hdr...)
	errCh := make(chan error, 1)
	waitDone := make(chan error, 1)
	go func() { waitDone <- session.waitForSendErr(hdr, body, errCh) }()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("physical write did not start")
	}
	select {
	case err := <-waitDone:
		if !errors.Is(err, ErrConnectionWriteTimeout) {
			t.Fatalf("wait error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("selected frame did not time out")
	}
	for i := range body {
		body[i] = 0
	}
	close(conn.release)
	var got []byte
	select {
	case got = <-conn.wrote:
	case <-time.After(time.Second):
		t.Fatal("physical write did not resume")
	}
	stopGatherSendLoop(t, session)
	want := append(wantHdr, wantBody...)
	if !bytes.Equal(got, want) {
		t.Fatalf("frame=%x want=%x", got, want)
	}
}

func TestSendLoopQueuedTimeoutClonesBodyBeforeCallerReuse(t *testing.T) {
	conn := newGatherRecordingConn()
	session := newGatherSendSession(conn, 20*time.Millisecond)
	body := []byte("queued timeout payload")
	wantBody := append([]byte(nil), body...)
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, 9, uint32(len(body)))
	wantHdr := append([]byte(nil), hdr...)
	errCh := make(chan error, 1)
	if err := session.waitForSendErr(hdr, body, errCh); !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("wait error=%v", err)
	}
	for i := range body {
		body[i] = 0
	}
	// Production replaces the caller's header slice after a queued timeout; it
	// does not mutate the old backing array retained by sendReady.
	hdr = header(make([]byte, headerSize))
	go func() { _ = session.sendLoop() }()
	select {
	case <-conn.wrote:
	case <-time.After(time.Second):
		t.Fatal("queued frame was not written")
	}
	stopGatherSendLoop(t, session)

	conn.mu.Lock()
	got := append([]byte(nil), conn.writes[0]...)
	conn.mu.Unlock()
	want := append(wantHdr, wantBody...)
	if !bytes.Equal(got, want) {
		t.Fatalf("frame=%x want=%x", got, want)
	}
}

type partialGatherConn struct {
	boundary int
	err      error
	writes   int
}

func (*partialGatherConn) Read([]byte) (int, error) { return 0, io.EOF }
func (*partialGatherConn) Close() error             { return nil }
func (c *partialGatherConn) Write(p []byte) (int, error) {
	c.writes++
	n := c.boundary
	if n > len(p) {
		n = len(p)
	}
	return n, c.err
}

func TestSendLoopGatheredDataFailsPartialWriteWithoutRetry(t *testing.T) {
	body := bytes.Repeat([]byte{0x5a}, 64)
	total := headerSize + len(body)
	boundaries := []int{0, headerSize - 1, headerSize, headerSize + 1, total - 1}
	for _, withError := range []bool{false, true} {
		for _, boundary := range boundaries {
			name := "short_nil"
			var writeErr error
			if withError {
				name = "write_error"
				writeErr = errors.New("injected write error")
			}
			t.Run(fmt.Sprintf("%s_at_%d", name, boundary), func(t *testing.T) {
				conn := &partialGatherConn{boundary: boundary, err: writeErr}
				session := newGatherSendSession(conn, time.Second)
				loopDone := make(chan error, 1)
				go func() { loopDone <- session.sendLoop() }()
				hdr := header(make([]byte, headerSize))
				hdr.encode(typeData, 0, 1, uint32(len(body)))
				errCh := make(chan error, 1)
				err := session.waitForSendErr(hdr, body, errCh)
				if withError {
					if !errors.Is(err, writeErr) {
						t.Fatalf("wait error=%v", err)
					}
				} else if !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("wait error=%v want ErrShortWrite", err)
				}
				if loopErr := <-loopDone; !errors.Is(loopErr, err) {
					t.Fatalf("loop error=%v wait error=%v", loopErr, err)
				}
				if conn.writes != 1 {
					t.Fatalf("physical writes=%d want=1", conn.writes)
				}
			})
		}
	}
}

func TestSendLoopPreservesPriorityAndDataFINOrder(t *testing.T) {
	conn := newGatherRecordingConn()
	session := newGatherSendSession(conn, time.Second)
	control := header(make([]byte, headerSize))
	control.encode(typeWindowUpdate, 0, 0, AggregateWindowUnitSize)
	data := header(make([]byte, headerSize))
	body := []byte("ordered")
	data.encode(typeData, 0, 1, uint32(len(body)))
	fin := header(make([]byte, headerSize))
	fin.encode(typeWindowUpdate, flagFIN, 1, 0)
	errs := []chan error{make(chan error, 1), make(chan error, 1), make(chan error, 1)}
	session.controlCh <- &sendReady{Hdr: control, Err: errs[0]}
	session.sendCh <- &sendReady{Hdr: data, Body: body, Err: errs[1]}
	session.sendCh <- &sendReady{Hdr: fin, Err: errs[2]}
	go func() { _ = session.sendLoop() }()
	for i, errCh := range errs {
		if err := <-errCh; err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	stopGatherSendLoop(t, session)

	conn.mu.Lock()
	writes := append([][]byte(nil), conn.writes...)
	conn.mu.Unlock()
	if len(writes) != 3 || !bytes.Equal(writes[0], control) || !bytes.Equal(writes[1], append(append([]byte(nil), data...), body...)) || !bytes.Equal(writes[2], fin) {
		t.Fatalf("write order/frames=%x", writes)
	}
}

func TestSendLoopGatheredFrameCapacityIsBounded(t *testing.T) {
	for _, size := range []int{int(AggregateMaxDataFrameSize), 4 << 20} {
		t.Run(fmt.Sprintf("payload_%d", size), func(t *testing.T) {
			conn := newGatherRecordingConn()
			session := newGatherSendSession(conn, time.Second)
			go func() { _ = session.sendLoop() }()
			body := make([]byte, size)
			hdr := header(make([]byte, headerSize))
			hdr.encode(typeData, 0, 1, uint32(size))
			if err := session.waitForSend(hdr, body); err != nil {
				t.Fatal(err)
			}
			stopGatherSendLoop(t, session)
			conn.mu.Lock()
			gotLen, gotCap := len(conn.writes[0]), conn.writeCaps[0]
			conn.mu.Unlock()
			want := headerSize + size
			if gotLen != want || gotCap < want || gotCap > 2*want {
				t.Fatalf("frame len/cap=%d/%d want len=%d cap in [%d,%d]", gotLen, gotCap, want, want, 2*want)
			}
			t.Logf("payload=%d gathered_len=%d gathered_cap=%d", size, gotLen, gotCap)
		})
	}
}

func closedGatherChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestAggregateGatheredDataWriteErrorAbortsAndRetainsCredit(t *testing.T) {
	writeErr := errors.New("injected aggregate DATA write failure")
	config := smallAggregateTestConfig()
	config.ConnectionWriteTimeout = time.Second
	session := &Session{
		config:          config,
		logger:          log.New(io.Discard, "", 0),
		conn:            &partialGatherConn{boundary: headerSize + 1, err: writeErr},
		streams:         make(map[uint32]*Stream),
		inflight:        make(map[uint32]struct{}),
		synCh:           make(chan struct{}, config.AcceptBacklog),
		acceptCh:        make(chan *Stream, config.AcceptBacklog),
		sendCh:          make(chan *sendReady, 8),
		controlCh:       make(chan *sendReady, 8),
		recvDoneCh:      closedGatherChannel(),
		sendDoneCh:      make(chan struct{}),
		pingReplyDoneCh: closedGatherChannel(),
		shutdownCh:      make(chan struct{}),
		aggregate:       newAggregateWindow(config),
	}
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream
	initialSessionCredit := session.aggregate.sendWindow
	initialStreamCredit := stream.allocationSendWindow
	go session.publishAggregateCredit()
	go session.send()

	if n, err := stream.Write([]byte("ambiguous DATA")); n != 0 || !errors.Is(err, writeErr) {
		t.Fatalf("Write n=%d err=%v", n, err)
	}
	select {
	case <-session.shutdownCh:
	case <-time.After(time.Second):
		t.Fatal("aggregate session did not abort after the gathered write failed")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	session.aggregate.mu.Lock()
	gotSessionCredit := session.aggregate.sendWindow
	gotStreamCredit := stream.allocationSendWindow
	session.aggregate.mu.Unlock()
	if want := initialSessionCredit - AggregateWindowUnitSize; gotSessionCredit != want {
		t.Fatalf("session send credit=%d want=%d", gotSessionCredit, want)
	}
	if want := initialStreamCredit - AggregateWindowUnitSize; gotStreamCredit != want {
		t.Fatalf("stream send credit=%d want=%d", gotStreamCredit, want)
	}
}
