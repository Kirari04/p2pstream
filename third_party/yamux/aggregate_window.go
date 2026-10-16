package yamux

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

const (
	AggregateWindowUnitSize   uint32 = 4 * 1024
	AggregateMaxDataFrameSize uint32 = 32 * 1024
	// AggregateMaxSessionWindowSize is the library protocol ceiling. It also
	// keeps a single pool slab addressable on 32-bit platforms.
	AggregateMaxSessionWindowSize uint32 = 1 << 30
	// aggregateChunkAccounting covers the per-unit chunk descriptor, the
	// worst-case one-unit slab descriptor/allocation rounding, and large-object
	// page rounding. Payload storage is allocated separately from descriptors.
	aggregateChunkAccounting     = 1024
	aggregateStreamWindowDivisor = 4
)

type aggregateChunk struct {
	next        *aggregateChunk
	read, write uint16
	buf         []byte
}

// aggregateSlab keeps the payload backing and its chunk descriptors alive.
// Linking slabs avoids an independently growing slice of slab descriptors;
// even repeated one-unit growth remains covered by per-unit accounting.
type aggregateSlab struct {
	next   *aggregateSlab
	data   []byte
	chunks []aggregateChunk
}

// AggregateReceiveWindowMemory returns a conservative inclusive reservation
// for the fixed session chunk pool, including embedded chunk bookkeeping.
func AggregateReceiveWindowMemory(window uint32) uint64 {
	units := (uint64(window) + uint64(AggregateWindowUnitSize) - 1) / uint64(AggregateWindowUnitSize)
	return units * uint64(AggregateWindowUnitSize+aggregateChunkAccounting)
}

func aggregateDataCharge(length uint32) uint32 {
	if length == 0 {
		return 0
	}
	return ((length + AggregateWindowUnitSize - 1) / AggregateWindowUnitSize) * AggregateWindowUnitSize
}

type aggregateWindow struct {
	protocolMax uint32
	mu          sync.Mutex

	sendWindow uint32
	sendLimit  uint32
	// sendNotify is closed and replaced for every credit generation. Closing
	// broadcasts to all competing writers; no writer can consume another's
	// only wakeup. sendNotifyRegistered avoids allocating a replacement for
	// credit updates when no writer observed an exhausted aggregate window.
	sendNotify           chan struct{}
	sendNotifyRegistered bool

	recvWindow       uint32
	recvLimit        uint32
	pendingGlobal    uint64
	publishingGlobal uint64

	free       *aggregateChunk
	freeUnits  uint32
	totalUnits uint32
	slabs      *aggregateSlab
	poolClosed bool

	ownerHead *Stream

	pendingStreamHead *Stream
	pendingStreamTail *Stream

	// publishMu preserves wire order between session growth, stream growth and
	// receiver-only refunds.
	publishMu   sync.Mutex
	publishWake chan struct{}
	publishDone chan struct{}
}

// AggregateReceiveWindowStats is a fixed-cardinality diagnostic snapshot of
// the receiver-owned payload pool. BufferedUnits includes chunks borrowed by
// the physical reader while a DATA frame is being copied.
type AggregateReceiveWindowStats struct {
	Enabled          bool
	Closed           bool
	ReceiveLimit     uint32
	ReceiveAvailable uint32
	TotalUnits       uint32
	FreeUnits        uint32
	BufferedUnits    uint32
	PendingRefund    uint64
}

func (s *Session) AggregateReceiveWindowStats() AggregateReceiveWindowStats {
	if s == nil || s.aggregate == nil {
		return AggregateReceiveWindowStats{}
	}
	a := s.aggregate
	a.mu.Lock()
	stats := AggregateReceiveWindowStats{
		Enabled:          true,
		Closed:           a.poolClosed,
		ReceiveLimit:     a.recvLimit,
		ReceiveAvailable: a.recvWindow,
		TotalUnits:       a.totalUnits,
		FreeUnits:        a.freeUnits,
		PendingRefund:    a.pendingGlobal + a.publishingGlobal,
	}
	if a.totalUnits >= a.freeUnits {
		stats.BufferedUnits = a.totalUnits - a.freeUnits
	}
	a.mu.Unlock()
	return stats
}

// AggregateReceiveAllocationWindow reports this stream's receiver-owned unit
// allowance. The byte receive window is deliberately separate: aggregate
// callers must grow the session pool and this allocation allowance before
// advertising additional byte credit.
func (s *Stream) AggregateReceiveAllocationWindow() (uint32, bool) {
	if s == nil || s.session == nil || s.session.aggregate == nil {
		return 0, false
	}
	a := s.session.aggregate
	a.mu.Lock()
	window := s.allocationRecvLimit
	a.mu.Unlock()
	return window, true
}

// AggregateReceiveAllocationMaximum is the negotiated ceiling for this
// stream's receiver-owned unit allowance.
func (s *Stream) AggregateReceiveAllocationMaximum() (uint32, bool) {
	if s == nil || s.session == nil || s.session.aggregate == nil {
		return 0, false
	}
	return s.session.config.MaxStreamAllocationWindowSize, true
}

func newAggregateWindow(config *Config) *aggregateWindow {
	a := &aggregateWindow{
		protocolMax: config.MaxSessionWindowSize,
		sendWindow:  config.InitialSessionWindowSize,
		sendLimit:   config.InitialSessionWindowSize,
		sendNotify:  make(chan struct{}),
		recvWindow:  config.InitialSessionWindowSize,
		recvLimit:   config.InitialSessionWindowSize,
		publishWake: make(chan struct{}, 1),
		publishDone: make(chan struct{}),
	}
	a.attachSlabLocked(makeAggregateSlab(config.InitialSessionWindowSize))
	return a
}

func makeAggregateSlab(delta uint32) *aggregateSlab {
	units := delta / AggregateWindowUnitSize
	if units == 0 {
		return nil
	}
	slab := &aggregateSlab{
		data:   make([]byte, int(units)*int(AggregateWindowUnitSize)),
		chunks: make([]aggregateChunk, units),
	}
	for i := range slab.chunks {
		start := i * int(AggregateWindowUnitSize)
		slab.chunks[i].buf = slab.data[start : start+int(AggregateWindowUnitSize)]
		if i+1 < len(slab.chunks) {
			slab.chunks[i].next = &slab.chunks[i+1]
		}
	}
	return slab
}

// attachSlabLocked performs only O(1) pointer/counter updates. Allocation,
// zeroing, buffer slicing, and chunk-chain construction happen before taking
// the shared aggregate ledger lock.
func (a *aggregateWindow) attachSlabLocked(slab *aggregateSlab) {
	if slab == nil {
		return
	}
	units := uint32(len(slab.chunks))
	slab.chunks[len(slab.chunks)-1].next = a.free
	a.free = &slab.chunks[0]
	slab.next = a.slabs
	a.slabs = slab
	a.freeUnits += units
	a.totalUnits += units
}

func (s *Session) aggregateEnabled() bool { return s != nil && s.aggregate != nil }

func (s *Stream) reserveAggregateSend(max uint32) (uint32, <-chan struct{}) {
	a := s.session.aggregate
	if a == nil {
		return max, nil
	}
	if max > AggregateMaxDataFrameSize {
		max = AggregateMaxDataFrameSize
	}
	a.mu.Lock()
	available := a.sendWindow
	if s.allocationSendWindow < available {
		available = s.allocationSendWindow
	}
	if max > available {
		max = available
	}
	charge := aggregateDataCharge(max)
	if charge > available {
		max = available - available%AggregateWindowUnitSize
		charge = max
	}
	if max > 0 {
		a.sendWindow -= charge
		s.allocationSendWindow -= charge
		a.mu.Unlock()
		return max, nil
	}
	a.sendNotifyRegistered = true
	wake := a.sendNotify
	a.mu.Unlock()
	return 0, wake
}

func (a *aggregateWindow) broadcastSendLocked() {
	if !a.sendNotifyRegistered {
		return
	}
	close(a.sendNotify)
	a.sendNotify = make(chan struct{})
	a.sendNotifyRegistered = false
}

func (s *Session) handleAggregateWindowUpdate(hdr header) error {
	a := s.aggregate
	if a == nil || hdr.Flags() != 0 || hdr.StreamID() != 0 || hdr.Length() == 0 || hdr.Length()%AggregateWindowUnitSize != 0 {
		return fmt.Errorf("yamux: invalid aggregate window update")
	}
	a.mu.Lock()
	if hdr.Length() > a.sendLimit-a.sendWindow {
		a.mu.Unlock()
		return ErrSessionWindowExceeded
	}
	a.sendWindow += hdr.Length()
	a.broadcastSendLocked()
	a.mu.Unlock()
	return nil
}

func (s *Session) handleAllocationWindowUpdate(hdr header) error {
	if s.aggregate == nil || hdr.Flags() != 0 || hdr.StreamID() == 0 || hdr.Length() == 0 || hdr.Length()%AggregateWindowUnitSize != 0 {
		return fmt.Errorf("yamux: invalid stream allocation window update")
	}
	s.streamLock.Lock()
	stream := s.streams[hdr.StreamID()]
	s.streamLock.Unlock()
	if stream == nil {
		return nil
	}
	a := s.aggregate
	a.mu.Lock()
	if hdr.Length() > stream.allocationSendLimit-stream.allocationSendWindow {
		a.mu.Unlock()
		return ErrStreamAllocationWindowExceeded
	}
	stream.allocationSendWindow += hdr.Length()
	a.broadcastSendLocked()
	a.mu.Unlock()
	return nil
}

func (s *Session) handleAggregateWindowGrow(hdr header) error {
	if s.aggregate == nil || hdr.Flags() != 0 || hdr.Length() == 0 || hdr.Length()%AggregateWindowUnitSize != 0 {
		return fmt.Errorf("yamux: invalid aggregate window growth")
	}
	a := s.aggregate
	if hdr.StreamID() == 0 {
		a.mu.Lock()
		if hdr.Length() > a.protocolMax-a.sendLimit {
			a.mu.Unlock()
			return ErrSessionWindowExceeded
		}
		a.sendLimit += hdr.Length()
		a.sendWindow += hdr.Length()
		a.broadcastSendLocked()
		a.mu.Unlock()
		return nil
	}

	s.streamLock.Lock()
	stream := s.streams[hdr.StreamID()]
	s.streamLock.Unlock()
	if stream == nil {
		return nil
	}
	a.mu.Lock()
	newLimit := uint64(stream.allocationSendLimit) + uint64(hdr.Length())
	if newLimit > uint64(s.config.MaxStreamAllocationWindowSize) || newLimit > uint64(a.sendLimit/aggregateStreamWindowDivisor) {
		a.mu.Unlock()
		return ErrStreamAllocationWindowExceeded
	}
	stream.allocationSendLimit = uint32(newLimit)
	stream.allocationSendWindow += hdr.Length()
	a.broadcastSendLocked()
	a.mu.Unlock()
	return nil
}

func (s *Session) reserveAggregateReceive(stream *Stream, length uint32) (uint32, error) {
	if length > AggregateMaxDataFrameSize {
		return 0, ErrDataFrameTooLarge
	}
	charge := aggregateDataCharge(length)
	if charge == 0 {
		return 0, nil
	}
	a := s.aggregate
	a.mu.Lock()
	defer a.mu.Unlock()
	if charge > a.recvWindow {
		return 0, ErrSessionWindowExceeded
	}
	if stream != nil && charge > stream.allocationRecvWindow {
		return 0, ErrStreamAllocationWindowExceeded
	}
	a.recvWindow -= charge
	if stream != nil {
		stream.allocationRecvWindow -= charge
	}
	return charge, nil
}

func (s *Session) restoreAggregateReceive(stream *Stream, charge uint32) {
	if charge == 0 || s.aggregate == nil {
		return
	}
	a := s.aggregate
	a.mu.Lock()
	a.recvWindow += charge
	if stream != nil {
		stream.allocationRecvWindow += charge
	}
	a.mu.Unlock()
}

func (s *Session) borrowAggregateChunks(charge uint32) (head, tail *aggregateChunk, units uint32, err error) {
	units = charge / AggregateWindowUnitSize
	a := s.aggregate
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.poolClosed || units > a.freeUnits {
		return nil, nil, 0, fmt.Errorf("yamux: aggregate chunk pool exhausted")
	}
	for i := uint32(0); i < units; i++ {
		chunk := a.free
		a.free = chunk.next
		chunk.next = nil
		chunk.read, chunk.write = 0, 0
		if tail == nil {
			head = chunk
		} else {
			tail.next = chunk
		}
		tail = chunk
	}
	a.freeUnits -= units
	return head, tail, units, nil
}

func (s *Session) returnAggregateChunks(stream *Stream, head, tail *aggregateChunk, units uint32, refund, refundStream bool) {
	if head == nil || units == 0 || s.aggregate == nil {
		return
	}
	a := s.aggregate
	a.mu.Lock()
	if a.poolClosed {
		a.mu.Unlock()
		return
	}
	tail.next = a.free
	a.free = head
	a.freeUnits += units
	if refund {
		delta := uint64(units) * uint64(AggregateWindowUnitSize)
		a.pendingGlobal += delta
		if refundStream && stream != nil {
			stream.allocationRefundPending += delta
			if !stream.allocationRefundQueued {
				stream.allocationRefundQueued = true
				if a.pendingStreamTail == nil {
					a.pendingStreamHead = stream
				} else {
					a.pendingStreamTail.allocationRefundNext = stream
				}
				a.pendingStreamTail = stream
			}
		}
	}
	a.mu.Unlock()
	if refund {
		asyncNotify(a.publishWake)
	}
}

func (s *Session) refundDiscardedAggregate(charge uint32) {
	if charge == 0 || s.aggregate == nil || s.IsClosed() {
		return
	}
	a := s.aggregate
	a.mu.Lock()
	if !a.poolClosed {
		a.pendingGlobal += uint64(charge)
	}
	a.mu.Unlock()
	asyncNotify(a.publishWake)
}

func (s *Session) trackAggregateBuffer(stream *Stream) {
	a := s.aggregate
	if a == nil {
		return
	}
	a.mu.Lock()
	if !stream.aggregateBufferTracked && !a.poolClosed {
		stream.aggregateBufferTracked = true
		stream.aggregateOwnerNext = a.ownerHead
		if a.ownerHead != nil {
			a.ownerHead.aggregateOwnerPrev = stream
		}
		a.ownerHead = stream
	}
	a.mu.Unlock()
}

func (s *Session) untrackAggregateBuffer(stream *Stream) {
	a := s.aggregate
	if a == nil {
		return
	}
	a.mu.Lock()
	if stream.aggregateBufferTracked {
		if stream.aggregateOwnerPrev == nil {
			a.ownerHead = stream.aggregateOwnerNext
		} else {
			stream.aggregateOwnerPrev.aggregateOwnerNext = stream.aggregateOwnerNext
		}
		if stream.aggregateOwnerNext != nil {
			stream.aggregateOwnerNext.aggregateOwnerPrev = stream.aggregateOwnerPrev
		}
		stream.aggregateOwnerPrev, stream.aggregateOwnerNext = nil, nil
		stream.aggregateBufferTracked = false
	}
	a.mu.Unlock()
}

func (s *Session) closeAggregatePool() {
	a := s.aggregate
	if a == nil {
		return
	}
	a.publishMu.Lock()
	defer a.publishMu.Unlock()
	a.mu.Lock()
	a.poolClosed = true
	owners := a.ownerHead
	a.ownerHead = nil
	pendingStreams := a.pendingStreamHead
	a.pendingStreamHead, a.pendingStreamTail = nil, nil
	for stream := pendingStreams; stream != nil; stream = stream.allocationRefundNext {
		stream.allocationRefundQueued = false
		stream.allocationRefundPending = 0
		stream.allocationRefundPublishing = 0
	}
	for stream := owners; stream != nil; stream = stream.aggregateOwnerNext {
		stream.aggregateOwnerPrev = nil
		stream.aggregateBufferTracked = false
	}
	a.mu.Unlock()
	for stream := owners; stream != nil; {
		next := stream.aggregateOwnerNext
		stream.aggregateOwnerNext = nil
		stream.discardReceiveBuffer(false)
		stream = next
	}
	a.mu.Lock()
	a.free = nil
	a.freeUnits, a.totalUnits = 0, 0
	a.slabs = nil
	// The publisher has joined before pool cleanup. Unpublished receiver-only
	// refunds can no longer reach the peer and carry no meaning after shutdown;
	// clear them with the session-owned pool so diagnostics conserve to zero.
	a.pendingGlobal, a.publishingGlobal = 0, 0
	a.mu.Unlock()
	for stream := pendingStreams; stream != nil; {
		next := stream.allocationRefundNext
		stream.allocationRefundNext = nil
		stream = next
	}
}

func (s *Session) publishAggregateCredit() {
	a := s.aggregate
	defer close(a.publishDone)
	for {
		select {
		case <-a.publishWake:
		case <-s.shutdownCh:
			return
		}
		for {
			work := s.takeAggregateRefundWork()
			if work.stream != nil {
				if err := s.publishStreamAllocationRefund(work); err != nil {
					if !errors.Is(err, ErrSessionShutdown) {
						s.abortAggregate(err)
					}
					return
				}
				continue
			}
			if work.global == 0 {
				break
			}
			if err := s.publishGlobalRefund(work.global); err != nil {
				if !errors.Is(err, ErrSessionShutdown) {
					s.abortAggregate(err)
				}
				return
			}
		}
	}
}

type aggregateRefundWork struct {
	stream      *Stream
	streamDelta uint64
	global      uint64
}

// takeAggregateRefundWork chooses stream and session credit under one lock.
// Every ordinary read adds equal stream and global credit under this same
// mutex. Global-only discard credit never adds a stream entry. Selecting a
// queued stream first and moving both counters atomically therefore prevents a
// concurrent global-only publication from consuming its matching credit.
func (s *Session) takeAggregateRefundWork() aggregateRefundWork {
	a := s.aggregate
	a.mu.Lock()
	defer a.mu.Unlock()
	stream := a.pendingStreamHead
	if stream != nil {
		streamDelta := stream.allocationRefundPending
		if streamDelta > math.MaxUint32 {
			streamDelta = math.MaxUint32 - math.MaxUint32%uint64(AggregateWindowUnitSize)
		}
		globalDelta := streamDelta
		if globalDelta > a.pendingGlobal {
			globalDelta = a.pendingGlobal
		}
		globalDelta -= globalDelta % uint64(AggregateWindowUnitSize)
		if streamDelta == 0 {
			return aggregateRefundWork{}
		}
		stream.allocationRefundPending -= streamDelta
		stream.allocationRefundPublishing += streamDelta
		a.pendingGlobal -= globalDelta
		a.publishingGlobal += globalDelta
		if stream.allocationRefundPending == 0 {
			a.pendingStreamHead = stream.allocationRefundNext
			if a.pendingStreamHead == nil {
				a.pendingStreamTail = nil
			}
			stream.allocationRefundNext = nil
			stream.allocationRefundQueued = false
		}
		return aggregateRefundWork{stream: stream, streamDelta: streamDelta, global: globalDelta}
	}
	delta := a.pendingGlobal
	if delta > math.MaxUint32 {
		delta = math.MaxUint32 - math.MaxUint32%uint64(AggregateWindowUnitSize)
	}
	if delta == 0 {
		return aggregateRefundWork{}
	}
	a.pendingGlobal -= delta
	a.publishingGlobal += delta
	return aggregateRefundWork{global: delta}
}

func (s *Session) publishStreamAllocationRefund(work aggregateRefundWork) error {
	stream, delta := work.stream, work.streamDelta
	stream.controlHdrLock.Lock()
	defer stream.controlHdrLock.Unlock()
	if !stream.acceptsAggregateData() {
		s.aggregate.mu.Lock()
		stream.allocationRefundPublishing -= delta
		s.aggregate.mu.Unlock()
		if work.global == 0 {
			return nil
		}
		return s.publishGlobalRefund(work.global)
	}
	s.aggregate.publishMu.Lock()
	defer s.aggregate.publishMu.Unlock()
	streamHdr := header(make([]byte, headerSize))
	streamHdr.encode(typeAllocationWindowUpdate, 0, stream.id, uint32(delta))
	errCh := make(chan error, 1)
	if work.global == 0 {
		return s.waitForSendErrCommit(streamHdr, nil, errCh, func() {
			s.aggregate.mu.Lock()
			stream.allocationRefundPublishing -= delta
			stream.allocationRecvWindow += uint32(delta)
			s.aggregate.mu.Unlock()
		})
	}
	globalHdr := header(make([]byte, headerSize))
	globalHdr.encode(typeWindowUpdate, 0, 0, uint32(work.global))
	return s.waitForAggregateControlBatchErrCommit(streamHdr, globalHdr, errCh, func() {
		s.aggregate.mu.Lock()
		stream.allocationRefundPublishing -= delta
		stream.allocationRecvWindow += uint32(delta)
		s.aggregate.publishingGlobal -= work.global
		s.aggregate.recvWindow += uint32(work.global)
		s.aggregate.mu.Unlock()
	})
}

func (s *Session) publishGlobalRefund(delta uint64) error {
	s.aggregate.publishMu.Lock()
	defer s.aggregate.publishMu.Unlock()
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowUpdate, 0, 0, uint32(delta))
	errCh := make(chan error, 1)
	return s.waitForSendErrCommit(hdr, nil, errCh, func() {
		s.aggregate.mu.Lock()
		s.aggregate.publishingGlobal -= delta
		s.aggregate.recvWindow += uint32(delta)
		s.aggregate.mu.Unlock()
	})
}

func (s *Session) abortAggregate(err error) {
	s.shutdownErrLock.Lock()
	if s.shutdownErr == nil {
		s.shutdownErr = err
	}
	s.shutdownErrLock.Unlock()
	_ = s.conn.Close()
	go s.Close()
}

// GrowReceiveWindow grows the aggregate session pool. The caller reserves the
// returned-memory delta before calling and retains it for the session lifetime.
func (s *Session) GrowReceiveWindow(window uint32) error {
	a := s.aggregate
	if a == nil {
		return fmt.Errorf("yamux: aggregate session flow control is disabled")
	}
	if window%AggregateWindowUnitSize != 0 || window > a.protocolMax {
		return fmt.Errorf("yamux: invalid aggregate receive window %d", window)
	}
	a.publishMu.Lock()
	defer a.publishMu.Unlock()
	a.mu.Lock()
	if a.poolClosed || s.IsClosed() {
		a.mu.Unlock()
		return ErrSessionShutdown
	}
	if window <= a.recvLimit {
		a.mu.Unlock()
		return nil
	}
	delta := window - a.recvLimit
	a.mu.Unlock()
	// publishMu excludes pool cleanup, while the second shutdown check below
	// handles Close beginning during this potentially expensive allocation.
	slab := makeAggregateSlab(delta)
	a.mu.Lock()
	if a.poolClosed || s.IsClosed() {
		a.mu.Unlock()
		return ErrSessionShutdown
	}
	a.attachSlabLocked(slab)
	a.recvLimit = window
	a.publishingGlobal += uint64(delta)
	a.mu.Unlock()
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowGrow, 0, 0, delta)
	errCh := make(chan error, 1)
	if err := s.waitForSendErrCommit(hdr, nil, errCh, func() {
		a.mu.Lock()
		a.publishingGlobal -= uint64(delta)
		a.recvWindow += delta
		a.mu.Unlock()
	}); err != nil {
		s.abortAggregate(err)
		return err
	}
	return nil
}

// GrowReceiveAllocationWindow increases this stream's share of the aggregate
// pool. Grow the session first; the target is capped at one quarter of it.
func (s *Stream) GrowReceiveAllocationWindow(window uint32) error {
	a := s.session.aggregate
	if a == nil {
		return fmt.Errorf("yamux: aggregate session flow control is disabled")
	}
	if window%AggregateWindowUnitSize != 0 || window > s.session.config.MaxStreamAllocationWindowSize {
		return fmt.Errorf("yamux: invalid stream allocation receive window %d", window)
	}
	s.controlHdrLock.Lock()
	defer s.controlHdrLock.Unlock()
	if !s.acceptsAggregateData() {
		return ErrStreamClosed
	}
	a.publishMu.Lock()
	defer a.publishMu.Unlock()
	a.mu.Lock()
	if a.poolClosed || s.session.IsClosed() {
		a.mu.Unlock()
		return ErrSessionShutdown
	}
	if window <= s.allocationRecvLimit {
		a.mu.Unlock()
		return nil
	}
	if window > a.recvLimit/aggregateStreamWindowDivisor {
		a.mu.Unlock()
		return fmt.Errorf("yamux: stream allocation window %d exceeds session fairness share %d", window, a.recvLimit/aggregateStreamWindowDivisor)
	}
	delta := window - s.allocationRecvLimit
	s.allocationRecvLimit = window
	s.allocationRefundPublishing += uint64(delta)
	a.mu.Unlock()
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowGrow, 0, s.id, delta)
	errCh := make(chan error, 1)
	if err := s.session.waitForSendErrCommit(hdr, nil, errCh, func() {
		a.mu.Lock()
		s.allocationRefundPublishing -= uint64(delta)
		s.allocationRecvWindow += delta
		a.mu.Unlock()
	}); err != nil {
		s.session.abortAggregate(err)
		return err
	}
	return nil
}

func (s *Stream) acceptsAggregateData() bool {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	switch s.state {
	case streamInit, streamSYNSent, streamSYNReceived, streamEstablished, streamLocalClose:
		return true
	default:
		return false
	}
}

func readAggregateFrame(r io.Reader, head *aggregateChunk, length uint32) (uint32, error) {
	remaining := length
	var copied uint32
	for chunk := head; chunk != nil && remaining > 0; chunk = chunk.next {
		want := remaining
		if want > AggregateWindowUnitSize {
			want = AggregateWindowUnitSize
		}
		n, err := io.ReadFull(r, chunk.buf[:want])
		chunk.write = uint16(n)
		copied += uint32(n)
		remaining -= uint32(n)
		if err != nil {
			return copied, err
		}
	}
	if remaining != 0 {
		return copied, io.ErrUnexpectedEOF
	}
	return copied, nil
}
