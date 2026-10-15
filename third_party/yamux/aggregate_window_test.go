package yamux

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

const (
	testAggregateInitial  = 1 * 1024 * 1024
	testAggregateMaximum  = 4 * 1024 * 1024
	testAllocationInitial = testAggregateInitial / 4
	testAllocationMaximum = testAggregateMaximum / 4
)

func aggregateTestConfig() *Config {
	config := testConfNoKeepAlive()
	config.InitialSessionWindowSize = testAggregateInitial
	config.MaxSessionWindowSize = testAggregateMaximum
	config.MaxDataFrameSize = AggregateMaxDataFrameSize
	config.InitialStreamAllocationWindowSize = testAllocationInitial
	config.MaxStreamAllocationWindowSize = testAllocationMaximum
	return config
}

func smallAggregateTestConfig() *Config {
	config := aggregateTestConfig()
	config.InitialSessionWindowSize = 64 * 1024
	config.MaxSessionWindowSize = 256 * 1024
	config.InitialStreamAllocationWindowSize = 16 * 1024
	config.MaxStreamAllocationWindowSize = 64 * 1024
	return config
}

func TestAggregateConfigIsOptInAndValidated(t *testing.T) {
	if err := VerifyConfig(DefaultConfig()); err != nil {
		t.Fatalf("legacy default config: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"partial": func(c *Config) { c.InitialSessionWindowSize = testAggregateInitial },
		"unaligned": func(c *Config) {
			*c = *aggregateTestConfig()
			c.InitialSessionWindowSize++
		},
		"frame": func(c *Config) {
			*c = *aggregateTestConfig()
			c.MaxDataFrameSize--
		},
		"initial_share": func(c *Config) {
			*c = *aggregateTestConfig()
			c.InitialStreamAllocationWindowSize = testAllocationInitial + AggregateWindowUnitSize
		},
		"maximum_share": func(c *Config) {
			*c = *aggregateTestConfig()
			c.MaxStreamAllocationWindowSize = testAllocationMaximum + AggregateWindowUnitSize
		},
		"protocol_ceiling": func(c *Config) {
			*c = *aggregateTestConfig()
			c.MaxSessionWindowSize = AggregateMaxSessionWindowSize + AggregateWindowUnitSize
		},
		"invalid_incoming_limit": func(c *Config) {
			*c = *aggregateTestConfig()
			c.MaxIncomingStreams = -2
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := DefaultConfig()
			mutate(config)
			if err := VerifyConfig(config); err == nil {
				t.Fatal("VerifyConfig error = nil")
			}
		})
	}
	if err := VerifyConfig(aggregateTestConfig()); err != nil {
		t.Fatalf("aggregate config: %v", err)
	}
}

func TestAggregateIncomingStreamLimitRejectsBeforeObjectAllocation(t *testing.T) {
	for _, limit := range []int{1, -1} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			session := newAggregateUnitSession(nil)
			session.config.MaxIncomingStreams = limit
			session.controlCh = make(chan *sendReady, 2)
			if limit > 0 {
				if err := session.incomingStream(1); err != nil {
					t.Fatal(err)
				}
			}
			rejectedID := uint32(3)
			if limit == -1 {
				rejectedID = 1
			}
			if err := session.incomingStream(rejectedID); err != nil {
				t.Fatalf("bounded rejection: %v", err)
			}
			wantObjects := 0
			if limit > 0 {
				wantObjects = 1
			}
			if got := len(session.streams); got != wantObjects {
				t.Fatalf("stream objects=%d want=%d", got, wantObjects)
			}
			if got := session.incoming; got != wantObjects {
				t.Fatalf("incoming count=%d want=%d", got, wantObjects)
			}
			select {
			case ready := <-session.controlCh:
				hdr := header(ready.Hdr)
				if hdr.StreamID() != rejectedID || hdr.Flags() != flagRST {
					t.Fatalf("rejection frame=%v", hdr)
				}
			default:
				t.Fatal("over-limit SYN did not queue bounded reset")
			}
			if limit > 0 {
				session.closeStream(1)
				if err := session.incomingStream(3); err != nil {
					t.Fatalf("SYN after removal: %v", err)
				}
				if got := session.incoming; got != 1 {
					t.Fatalf("recovered incoming count=%d want=1", got)
				}
			}
		})
	}
}

func TestAggregateChargeAndPoolMemory(t *testing.T) {
	for _, tc := range []struct{ payload, charge uint32 }{
		{0, 0}, {1, 4 * 1024}, {4 * 1024, 4 * 1024}, {4*1024 + 1, 8 * 1024}, {32 * 1024, 32 * 1024},
	} {
		if got := aggregateDataCharge(tc.payload); got != tc.charge {
			t.Fatalf("charge(%d)=%d want=%d", tc.payload, got, tc.charge)
		}
	}
	want := uint64(testAggregateInitial/AggregateWindowUnitSize) * uint64(AggregateWindowUnitSize+aggregateChunkAccounting)
	if got := AggregateReceiveWindowMemory(testAggregateInitial); got != want {
		t.Fatalf("memory=%d want=%d", got, want)
	}
	maxWantUnits := (uint64(^uint32(0)) + uint64(AggregateWindowUnitSize) - 1) / uint64(AggregateWindowUnitSize)
	if got, want := AggregateReceiveWindowMemory(^uint32(0)), maxWantUnits*uint64(AggregateWindowUnitSize+aggregateChunkAccounting); got != want {
		t.Fatalf("max memory=%d want=%d", got, want)
	}
	// Payload buffers are page-multiple allocations. This checks the more
	// expensive repeated one-unit growth case: one descriptor of each kind,
	// plus an explicit allowance for both allocator size-class roundings, must
	// fit inside the per-unit non-payload reservation.
	oneUnitMetadata := uint64(unsafe.Sizeof(aggregateChunk{})) + uint64(unsafe.Sizeof(aggregateSlab{})) + 128
	if uint64(aggregateChunkAccounting) < oneUnitMetadata {
		t.Fatalf("per-unit accounting=%d metadata and rounding bound=%d", aggregateChunkAccounting, oneUnitMetadata)
	}
	// Go large objects are page-rounded after the 32 KiB size-class boundary.
	// Exercise every pool growth size around that boundary. The model below is
	// deliberately pessimistic: an 8 KiB payload page, descriptor storage, one
	// slab object, and 128 bytes of size-class slack per allocation.
	for units := uint64(7); units <= 12; units++ {
		payload := units * uint64(AggregateWindowUnitSize)
		allocatedPayload := payload
		if payload > 32*1024 {
			allocatedPayload = (payload + 8*1024 - 1) / (8 * 1024) * (8 * 1024)
		}
		allocatedBound := allocatedPayload + units*uint64(unsafe.Sizeof(aggregateChunk{})) + uint64(unsafe.Sizeof(aggregateSlab{})) + 256
		reserved := units * uint64(AggregateWindowUnitSize+aggregateChunkAccounting)
		if allocatedBound > reserved {
			t.Fatalf("units=%d allocation bound=%d reservation=%d", units, allocatedBound, reserved)
		}
	}
}

type gatedPayloadReader struct {
	entered chan struct{}
	release chan struct{}
	data    []byte
	once    sync.Once
}

func (r *gatedPayloadReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestAggregateFinalDataBufferedBeforeFINIsPublished(t *testing.T) {
	payload := []byte("final payload")
	reader := &gatedPayloadReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		data:    append([]byte(nil), payload...),
	}
	var releaseOnce sync.Once
	releaseReader := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer releaseReader()
	session := newAggregateUnitSession(nil)
	session.bufRead = bufio.NewReader(reader)
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream

	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, flagFIN, stream.id, uint32(len(payload)))
	handleDone := make(chan error, 1)
	go func() { handleDone <- session.handleAggregateData(hdr) }()
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

func newAggregateUnitSession(body []byte) *Session {
	config := aggregateTestConfig()
	return &Session{
		config:     config,
		logger:     log.New(io.Discard, "", 0),
		bufRead:    bufio.NewReader(bytes.NewReader(body)),
		streams:    make(map[uint32]*Stream),
		inflight:   make(map[uint32]struct{}),
		synCh:      make(chan struct{}, config.AcceptBacklog),
		acceptCh:   make(chan *Stream, config.AcceptBacklog),
		shutdownCh: make(chan struct{}),
		aggregate:  newAggregateWindow(config),
	}
}

func aggregateState(session *Session) (recv uint32, pending uint64, free, total uint32) {
	session.aggregate.mu.Lock()
	defer session.aggregate.mu.Unlock()
	return session.aggregate.recvWindow, session.aggregate.pendingGlobal, session.aggregate.freeUnits, session.aggregate.totalUnits
}

func TestAggregatePartialFrameFreesChunksWithoutPublishingUnreceivedCredit(t *testing.T) {
	const declared = 8 * 1024
	session := newAggregateUnitSession(bytes.Repeat([]byte{0x5a}, 3*1024))
	stream := newStream(session, 1, streamEstablished)
	session.streams[1] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, 1, declared)
	if err := session.handleAggregateData(hdr); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error=%v want unexpected EOF", err)
	}
	recv, pending, free, total := aggregateState(session)
	if recv != testAggregateInitial-declared || pending != 0 || free != total {
		t.Fatalf("recv=%d pending=%d free=%d total=%d", recv, pending, free, total)
	}
	if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
		t.Fatalf("partial frame retained buffered=%d allocated=%d", buffered, allocated)
	}
}

func TestAggregateRSTAndLateDataConservePoolAndReceiverRefunds(t *testing.T) {
	session := newAggregateUnitSession([]byte{1})
	stream := newStream(session, 1, streamEstablished)
	session.streams[1] = stream
	data := header(make([]byte, headerSize))
	data.encode(typeData, 0, 1, 1)
	if err := session.handleAggregateData(data); err != nil {
		t.Fatal(err)
	}
	_, _, free, total := aggregateState(session)
	if free+1 != total {
		t.Fatalf("buffered unit not reflected in pool: free=%d total=%d", free, total)
	}

	session.bufRead = bufio.NewReader(bytes.NewReader([]byte{2}))
	rst := header(make([]byte, headerSize))
	rst.encode(typeData, flagRST, 1, 1)
	if err := session.handleAggregateData(rst); err != nil {
		t.Fatal(err)
	}
	session.bufRead = bufio.NewReader(bytes.NewReader([]byte{3}))
	if err := session.handleAggregateData(data); err != nil {
		t.Fatal(err)
	}
	recv, pending, free, total := aggregateState(session)
	if recv != testAggregateInitial-3*AggregateWindowUnitSize || pending != 3*uint64(AggregateWindowUnitSize) || free != total {
		t.Fatalf("recv=%d pending=%d free=%d total=%d", recv, pending, free, total)
	}
	if _, ok := session.streams[1]; ok {
		t.Fatal("reset stream remained registered")
	}
}

func TestAggregateRejectsMalformedCreditBeforeAllocation(t *testing.T) {
	session := newAggregateUnitSession(nil)
	stream := newStream(session, 1, streamEstablished)
	session.streams[1] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, 1, AggregateMaxDataFrameSize+1)
	if err := session.handleAggregateData(hdr); !errors.Is(err, ErrDataFrameTooLarge) {
		t.Fatalf("oversize error=%v", err)
	}
	session.aggregate.mu.Lock()
	session.aggregate.recvWindow = 0
	session.aggregate.mu.Unlock()
	hdr.encode(typeData, 0, 1, 1)
	if err := session.handleAggregateData(hdr); !errors.Is(err, ErrSessionWindowExceeded) {
		t.Fatalf("overcredit error=%v", err)
	}
	session.aggregate.mu.Lock()
	session.aggregate.recvWindow = testAggregateInitial
	stream.allocationRecvWindow = 0
	session.aggregate.mu.Unlock()
	if err := session.handleAggregateData(hdr); !errors.Is(err, ErrStreamAllocationWindowExceeded) {
		t.Fatalf("stream overcredit error=%v", err)
	}
	_, _, free, total := aggregateState(session)
	if free != total {
		t.Fatalf("rejected frame borrowed chunks: free=%d total=%d", free, total)
	}

	update := header(make([]byte, headerSize))
	update.encode(typeWindowUpdate, 0, 0, 1)
	if err := session.handleAggregateWindowUpdate(update); err == nil {
		t.Fatal("non-unit session refund accepted")
	}
	update.encode(typeAllocationWindowUpdate, flagACK, 1, AggregateWindowUnitSize)
	if err := session.handleAllocationWindowUpdate(update); err == nil {
		t.Fatal("flagged allocation refund accepted")
	}
	session.aggregate.mu.Lock()
	stream.allocationSendWindow = stream.allocationSendLimit
	session.aggregate.mu.Unlock()
	update.encode(typeAllocationWindowUpdate, 0, 1, AggregateWindowUnitSize)
	if err := session.handleAllocationWindowUpdate(update); !errors.Is(err, ErrStreamAllocationWindowExceeded) {
		t.Fatalf("allocation refund overflow error=%v", err)
	}
}

func TestAggregateLateAllocationFramesForRemovedStreamAreIgnored(t *testing.T) {
	session := newAggregateUnitSession(nil)
	update := header(make([]byte, headerSize))
	update.encode(typeAllocationWindowUpdate, 0, 99, AggregateWindowUnitSize)
	if err := session.handleAllocationWindowUpdate(update); err != nil {
		t.Fatalf("late refund: %v", err)
	}
	update.encode(typeWindowGrow, 0, 99, AggregateWindowUnitSize)
	if err := session.handleAggregateWindowGrow(update); err != nil {
		t.Fatalf("late growth: %v", err)
	}
}

func TestAggregateCreditWakeBroadcastDoesNotStrandUsableWriter(t *testing.T) {
	session := newAggregateUnitSession(nil)
	a := newStream(session, 1, streamEstablished)
	b := newStream(session, 3, streamEstablished)
	session.aggregate.mu.Lock()
	session.aggregate.sendWindow = 0
	a.allocationSendWindow = 0
	b.allocationSendWindow = AggregateWindowUnitSize
	session.aggregate.mu.Unlock()
	if n, wakeA := a.reserveAggregateSend(1); n != 0 {
		t.Fatalf("A reserved=%d", n)
	} else {
		if n, wakeB := b.reserveAggregateSend(1); n != 0 {
			t.Fatalf("B reserved=%d", n)
		} else {
			hdr := header(make([]byte, headerSize))
			hdr.encode(typeWindowUpdate, 0, 0, AggregateWindowUnitSize)
			if err := session.handleAggregateWindowUpdate(hdr); err != nil {
				t.Fatal(err)
			}
			select {
			case <-wakeA:
			default:
				t.Fatal("A did not observe broadcast")
			}
			select {
			case <-wakeB:
			default:
				t.Fatal("B did not observe broadcast")
			}
			if n, _ := a.reserveAggregateSend(1); n != 0 {
				t.Fatalf("A reserved without stream credit: %d", n)
			}
			if n, _ := b.reserveAggregateSend(1); n != 1 {
				t.Fatalf("B reserved=%d want=1", n)
			}
		}
	}
}

func TestAggregateZeroDataUsesNoPoolUnit(t *testing.T) {
	session := newAggregateUnitSession(nil)
	stream := newStream(session, 1, streamEstablished)
	session.streams[1] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, 1, 0)
	if err := session.handleAggregateData(hdr); err != nil {
		t.Fatal(err)
	}
	recv, pending, free, total := aggregateState(session)
	if recv != testAggregateInitial || pending != 0 || free != total {
		t.Fatalf("recv=%d pending=%d free=%d total=%d", recv, pending, free, total)
	}
}

func TestAggregateNewFrameTypesRejectedInLegacyMode(t *testing.T) {
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeAllocationWindowUpdate, 0, 1, AggregateWindowUnitSize)
	session := &Session{bufRead: bufio.NewReader(bytes.NewReader(hdr)), recvDoneCh: make(chan struct{})}
	if err := session.recvLoop(); !errors.Is(err, ErrInvalidMsgType) {
		t.Fatalf("legacy recv error=%v", err)
	}
}

func TestAggregateGracefulCloseTracksUnreadUntilReadOrSessionCleanup(t *testing.T) {
	for _, readFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "read", false: "session_cleanup"}[readFirst], func(t *testing.T) {
			session := newAggregateUnitSession(bytes.Repeat([]byte{0x11}, 5000))
			stream := newStream(session, 1, streamLocalClose)
			session.streams[1] = stream
			hdr := header(make([]byte, headerSize))
			hdr.encode(typeData, flagFIN, 1, 5000)
			if err := session.handleAggregateData(hdr); err != nil {
				t.Fatal(err)
			}
			if _, ok := session.streams[1]; ok {
				t.Fatal("both-FIN stream remained active")
			}
			if readFirst {
				got := make([]byte, 5000)
				if _, err := io.ReadFull(stream, got); err != nil {
					t.Fatal(err)
				}
			} else {
				session.closeAggregatePool()
				if session.aggregate.slabs != nil {
					t.Fatal("session cleanup retained pool slabs")
				}
			}
			if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
				t.Fatalf("retained buffered=%d allocated=%d", buffered, allocated)
			}
		})
	}
}

type frameRecordingConn struct {
	io.ReadWriteCloser
	mu      sync.Mutex
	lengths []uint32
}

type priorityRecordingConn struct {
	mu           sync.Mutex
	writes       [][]byte
	first        bool
	firstStarted chan struct{}
	releaseFirst chan struct{}
	wrote        chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
}

func newPriorityRecordingConn() *priorityRecordingConn {
	return &priorityRecordingConn{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		wrote:        make(chan struct{}, 128),
		closed:       make(chan struct{}),
	}
}

func (c *priorityRecordingConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *priorityRecordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	first := !c.first
	if first {
		c.first = true
	}
	c.mu.Unlock()
	if first {
		close(c.firstStarted)
		select {
		case <-c.releaseFirst:
		case <-c.closed:
			return 0, io.ErrClosedPipe
		}
	}
	copyOfP := append([]byte(nil), p...)
	c.mu.Lock()
	c.writes = append(c.writes, copyOfP)
	c.mu.Unlock()
	c.wrote <- struct{}{}
	return len(p), nil
}

func (c *priorityRecordingConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func TestAggregateControlQueuePrecedesBulkBacklog(t *testing.T) {
	conn := newPriorityRecordingConn()
	session := &Session{
		logger:     log.New(io.Discard, "", 0),
		conn:       conn,
		sendCh:     make(chan *sendReady, 8),
		controlCh:  make(chan *sendReady, 8),
		sendDoneCh: make(chan struct{}),
		shutdownCh: make(chan struct{}),
	}
	dataHeader := func(id uint32) header {
		hdr := header(make([]byte, headerSize))
		hdr.encode(typeData, 0, id, 0)
		return hdr
	}
	session.sendCh <- &sendReady{Hdr: dataHeader(1)}
	go session.send()
	<-conn.firstStarted
	for id := uint32(3); id <= 9; id += 2 {
		session.sendCh <- &sendReady{Hdr: dataHeader(id)}
	}
	credit := header(make([]byte, headerSize))
	credit.encode(typeWindowUpdate, 0, 0, AggregateWindowUnitSize)
	session.controlCh <- &sendReady{Hdr: credit}
	close(conn.releaseFirst)
	<-conn.wrote
	<-conn.wrote
	close(session.shutdownCh)
	<-session.sendDoneCh
	_ = conn.Close()

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.writes) < 2 {
		t.Fatalf("writes=%d want at least 2", len(conn.writes))
	}
	if got := header(conn.writes[1]).MsgType(); got != typeWindowUpdate {
		t.Fatalf("second frame type=%d want aggregate credit", got)
	}
}

func TestAggregateReaderControlDoesNotWaitBehindBulkAndFailsClosedWhenFull(t *testing.T) {
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowUpdate, flagRST, 99, 0)
	session := &Session{
		aggregate:  &aggregateWindow{},
		sendCh:     make(chan *sendReady, 1),
		controlCh:  make(chan *sendReady, 1),
		shutdownCh: make(chan struct{}),
	}
	session.sendCh <- &sendReady{}
	if err := session.sendReaderControlNoWait(hdr); err != nil {
		t.Fatalf("reader control behind full bulk queue: %v", err)
	}
	if got := len(session.controlCh); got != 1 {
		t.Fatalf("priority queue depth=%d want=1", got)
	}

	closed := func() chan struct{} {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	conn := newPriorityRecordingConn()
	full := &Session{
		config:          aggregateTestConfig(),
		logger:          log.New(io.Discard, "", 0),
		conn:            conn,
		streams:         make(map[uint32]*Stream),
		aggregate:       &aggregateWindow{publishDone: closed()},
		controlCh:       make(chan *sendReady, 1),
		shutdownCh:      make(chan struct{}),
		recvDoneCh:      closed(),
		sendDoneCh:      closed(),
		pingReplyDoneCh: closed(),
	}
	full.controlCh <- &sendReady{}
	started := time.Now()
	if err := full.sendReaderControlNoWait(hdr); !errors.Is(err, errAggregateControlBacklog) {
		t.Fatalf("full control queue error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("full control queue blocked reader for %s", elapsed)
	}
	select {
	case <-full.shutdownCh:
	case <-time.After(time.Second):
		t.Fatal("full control queue did not fail session closed")
	}
}

func TestAggregateTerminalFramesRemainOrderedAndCloseWakesCreditWriter(t *testing.T) {
	for name, flags := range map[string]uint16{"fin": flagFIN, "rst": flagRST} {
		t.Run(name, func(t *testing.T) {
			hdr := header(make([]byte, headerSize))
			hdr.encode(typeWindowUpdate, flags, 1, 0)
			if isAggregatePriorityControl(hdr, nil) {
				t.Fatal("terminal frame classified as priority control")
			}
		})
	}

	clientConn, serverConn := testConnPipe(t)
	config := smallAggregateTestConfig()
	client, server := testClientServerConfig(t, clientConn, serverConn, config.Clone(), config.Clone())
	clientStream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	serverStream, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x5a}, int(config.InitialStreamAllocationWindowSize))
	if _, err := clientStream.Write(payload); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { _, err := clientStream.Write([]byte{1}); writeDone <- err }()
	select {
	case err := <-writeDone:
		t.Fatalf("credit-exhausted Write returned early: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- clientStream.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake aggregate-credit writer")
	}
	if err := <-writeDone; !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("blocked Write error=%v want stream closed", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(serverStream, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload preceding FIN changed")
	}
	if n, err := serverStream.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("Read after ordered FIN n=%d err=%v", n, err)
	}
}

func TestAggregateWindowUpdateTakesStateBeforeReceiveLock(t *testing.T) {
	session := newAggregateUnitSession(nil)
	stream := newStream(session, 1, streamEstablished)
	atomic.StoreUint32(&stream.maxReceiveWindow, stream.recvWindow)
	// Hold recvLock so sendWindowUpdateMode must expose which lock it takes
	// first. RST processing uses stateLock then recvLock; the update path must
	// use that same order to avoid a cycle with the physical reader.
	stream.recvLock.Lock()
	done := make(chan error, 1)
	go func() { done <- stream.sendWindowUpdateMode(false) }()

	deadline := time.Now().Add(time.Second)
	stateHeld := false
	for time.Now().Before(deadline) {
		if stream.stateLock.TryLock() {
			stream.stateLock.Unlock()
			runtime.Gosched()
			continue
		}
		stateHeld = true
		break
	}
	stream.recvLock.Unlock()
	if !stateHeld {
		t.Fatal("window update waited for recvLock without first holding stateLock")
	}
	if err := <-done; err != nil {
		t.Fatalf("window update: %v", err)
	}
}

func TestAggregateTerminalBeforePayloadReadDiscardsWithoutRetainingChunks(t *testing.T) {
	payload := bytes.Repeat([]byte{0x6d}, 5000)
	session := newAggregateUnitSession(payload)
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream
	charge, err := session.reserveAggregateReceive(stream, uint32(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	// Mirror closeTimeout completing after handleAggregateData's lookup and
	// reservation but before its payload append.
	stream.forceClose()
	stream.discardReceiveBuffer(true)
	session.closeStream(stream.id)
	reader := bytes.NewReader(payload)
	if copied, accepted, err := stream.readAggregateDataPayload(uint32(len(payload)), charge, reader); copied != 0 || accepted || err != nil {
		t.Fatalf("terminal payload copied=%d accepted=%t err=%v", copied, accepted, err)
	}
	if reader.Len() != len(payload) {
		t.Fatalf("terminal stream consumed %d payload bytes before discard", len(payload)-reader.Len())
	}
	if err := session.discardAggregateFrame(stream.id, uint32(len(payload)), charge); err != nil {
		t.Fatal(err)
	}
	_, pending, free, total := aggregateState(session)
	if pending != uint64(charge) || free != total {
		t.Fatalf("after terminal discard pending=%d charge=%d free=%d total=%d", pending, charge, free, total)
	}
	if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
		t.Fatalf("terminal stream retained buffered=%d allocated=%d", buffered, allocated)
	}
}

func TestAggregateDataAfterRemoteFINIsProtocolError(t *testing.T) {
	payload := []byte("late")
	session := newAggregateUnitSession(nil)
	stream := newStream(session, 1, streamRemoteClose)
	session.streams[stream.id] = stream
	charge, err := session.reserveAggregateReceive(stream, uint32(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(payload)
	if copied, accepted, err := stream.readAggregateDataPayload(uint32(len(payload)), charge, reader); copied != 0 || accepted || !errors.Is(err, ErrUnexpectedFlag) {
		t.Fatalf("post-FIN payload copied=%d accepted=%t err=%v", copied, accepted, err)
	}
	if reader.Len() != len(payload) {
		t.Fatalf("post-FIN DATA consumed %d bytes", len(payload)-reader.Len())
	}
	_, _, free, total := aggregateState(session)
	if free != total {
		t.Fatalf("post-FIN DATA borrowed chunks: free=%d total=%d", free, total)
	}
}

func TestAggregateFinalFINCrossingLocalTimeoutDoesNotKillSession(t *testing.T) {
	payload := []byte("final payload crossing timeout")
	reader := &gatedPayloadReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		data:    append([]byte(nil), payload...),
	}
	var releaseOnce sync.Once
	releaseReader := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer releaseReader()
	session := newAggregateUnitSession(nil)
	session.bufRead = bufio.NewReader(reader)
	session.sendCh = make(chan *sendReady, 1)
	stream := newStream(session, 1, streamEstablished)
	session.streams[stream.id] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, flagFIN, stream.id, uint32(len(payload)))
	handleDone := make(chan error, 1)
	go func() { handleDone <- session.handleAggregateData(hdr) }()
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
	_, pending, free, total := aggregateState(session)
	wantCharge := uint64(aggregateDataCharge(uint32(len(payload))))
	if pending != wantCharge || free != total {
		t.Fatalf("crossed timeout pending=%d want=%d free=%d total=%d", pending, wantCharge, free, total)
	}
	if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
		t.Fatalf("crossed timeout retained buffered=%d allocated=%d", buffered, allocated)
	}
}

func TestAggregateByteWindowUpdateRejectsUnknownFlagsAndOverflow(t *testing.T) {
	session := newAggregateUnitSession(nil)
	stream := newStream(session, 1, streamEstablished)

	unknown := header(make([]byte, headerSize))
	unknown.encode(typeWindowUpdate, 1<<15, stream.id, 1)
	before := atomic.LoadUint32(&stream.sendWindow)
	if err := stream.incrSendWindow(unknown, unknown.Flags()); !errors.Is(err, ErrUnexpectedFlag) {
		t.Fatalf("unknown flags error=%v", err)
	}
	if got := atomic.LoadUint32(&stream.sendWindow); got != before {
		t.Fatalf("window changed on unknown flags: got=%d want=%d", got, before)
	}

	atomic.StoreUint32(&stream.sendWindow, math.MaxUint32-1)
	overflow := header(make([]byte, headerSize))
	overflow.encode(typeWindowUpdate, 0, stream.id, 2)
	if err := stream.incrSendWindow(overflow, 0); !errors.Is(err, ErrStreamWindowExceeded) {
		t.Fatalf("overflow error=%v", err)
	}
	if got := atomic.LoadUint32(&stream.sendWindow); got != math.MaxUint32-1 {
		t.Fatalf("window wrapped on overflow: %d", got)
	}
}

func (c *frameRecordingConn) Write(p []byte) (int, error) {
	if len(p) == headerSize {
		hdr := header(p)
		if hdr.Version() == protoVersion && hdr.MsgType() == typeData {
			c.mu.Lock()
			c.lengths = append(c.lengths, hdr.Length())
			c.mu.Unlock()
		}
	}
	return c.ReadWriteCloser.Write(p)
}

func TestAggregateCreditGrowthAndFrameQuantum(t *testing.T) {
	clientConn, serverConn := testConnPipe(t)
	recording := &frameRecordingConn{ReadWriteCloser: clientConn}
	client, server := testClientServerConfig(t, recording, serverConn, smallAggregateTestConfig(), smallAggregateTestConfig())
	clientStream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	serverStream, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}

	if err := server.GrowReceiveWindow(128 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := serverStream.GrowReceiveAllocationWindow(32 * 1024); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x91}, 96*1024)
	writeDone := make(chan error, 1)
	go func() { _, err := clientStream.Write(payload); writeDone <- err }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(serverStream, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload mismatch")
	}
	recording.mu.Lock()
	defer recording.mu.Unlock()
	for i, length := range recording.lengths {
		if length == 0 || length > AggregateMaxDataFrameSize {
			t.Fatalf("DATA frame %d length=%d", i, length)
		}
	}
}

func TestAggregateStalledTinyFrameStreamLeavesCreditForAnother(t *testing.T) {
	for _, tc := range []struct {
		name       string
		session    uint32
		allocation uint32
	}{
		{"one_mib", 1 * 1024 * 1024, 256 * 1024},
		{"app_two_mib", 2 * 1024 * 1024, 512 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := aggregateTestConfig()
			config.InitialSessionWindowSize = tc.session
			config.MaxSessionWindowSize = tc.session
			config.InitialStreamAllocationWindowSize = tc.allocation
			config.MaxStreamAllocationWindowSize = tc.allocation
			clientConn, serverConn := testConnPipe(t)
			client, server := testClientServerConfig(t, clientConn, serverConn, config.Clone(), config.Clone())
			aClient, err := client.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			aServer, err := server.AcceptStream()
			if err != nil {
				t.Fatal(err)
			}
			bClient, err := client.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			bServer, err := server.AcceptStream()
			if err != nil {
				t.Fatal(err)
			}

			frames := int(tc.allocation / AggregateWindowUnitSize)
			var completed atomic.Int32
			aDone := make(chan error, 1)
			go func() {
				for i := 0; i < frames+1; i++ {
					if _, err := aClient.Write([]byte{byte(i)}); err != nil {
						aDone <- err
						return
					}
					completed.Add(1)
				}
				aDone <- nil
			}()
			deadline := time.Now().Add(2 * time.Second)
			for completed.Load() != int32(frames) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := completed.Load(); got != int32(frames) {
				t.Fatalf("tiny frames completed=%d want=%d", got, frames)
			}
			select {
			case err := <-aDone:
				t.Fatalf("stalled stream completed early: %v", err)
			default:
			}

			wantB := bytes.Repeat([]byte{0x42}, int(AggregateMaxDataFrameSize))
			bDone := make(chan error, 1)
			go func() { _, err := bClient.Write(wantB); bDone <- err }()
			gotB := make([]byte, len(wantB))
			if _, err := io.ReadFull(bServer, gotB); err != nil {
				t.Fatal(err)
			}
			if err := <-bDone; err != nil {
				t.Fatalf("second stream failed: %v", err)
			}
			one := make([]byte, 1)
			if _, err := io.ReadFull(aServer, one); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-aDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stalled stream did not resume after one chunk refund")
			}
		})
	}
}

func TestAggregateCreditWaitHonorsWriteDeadline(t *testing.T) {
	clientConn, serverConn := testConnPipe(t)
	config := smallAggregateTestConfig()
	client, server := testClientServerConfig(t, clientConn, serverConn, config.Clone(), config.Clone())
	clientStream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	serverStream, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	defer serverStream.Close()
	if err := clientStream.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	n, err := clientStream.Write(bytes.Repeat([]byte{0x23}, 2*int(config.InitialStreamAllocationWindowSize)))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("write n=%d err=%v want timeout", n, err)
	}
	if n != int(config.InitialStreamAllocationWindowSize) {
		t.Fatalf("write before timeout=%d want=%d", n, config.InitialStreamAllocationWindowSize)
	}
}

func TestAggregateGrowAfterAndConcurrentWithCloseRetainsNoSlabs(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "after", true: "concurrent"}[concurrent], func(t *testing.T) {
			clientConn, serverConn := testConnPipe(t)
			client, server := testClientServerConfig(t, clientConn, serverConn, aggregateTestConfig(), aggregateTestConfig())
			if concurrent {
				growDone := make(chan error, 1)
				go func() { growDone <- client.GrowReceiveWindow(2 * testAggregateInitial) }()
				_ = client.Close()
				err := <-growDone
				if err != nil && !errors.Is(err, ErrSessionShutdown) && !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("concurrent grow error=%v", err)
				}
			} else {
				_ = client.Close()
				if err := client.GrowReceiveWindow(2 * testAggregateInitial); !errors.Is(err, ErrSessionShutdown) {
					t.Fatalf("grow after close error=%v", err)
				}
			}
			client.aggregate.mu.Lock()
			retained := client.aggregate.slabs != nil
			free, total := client.aggregate.freeUnits, client.aggregate.totalUnits
			client.aggregate.mu.Unlock()
			if retained || free != 0 || total != 0 {
				t.Fatalf("retained slabs=%t free=%d total=%d", retained, free, total)
			}
			_ = server.Close()
		})
	}
}
