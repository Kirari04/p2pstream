//go:build p2pstream_qualification

package yamux

const qualificationAggregateSendCountersEnabled = true

// qualificationAggregateSendCounters is accessed with aggregateWindow.mu.
// Counters describe reservations and zero-credit observations; they do not
// imply that a reserved DATA frame was physically delivered.
type qualificationAggregateSendCounters struct {
	reservedDataFrames                uint64
	reservedPayloadBytes              uint64
	reservedAllocationChargedBytes    uint64
	byteCreditZeroObservations        uint64
	streamAllocationZeroObservations  uint64
	sessionAllocationZeroObservations uint64
}

func (c *qualificationAggregateSendCounters) recordReservedData(payload, charged uint32) {
	c.reservedDataFrames++
	c.reservedPayloadBytes += uint64(payload)
	c.reservedAllocationChargedBytes += uint64(charged)
}

func (c *qualificationAggregateSendCounters) recordAllocationZero(sessionZero, streamZero bool) {
	if sessionZero {
		c.sessionAllocationZeroObservations++
	}
	if streamZero {
		c.streamAllocationZeroObservations++
	}
}

func (s *Session) recordQualificationByteCreditZero() {
	if s == nil || s.aggregate == nil {
		return
	}
	s.aggregate.mu.Lock()
	s.aggregate.qualification.byteCreditZeroObservations++
	s.aggregate.mu.Unlock()
}

// QualificationAggregateSendCounters is a destructive, post-transfer
// diagnostic snapshot. Zero-credit fields count observed attempts, may
// overlap, and do not represent unique waits or elapsed duration.
type QualificationAggregateSendCounters struct {
	Enabled                           bool
	ReservedDataFrames                uint64
	ReservedPayloadBytes              uint64
	ReservedAllocationChargedBytes    uint64
	ByteCreditZeroObservations        uint64
	StreamAllocationZeroObservations  uint64
	SessionAllocationZeroObservations uint64
}

// DrainQualificationAggregateSendCounters returns and resets this aggregate
// session's tagged sender counters. Call it once after a transfer, not as a
// live polling API. It takes only the existing aggregate ledger mutex.
func (s *Session) DrainQualificationAggregateSendCounters() QualificationAggregateSendCounters {
	if s == nil || s.aggregate == nil {
		return QualificationAggregateSendCounters{}
	}
	a := s.aggregate
	a.mu.Lock()
	counters := a.qualification
	a.qualification = qualificationAggregateSendCounters{}
	a.mu.Unlock()
	return QualificationAggregateSendCounters{
		Enabled:                           true,
		ReservedDataFrames:                counters.reservedDataFrames,
		ReservedPayloadBytes:              counters.reservedPayloadBytes,
		ReservedAllocationChargedBytes:    counters.reservedAllocationChargedBytes,
		ByteCreditZeroObservations:        counters.byteCreditZeroObservations,
		StreamAllocationZeroObservations:  counters.streamAllocationZeroObservations,
		SessionAllocationZeroObservations: counters.sessionAllocationZeroObservations,
	}
}
