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
	aggregateRefundBatchStreams  = 16
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
	// qualification is a zero-size type in ordinary builds. Tagged diagnostic
	// builds use it for bounded per-session sender counters.
	qualification qualificationAggregateSendCounters
	protocolMax   uint32
	mu            sync.Mutex

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
	// publishingRefunds retains every selected stream until the physical send
	// loop has either committed its credit or joined during session shutdown.
	// A selected stream can leave both the pending and buffered-owner lists, so
	// the session pool cannot rely on either list to clear an uncommitted batch.
	publishingRefunds      [aggregateRefundBatchStreams]aggregateRefundPart
	publishingRefundsCount uint8

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
		if qualificationAggregateSendCountersEnabled {
			a.qualification.recordReservedData(max, charge)
		}
		a.mu.Unlock()
		return max, nil
	}
	if qualificationAggregateSendCountersEnabled {
		a.qualification.recordAllocationZero(a.sendWindow == 0, s.allocationSendWindow == 0)
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
	for i := uint8(0); i < a.publishingRefundsCount; i++ {
		part := a.publishingRefunds[i]
		if part.stream != nil {
			if part.stream.allocationRefundPublishing >= part.delta {
				part.stream.allocationRefundPublishing -= part.delta
			} else {
				part.stream.allocationRefundPublishing = 0
			}
		}
		a.publishingRefunds[i] = aggregateRefundPart{}
	}
	a.publishingRefundsCount = 0
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
			if work.count == 0 && work.global == 0 {
				break
			}
			if err := s.publishAggregateRefund(work); err != nil {
				if !errors.Is(err, ErrSessionShutdown) {
					s.abortAggregate(err)
				}
				return
			}
		}
	}
}

type aggregateRefundPart struct {
	stream *Stream
	delta  uint64
	slot   uint8
}

type aggregateRefundWork struct {
	parts  [aggregateRefundBatchStreams]aggregateRefundPart
	count  uint8
	global uint64
}

const aggregateMaxWindowUpdate = uint64(math.MaxUint32 - math.MaxUint32%AggregateWindowUnitSize)

// takeAggregateRefundWork chooses stream and session credit under one lock.
// Every ordinary read adds equal stream and global credit under this same
// mutex. Global-only discard credit never adds a stream entry. Selecting a
// bounded set of queued streams first and moving their matching global credit
// atomically therefore prevents a global-only publication from consuming a
// stream's match. A partially selected head remains at the front and ends this
// batch so the same stream never appears twice in one control-lock set.
func (s *Session) takeAggregateRefundWork() aggregateRefundWork {
	a := s.aggregate
	a.mu.Lock()
	defer a.mu.Unlock()
	var work aggregateRefundWork
	remaining := aggregateMaxWindowUpdate
	for work.count < aggregateRefundBatchStreams && remaining > 0 {
		stream := a.pendingStreamHead
		if stream == nil {
			break
		}
		delta := stream.allocationRefundPending
		if delta > remaining {
			delta = remaining
		}
		if delta > a.pendingGlobal {
			delta = a.pendingGlobal
		}
		delta -= delta % uint64(AggregateWindowUnitSize)
		if delta == 0 {
			break
		}
		slot := work.count
		part := aggregateRefundPart{stream: stream, delta: delta, slot: slot}
		work.parts[work.count] = part
		work.count++
		work.global += delta
		remaining -= delta
		stream.allocationRefundPending -= delta
		stream.allocationRefundPublishing += delta
		a.pendingGlobal -= delta
		a.publishingGlobal += delta
		a.publishingRefunds[slot] = part
		a.publishingRefundsCount = work.count
		if stream.allocationRefundPending != 0 {
			break
		}
		a.pendingStreamHead = stream.allocationRefundNext
		if a.pendingStreamHead == nil {
			a.pendingStreamTail = nil
		}
		stream.allocationRefundNext = nil
		stream.allocationRefundQueued = false
	}
	// Only credit beyond all queued stream matches is known to be global-only.
	// If the stream queue remains, preserve the rest for its future batch.
	if a.pendingStreamHead == nil && remaining > 0 {
		delta := a.pendingGlobal
		if delta > remaining {
			delta = remaining
		}
		delta -= delta % uint64(AggregateWindowUnitSize)
		work.global += delta
		a.pendingGlobal -= delta
		a.publishingGlobal += delta
	}
	return work
}

func sortAggregateRefundParts(parts []aggregateRefundPart) {
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		j := i
		for j > 0 && parts[j-1].stream.id > part.stream.id {
			parts[j] = parts[j-1]
			j--
		}
		parts[j] = part
	}
}

func (s *Session) discardPublishingRefundPart(part aggregateRefundPart) {
	a := s.aggregate
	a.mu.Lock()
	if part.stream.allocationRefundPublishing >= part.delta {
		part.stream.allocationRefundPublishing -= part.delta
	} else {
		part.stream.allocationRefundPublishing = 0
	}
	a.publishingRefunds[part.slot] = aggregateRefundPart{}
	a.mu.Unlock()
}

func (s *Session) publishAggregateRefund(work aggregateRefundWork) error {
	a := s.aggregate
	// Snapshot the fixed in-flight ledger into function-local storage before
	// taking stream locks. Some lock-bearing stream operations make their
	// receiver escape; sourcing those pointers from the session ledger keeps
	// the by-value selection work and its fixed array on the caller's stack.
	var lockedStorage [aggregateRefundBatchStreams]aggregateRefundPart
	lockedCount := uint8(0)
	a.mu.Lock()
	for i := uint8(0); i < a.publishingRefundsCount; i++ {
		part := a.publishingRefunds[i]
		if part.stream == nil {
			continue
		}
		lockedStorage[lockedCount] = part
		lockedCount++
	}
	a.mu.Unlock()
	lockedParts := lockedStorage[:lockedCount]
	sortAggregateRefundParts(lockedParts)
	for i := range lockedParts {
		lockedParts[i].stream.controlHdrLock.Lock()
	}
	defer func() {
		for i := len(lockedParts) - 1; i >= 0; i-- {
			lockedParts[i].stream.controlHdrLock.Unlock()
		}
	}()

	var activeParts [aggregateRefundBatchStreams]aggregateRefundPart
	active := uint8(0)
	for i := range lockedParts {
		part := lockedParts[i]
		if !part.stream.acceptsAggregateData() {
			s.discardPublishingRefundPart(part)
			continue
		}
		activeParts[active] = part
		active++
	}
	parts := activeParts[:active]

	a.publishMu.Lock()
	defer a.publishMu.Unlock()
	global := work.global
	headerCount := len(parts)
	if global != 0 {
		headerCount++
	}
	if headerCount == 0 {
		return nil
	}
	batch := header(make([]byte, headerCount*headerSize))
	for i, part := range parts {
		hdr := header(batch[i*headerSize : (i+1)*headerSize])
		hdr.encode(typeAllocationWindowUpdate, 0, part.stream.id, uint32(part.delta))
	}
	if global != 0 {
		hdr := header(batch[(headerCount-1)*headerSize:])
		hdr.encode(typeWindowUpdate, 0, 0, uint32(global))
	}
	errCh := make(chan error, 1)
	return s.waitForAggregateControlBatchErrCommit(batch, errCh, func() {
		a.mu.Lock()
		// The session ledger owns selected parts until this single publisher's
		// queued send commits or shutdown joins it. Reading the fixed ledger here
		// avoids retaining the work and active-parts arrays in the closure.
		for i := uint8(0); i < a.publishingRefundsCount; i++ {
			part := a.publishingRefunds[i]
			if part.stream == nil {
				continue
			}
			part.stream.allocationRefundPublishing -= part.delta
			part.stream.allocationRecvWindow += uint32(part.delta)
			a.publishingRefunds[i] = aggregateRefundPart{}
		}
		a.publishingRefundsCount = 0
		a.publishingGlobal -= global
		a.recvWindow += uint32(global)
		a.mu.Unlock()
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
