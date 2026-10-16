package yamux

import (
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type streamState int

const (
	streamInit streamState = iota
	streamSYNSent
	streamSYNReceived
	streamEstablished
	streamLocalClose
	streamRemoteClose
	streamClosed
	streamReset
)

// Stream is used to represent a logical stream
// within a session.
type Stream struct {
	recvWindow       uint32
	sendWindow       uint32
	maxReceiveWindow uint32

	id       uint32
	session  *Session
	incoming bool

	state     streamState
	stateLock sync.Mutex

	recvBuf  *recvBuffer
	recvLock sync.Mutex

	aggregateHead     *aggregateChunk
	aggregateTail     *aggregateChunk
	aggregateBuffered int
	aggregateUnits    uint32

	allocationSendWindow uint32
	allocationSendLimit  uint32
	allocationRecvWindow uint32
	allocationRecvLimit  uint32

	allocationRefundPending    uint64
	allocationRefundPublishing uint64
	allocationRefundQueued     bool
	allocationRefundNext       *Stream

	aggregateBufferTracked bool
	aggregateOwnerPrev     *Stream
	aggregateOwnerNext     *Stream

	controlHdr     header
	controlErr     chan error
	controlHdrLock sync.Mutex

	sendHdr  header
	sendErr  chan error
	sendLock sync.Mutex

	recvNotifyCh chan struct{}
	sendNotifyCh chan struct{}

	readDeadline  atomic.Value // time.Time
	writeDeadline atomic.Value // time.Time

	// establishCh is notified if the stream is established or being closed.
	establishCh chan struct{}

	// closeTimer is set with stateLock held to honor the StreamCloseTimeout
	// setting on Session.
	closeTimer *time.Timer
}

// newStream is used to construct a new stream within
// a given session for an ID
func newStream(session *Session, id uint32, state streamState) *Stream {
	s := &Stream{
		id:               id,
		session:          session,
		state:            state,
		controlHdr:       header(make([]byte, headerSize)),
		controlErr:       make(chan error, 1),
		sendHdr:          header(make([]byte, headerSize)),
		sendErr:          make(chan error, 1),
		recvWindow:       initialStreamWindow,
		maxReceiveWindow: session.config.MaxStreamWindowSize,
		sendWindow:       initialStreamWindow,
		recvNotifyCh:     make(chan struct{}, 1),
		sendNotifyCh:     make(chan struct{}, 1),
		establishCh:      make(chan struct{}, 1),
	}
	s.readDeadline.Store(time.Time{})
	s.writeDeadline.Store(time.Time{})
	if session.aggregateEnabled() {
		s.allocationSendWindow = session.config.InitialStreamAllocationWindowSize
		s.allocationSendLimit = session.config.InitialStreamAllocationWindowSize
		s.allocationRecvWindow = session.config.InitialStreamAllocationWindowSize
		s.allocationRecvLimit = session.config.InitialStreamAllocationWindowSize
	}
	return s
}

// Session returns the associated stream session
func (s *Stream) Session() *Session {
	return s.session
}

// StreamID returns the ID of this stream
func (s *Stream) StreamID() uint32 {
	return s.id
}

// Read is used to read from the stream
func (s *Stream) Read(b []byte) (n int, err error) {
	defer asyncNotify(s.recvNotifyCh)
	var drainedHead, drainedTail *aggregateChunk
	var drainedUnits uint32
	var refundAllocation bool
START:
	refundAllocation = false

	// If the stream is closed and there's no data buffered, return EOF
	s.stateLock.Lock()
	switch s.state {
	case streamInit, streamSYNSent, streamSYNReceived, streamEstablished:
		refundAllocation = true
	case streamLocalClose:
		// LocalClose only prohibits further local writes. Handle reads normally.
		refundAllocation = true
	case streamRemoteClose:
		fallthrough
	case streamClosed:
		s.recvLock.Lock()
		if s.receiveBufferedLocked() == 0 {
			s.recvLock.Unlock()
			s.stateLock.Unlock()
			return 0, io.EOF
		}
		s.recvLock.Unlock()
	case streamReset:
		s.stateLock.Unlock()
		return 0, ErrConnectionReset
	}
	s.stateLock.Unlock()

	// If there is no data available, block
	s.recvLock.Lock()
	if s.receiveBufferedLocked() == 0 {
		s.recvLock.Unlock()
		goto WAIT
	}

	if s.session.aggregateEnabled() {
		n, drainedHead, drainedTail, drainedUnits = s.readAggregateLocked(b)
		if drainedUnits > 0 {
			s.session.returnAggregateChunks(s, drainedHead, drainedTail, drainedUnits, true, refundAllocation)
			drainedHead, drainedTail, drainedUnits = nil, nil, 0
		}
		if s.aggregateBuffered == 0 {
			s.session.untrackAggregateBuffer(s)
		}
	} else {
		n, _ = s.recvBuf.Read(b)
	}
	s.recvLock.Unlock()

	// Send a window update potentially
	err = s.sendWindowUpdate()
	if err == ErrSessionShutdown {
		err = nil
	}
	return n, err

WAIT:
	var timeout <-chan time.Time
	var timer *time.Timer
	readDeadline := s.readDeadline.Load().(time.Time)
	if !readDeadline.IsZero() {
		delay := time.Until(readDeadline)
		timer = time.NewTimer(delay)
		timeout = timer.C
	}
	select {
	case <-s.session.shutdownCh:
	case <-s.recvNotifyCh:
	case <-timeout:
		return 0, ErrTimeout
	}
	if timer != nil {
		if !timer.Stop() {
			<-timeout
		}
	}
	goto START
}

func (s *Stream) receiveBufferedLocked() int {
	if s.session.aggregateEnabled() {
		return s.aggregateBuffered
	}
	if s.recvBuf == nil {
		return 0
	}
	return s.recvBuf.Len()
}

func (s *Stream) readAggregateLocked(p []byte) (n int, drainedHead, drainedTail *aggregateChunk, drainedUnits uint32) {
	for len(p) > 0 && s.aggregateHead != nil {
		chunk := s.aggregateHead
		copied := copy(p, chunk.buf[chunk.read:chunk.write])
		chunk.read += uint16(copied)
		s.aggregateBuffered -= copied
		n += copied
		p = p[copied:]
		if chunk.read != chunk.write {
			break
		}
		s.aggregateHead = chunk.next
		chunk.next = nil
		if drainedTail == nil {
			drainedHead = chunk
		} else {
			drainedTail.next = chunk
		}
		drainedTail = chunk
		drainedUnits++
		s.aggregateUnits--
	}
	if s.aggregateHead == nil {
		s.aggregateTail = nil
	}
	return n, drainedHead, drainedTail, drainedUnits
}

// Write is used to write to the stream
func (s *Stream) Write(b []byte) (n int, err error) {
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	total := 0
	for total < len(b) {
		n, err := s.write(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// write is used to write to the stream, may return on
// a short write.
func (s *Stream) write(b []byte) (n int, err error) {
	var flags uint16
	var max uint32
	var body []byte
	var aggregateNotify <-chan struct{}
START:
	aggregateNotify = nil
	s.stateLock.Lock()
	switch s.state {
	case streamLocalClose:
		fallthrough
	case streamClosed:
		s.stateLock.Unlock()
		return 0, ErrStreamClosed
	case streamReset:
		s.stateLock.Unlock()
		return 0, ErrConnectionReset
	}
	s.stateLock.Unlock()

	// If there is no data available, block
	window := atomic.LoadUint32(&s.sendWindow)
	if window == 0 {
		goto WAIT
	}

	// Send up to our send window
	max = min(window, uint32(len(b)))
	if s.session.aggregateEnabled() {
		max, aggregateNotify = s.reserveAggregateSend(max)
		if max == 0 {
			goto WAIT
		}
	}
	// Determine flags only after all DATA credit is reserved. Otherwise a
	// blocked first write could consume SYN without putting it on the wire.
	flags = s.sendFlags()
	body = b[:max]

	// Send the header
	s.sendHdr.encode(typeData, flags, s.id, max)
	if err = s.session.waitForSendErr(s.sendHdr, body, s.sendErr); err != nil {
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrConnectionWriteTimeout) {
			// Message left in ready queue, header re-use is unsafe.
			s.sendHdr = header(make([]byte, headerSize))
		}
		if s.session.aggregateEnabled() {
			// A timeout can race a queued write. Session credit cannot safely be
			// restored without knowing whether the peer observed the frame.
			s.session.abortAggregate(err)
		}
		return 0, err
	}

	// Reduce our send window
	atomic.AddUint32(&s.sendWindow, ^uint32(max-1))

	// Unlock
	return int(max), err

WAIT:
	var timeout <-chan time.Time
	var timer *time.Timer
	writeDeadline := s.writeDeadline.Load().(time.Time)
	if !writeDeadline.IsZero() {
		delay := time.Until(writeDeadline)
		timer = time.NewTimer(delay)
		timeout = timer.C
	}
	select {
	case <-s.session.shutdownCh:
	case <-s.sendNotifyCh:
	case <-aggregateNotify:
	case <-timeout:
		return 0, ErrTimeout
	}
	if timer != nil {
		if !timer.Stop() {
			<-timeout
		}
	}
	goto START
}

// sendFlags determines any flags that are appropriate
// based on the current stream state
func (s *Stream) sendFlags() uint16 {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	return s.sendFlagsLocked()
}

// sendFlagsLocked returns pending establishment flags. The caller holds
// stateLock; callers that also inspect receive storage must acquire stateLock
// before recvLock, matching processFlags, Read, and reset cleanup.
func (s *Stream) sendFlagsLocked() uint16 {
	var flags uint16
	switch s.state {
	case streamInit:
		flags |= flagSYN
		s.state = streamSYNSent
	case streamSYNReceived:
		flags |= flagACK
		s.state = streamEstablished
	}
	return flags
}

// sendWindowUpdate potentially sends a window update enabling
// further writes to take place. Normal reads batch updates smaller than a
// quarter of the receive window.
func (s *Stream) sendWindowUpdate() error {
	return s.sendWindowUpdateMode(false)
}

// sendWindowUpdateForced publishes a positive receive-credit change
// immediately. Explicit receive-window growth must not depend on a later
// application read to cross the normal quarter-window batching threshold.
func (s *Stream) sendWindowUpdateForced() error {
	return s.sendWindowUpdateMode(true)
}

func (s *Stream) sendWindowUpdateMode(force bool) error {
	s.controlHdrLock.Lock()
	defer s.controlHdrLock.Unlock()

	// Determine the delta update
	max := atomic.LoadUint32(&s.maxReceiveWindow)
	var bufLen uint32
	s.stateLock.Lock()
	s.recvLock.Lock()
	if s.session.aggregateEnabled() {
		bufLen = uint32(s.aggregateBuffered)
	} else if s.recvBuf != nil {
		bufLen = uint32(s.recvBuf.Len())
	}
	delta := (max - bufLen) - s.recvWindow

	// Determine the flags if any
	flags := s.sendFlagsLocked()

	// A concurrent update may have published the growth while this caller was
	// waiting for controlHdrLock. Do not emit a spurious zero-credit frame.
	if delta == 0 && flags == 0 {
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return nil
	}

	// Check if we can omit the update
	if !force && delta < (max/4) && flags == 0 {
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return nil
	}

	// Update our window
	s.recvWindow += delta
	s.recvLock.Unlock()
	s.stateLock.Unlock()

	// Send the header
	s.controlHdr.encode(typeWindowUpdate, flags, s.id, delta)
	if err := s.session.waitForSendErr(s.controlHdr, nil, s.controlErr); err != nil {
		if errors.Is(err, ErrConnectionWriteTimeout) {
			// Receive credit and SYN state are already committed. A queued
			// update may still be sent, so this session cannot safely continue.
			// Close the wire before returning; join the session asynchronously
			// because a blocked sender can still own this stream's locks.
			s.session.shutdownErrLock.Lock()
			if s.session.shutdownErr == nil {
				s.session.shutdownErr = err
			}
			s.session.shutdownErrLock.Unlock()
			_ = s.session.conn.Close()
			go s.session.Close()
		}
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrConnectionWriteTimeout) {
			// Message left in ready queue, header re-use is unsafe.
			s.controlHdr = header(make([]byte, headerSize))
		}
		return err
	}
	return nil
}

// MaxReceiveWindow returns this stream's current receive-credit limit.
func (s *Stream) MaxReceiveWindow() uint32 {
	return atomic.LoadUint32(&s.maxReceiveWindow)
}

// ReceiveBufferStats reports the logical bytes currently buffered and the
// accounted receive storage retained by this stream. Both values are sampled
// while holding recvLock, so the pair describes one consistent point in time.
func (s *Stream) ReceiveBufferStats() (bufferedBytes, allocatedBytes uint64) {
	s.recvLock.Lock()
	if s.session.aggregateEnabled() {
		bufferedBytes = uint64(s.aggregateBuffered)
		allocatedBytes = uint64(s.aggregateUnits) * uint64(AggregateWindowUnitSize+aggregateChunkAccounting)
	} else if s.recvBuf != nil {
		bufferedBytes = uint64(s.recvBuf.Buffered())
		allocatedBytes = uint64(s.recvBuf.AllocatedCapacity())
	}
	s.recvLock.Unlock()
	return bufferedBytes, allocatedBytes
}

// GrowReceiveWindow increases this stream's receive credit. The caller must
// reserve the additional memory before calling and retain that reservation
// until the stream has fully closed. Credit can never be revoked once sent.
// This does not change the wire protocol or other streams' receive windows.
func (s *Stream) GrowReceiveWindow(window uint32) error {
	for {
		current := atomic.LoadUint32(&s.maxReceiveWindow)
		if window <= current {
			return nil
		}
		if atomic.CompareAndSwapUint32(&s.maxReceiveWindow, current, window) {
			return s.sendWindowUpdateForced()
		}
	}
}

// sendClose is used to send a FIN
func (s *Stream) sendClose() error {
	s.controlHdrLock.Lock()
	defer s.controlHdrLock.Unlock()

	flags := s.sendFlags()
	flags |= flagFIN
	s.controlHdr.encode(typeWindowUpdate, flags, s.id, 0)
	if err := s.session.waitForSendErr(s.controlHdr, nil, s.controlErr); err != nil {
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrConnectionWriteTimeout) {
			// Message left in ready queue, header re-use is unsafe.
			s.controlHdr = header(make([]byte, headerSize))
		}
		return err
	}
	return nil
}

// Close is used to close the stream
func (s *Stream) Close() error {
	closeStream := false
	s.stateLock.Lock()
	switch s.state {
	// Opened means we need to signal a close
	case streamSYNSent:
		fallthrough
	case streamSYNReceived:
		fallthrough
	case streamEstablished:
		s.state = streamLocalClose
		goto SEND_CLOSE

	case streamLocalClose:
	case streamRemoteClose:
		s.state = streamClosed
		closeStream = true
		goto SEND_CLOSE

	case streamClosed:
	case streamReset:
	default:
		panic("unhandled state")
	}
	s.stateLock.Unlock()
	return nil
SEND_CLOSE:
	// Aggregate DATA and terminal controls share the ordered FIFO. Wake a
	// writer blocked on credit, then wait for any active Write to leave that
	// FIFO before enqueuing FIN. This prevents DATA from crossing FIN while
	// preserving Close's ability to cancel a credit-blocked Write.
	if s.session.aggregateEnabled() {
		s.stateLock.Unlock()
		s.notifyWaiting()
		s.sendLock.Lock()
		defer s.sendLock.Unlock()
		s.stateLock.Lock()
		if s.state == streamReset {
			s.stateLock.Unlock()
			return nil
		}
		if s.state == streamClosed {
			closeStream = true
		}
	}
	// This shouldn't happen (the more realistic scenario to cancel the
	// timer is via processFlags) but just in case this ever happens, we
	// cancel the timer to prevent dangling timers.
	if s.closeTimer != nil {
		s.closeTimer.Stop()
		s.closeTimer = nil
	}

	// If we have a StreamCloseTimeout set we start the timeout timer.
	// We do this only if we're not already closing the stream since that
	// means this was a graceful close.
	//
	// This prevents memory leaks if one side (this side) closes and the
	// remote side poorly behaves and never responds with a FIN to complete
	// the close. After the specified timeout, we clean our resources up no
	// matter what.
	if !closeStream && s.session.config.StreamCloseTimeout > 0 {
		s.closeTimer = time.AfterFunc(
			s.session.config.StreamCloseTimeout, s.closeTimeout)
	}

	s.stateLock.Unlock()
	s.sendClose()
	s.notifyWaiting()
	if closeStream {
		s.session.closeStream(s.id)
	}
	return nil
}

// Reset aborts the stream and releases its receive storage immediately. It is
// serialized with local writes, but the peer is allowed to discard unread
// DATA on RST. Unlike Close it does not retain the stream until a peer FIN or
// close timeout.
func (s *Stream) Reset() error {
	s.sendLock.Lock()
	defer s.sendLock.Unlock()

	s.stateLock.Lock()
	if s.state == streamReset || s.state == streamClosed {
		// A terminal state may have been published by closeTimeout or session
		// shutdown while the physical reader still owns recvLock. Wait for that
		// accepted payload and discard it before Reset promises its caller that
		// receive storage is fenced.
		s.discardReceiveBuffer(true)
		s.stateLock.Unlock()
		return nil
	}
	s.state = streamReset
	if s.closeTimer != nil {
		s.closeTimer.Stop()
		s.closeTimer = nil
	}
	s.discardReceiveBuffer(true)
	s.stateLock.Unlock()
	s.notifyWaiting()
	s.session.closeStream(s.id)

	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowUpdate, flagRST, s.id, 0)
	return s.session.sendNoWait(hdr)
}

// closeTimeout is called after StreamCloseTimeout during a close to
// close this stream.
func (s *Stream) closeTimeout() {
	// Close our side forcibly
	s.forceClose()
	s.discardReceiveBuffer(true)

	// Free the stream from the session map
	s.session.closeStream(s.id)

	// Send a RST so the remote side closes too.
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowUpdate, flagRST, s.id, 0)
	_ = s.session.sendNoWait(hdr)
}

// forceClose is used for when the session is exiting
func (s *Stream) forceClose() {
	s.stateLock.Lock()
	s.state = streamClosed
	s.stateLock.Unlock()
	s.notifyWaiting()
}

// processFlags is used to update the state of the stream
// based on set flags, if any. Lock must be held
func (s *Stream) processFlags(flags uint16) error {
	return s.processFlagsMode(flags, false)
}

// processFinalFINAfterPayload tolerates a local timeout/reset that crossed the
// physical read of an already-accepted final frame. Genuine duplicate FIN is
// rejected in streamRemoteClose before payload acceptance.
func (s *Stream) processFinalFINAfterPayload() error {
	return s.processFlagsMode(flagFIN, true)
}

func (s *Stream) processFlagsMode(flags uint16, allowCrossedTerminalFIN bool) error {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	// Close the stream without holding the state lock
	closeStream := false
	defer func() {
		if closeStream {
			if s.closeTimer != nil {
				// Stop our close timeout timer since we gracefully closed
				s.closeTimer.Stop()
			}

			s.session.closeStream(s.id)
		}
	}()

	if flags&flagACK == flagACK {
		if s.state == streamSYNSent {
			s.state = streamEstablished
		}
		asyncNotify(s.establishCh)
		s.session.establishStream(s.id)
	}
	if flags&flagFIN == flagFIN {
		switch s.state {
		case streamSYNSent:
			fallthrough
		case streamSYNReceived:
			fallthrough
		case streamEstablished:
			s.state = streamRemoteClose
			s.notifyWaiting()
		case streamLocalClose:
			s.state = streamClosed
			closeStream = true
			s.notifyWaiting()
		case streamClosed, streamReset:
			if !allowCrossedTerminalFIN {
				s.session.logger.Printf("[ERR] yamux: unexpected FIN flag in state %d", s.state)
				return ErrUnexpectedFlag
			}
		default:
			s.session.logger.Printf("[ERR] yamux: unexpected FIN flag in state %d", s.state)
			return ErrUnexpectedFlag
		}
	}
	if flags&flagRST == flagRST {
		s.state = streamReset
		closeStream = true
		s.discardReceiveBuffer(true)
		s.notifyWaiting()
	}
	return nil
}

// discardReceiveBuffer releases buffered receive storage exactly once. In an
// active aggregate session, discarded payload is returned only through the
// receiver's session WINDOW_UPDATE worker.
func (s *Stream) discardReceiveBuffer(refund bool) uint32 {
	s.recvLock.Lock()
	var discarded uint32
	var aggregateHead, aggregateTail *aggregateChunk
	var aggregateUnits uint32
	if s.session.aggregateEnabled() {
		discarded = uint32(s.aggregateBuffered)
		aggregateHead, aggregateTail, aggregateUnits = s.aggregateHead, s.aggregateTail, s.aggregateUnits
		s.aggregateHead, s.aggregateTail = nil, nil
		s.aggregateBuffered, s.aggregateUnits = 0, 0
		if aggregateUnits > 0 {
			s.session.returnAggregateChunks(s, aggregateHead, aggregateTail, aggregateUnits, refund, false)
			aggregateHead, aggregateTail, aggregateUnits = nil, nil, 0
		}
		s.session.untrackAggregateBuffer(s)
	} else if s.recvBuf != nil {
		discarded = uint32(s.recvBuf.Len())
		s.recvBuf = nil
	}
	s.recvLock.Unlock()
	return discarded
}

// notifyWaiting notifies all the waiting channels
func (s *Stream) notifyWaiting() {
	asyncNotify(s.recvNotifyCh)
	asyncNotify(s.sendNotifyCh)
	asyncNotify(s.establishCh)
}

// incrSendWindow updates the size of our send window
func (s *Stream) incrSendWindow(hdr header, flags uint16) error {
	if s.session.aggregateEnabled() && flags&^uint16(flagSYN|flagACK|flagFIN|flagRST) != 0 {
		return ErrUnexpectedFlag
	}
	if err := s.processFlags(flags); err != nil {
		return err
	}

	// Increase window, unblock a sender
	if s.session.aggregateEnabled() {
		delta := hdr.Length()
		for {
			current := atomic.LoadUint32(&s.sendWindow)
			if delta > math.MaxUint32-current {
				return ErrStreamWindowExceeded
			}
			if atomic.CompareAndSwapUint32(&s.sendWindow, current, current+delta) {
				break
			}
		}
	} else {
		atomic.AddUint32(&s.sendWindow, hdr.Length())
	}
	asyncNotify(s.sendNotifyCh)
	return nil
}

// readData is used to handle a data frame
func (s *Stream) readData(hdr header, flags uint16, conn io.Reader) error {
	// RST discards its payload. Applying it first fences concurrent local
	// cleanup before the physical reader drains attacker-controlled bytes.
	if flags&flagRST != 0 {
		if err := s.processFlags(flags); err != nil {
			return err
		}
		_, accepted, err := s.readDataPayload(hdr.Length(), conn)
		if err != nil {
			return err
		}
		if !accepted {
			return s.discardDataPayload(hdr.Length(), conn)
		}
		return nil
	}
	// FIN becomes visible to application Read only after its final payload is
	// buffered. ACK/SYN state may safely publish before the physical read.
	if err := s.processFlags(flags &^ flagFIN); err != nil {
		return err
	}
	_, accepted, err := s.readDataPayload(hdr.Length(), conn)
	if err != nil {
		return err
	}
	if !accepted {
		return s.discardDataPayload(hdr.Length(), conn)
	}
	if flags&flagFIN != 0 {
		if err := s.processFinalFINAfterPayload(); err != nil {
			return err
		}
	}
	asyncNotify(s.recvNotifyCh)
	return nil
}

func (s *Stream) readDataPayload(length uint32, conn io.Reader) (uint32, bool, error) {
	// Recheck terminal state after the session map lookup while preserving the
	// global state -> receive lock order. Reset/timeout cleanup that starts
	// after this point waits for recvLock and discards any appended bytes before
	// returning the stream's lifetime resource lease.
	s.stateLock.Lock()
	s.recvLock.Lock()
	if length > s.recvWindow {
		s.session.logger.Printf("[ERR] yamux: receive window exceeded (stream: %d, remain: %d, recv: %d)", s.id, s.recvWindow, length)
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return 0, true, ErrRecvWindowExceeded
	}
	switch s.state {
	case streamInit, streamSYNSent, streamSYNReceived, streamEstablished, streamLocalClose:
		s.stateLock.Unlock()
	case streamRemoteClose:
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return 0, false, ErrUnexpectedFlag
	case streamClosed, streamReset:
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return 0, false, nil
	default:
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return 0, false, ErrUnexpectedFlag
	}
	if length == 0 {
		s.recvLock.Unlock()
		return 0, true, nil
	}

	if s.recvBuf == nil {
		s.recvBuf = &recvBuffer{}
	}
	copiedLength, err := s.recvBuf.readFrom(conn, int64(length))
	// Account for bytes that arrived even when the underlying frame is
	// truncated. The session will terminate on the error, but retaining this
	// invariant makes direct Stream users observe accurate receive credit.
	s.recvWindow -= uint32(copiedLength)
	if err != nil {
		s.session.logger.Printf("[ERR] yamux: Failed to read stream data: %v", err)
		s.recvLock.Unlock()
		return uint32(copiedLength), true, err
	}

	s.recvLock.Unlock()
	return uint32(copiedLength), true, nil
}

func (s *Stream) discardDataPayload(length uint32, conn io.Reader) error {
	if length == 0 {
		return nil
	}
	if _, err := io.CopyN(io.Discard, conn, int64(length)); err != nil {
		s.session.logger.Printf("[ERR] yamux: Failed to discard terminal stream data: %v", err)
		return err
	}
	return nil
}

func (s *Stream) readAggregateDataPayload(length, charge uint32, conn io.Reader) (uint32, bool, error) {
	if length == 0 {
		return 0, true, nil
	}
	// Recheck terminal state after the session lookup/reservation while using
	// the global state -> receive lock order. Release stateLock before the
	// physical read; a timeout may then mark the stream closed, but its discard
	// waits for recvLock and releases any just-appended chunks exactly once.
	s.stateLock.Lock()
	s.recvLock.Lock()
	switch s.state {
	case streamInit, streamSYNSent, streamSYNReceived, streamEstablished, streamLocalClose:
		s.stateLock.Unlock()
	case streamRemoteClose:
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return 0, false, ErrUnexpectedFlag
	case streamClosed, streamReset:
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return 0, false, nil
	default:
		s.recvLock.Unlock()
		s.stateLock.Unlock()
		return 0, false, ErrUnexpectedFlag
	}
	head, tail, units, err := s.session.borrowAggregateChunks(charge)
	if err != nil {
		s.recvLock.Unlock()
		return 0, true, err
	}
	if length > s.recvWindow {
		s.recvLock.Unlock()
		s.session.returnAggregateChunks(s, head, tail, units, false, false)
		return 0, true, ErrRecvWindowExceeded
	}
	copied, err := readAggregateFrame(conn, head, length)
	s.recvWindow -= copied
	if err != nil {
		s.recvLock.Unlock()
		s.session.returnAggregateChunks(s, head, tail, units, false, false)
		return copied, true, err
	}
	if s.aggregateTail == nil {
		s.aggregateHead = head
	} else {
		s.aggregateTail.next = head
	}
	s.aggregateTail = tail
	s.aggregateBuffered += int(length)
	s.aggregateUnits += units
	s.session.trackAggregateBuffer(s)
	s.recvLock.Unlock()
	return copied, true, nil
}

// SetDeadline sets the read and write deadlines
func (s *Stream) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	if err := s.SetWriteDeadline(t); err != nil {
		return err
	}
	return nil
}

// SetReadDeadline sets the deadline for blocked and future Read calls.
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.readDeadline.Store(t)
	asyncNotify(s.recvNotifyCh)
	return nil
}

// SetWriteDeadline sets the deadline for blocked and future Write calls
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline.Store(t)
	asyncNotify(s.sendNotifyCh)
	return nil
}

// Shrink is used to compact the amount of buffers utilized
// This is useful when using Yamux in a connection pool to reduce
// the idle memory utilization.
func (s *Stream) Shrink() {
	if s.session.aggregateEnabled() {
		return
	}
	s.recvLock.Lock()
	if s.recvBuf != nil && s.recvBuf.Len() == 0 {
		// This also drops the bounded reusable spare chunks, returning an idle
		// stream's receive storage to zero.
		s.recvBuf = nil
	}
	s.recvLock.Unlock()
}
