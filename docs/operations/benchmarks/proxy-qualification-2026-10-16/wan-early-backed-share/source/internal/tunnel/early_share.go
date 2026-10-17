package tunnel

import (
	"errors"

	"github.com/hashicorp/yamux"
)

const earlyShareThreshold = uint64(512 << 10)
const earlyShareAbsoluteMaximum = uint32(4 << 20)

// BackedEarlyShareState samples immutable backing dimensions while the common
// growth-owner mutex excludes ordinary pool growth and reservation changes.
// Available/free credit is deliberately excluded: reads/refunds can change it.
type BackedEarlyShareState struct {
	ByteWindow        uint32
	AllocationWindow  uint32
	PoolReceiveLimit  uint32
	PoolTotalUnits    uint32
	ReservationLeases int
}

// BackedEarlyShareObservation is a separate diagnostic event. It is never an
// estimator epoch or evidence that the ordinary estimator approved a proposal.
type BackedEarlyShareObservation struct {
	// ThresholdProgressBytes is capped at 512 KiB, not a lifetime byte total.
	ThresholdProgressBytes uint64
	Enabled                bool
	Maximum                uint32
	NegotiatedMaximum      uint32
	Target                 uint32
	Before                 BackedEarlyShareState
	After                  BackedEarlyShareState
	Outcome                string
	PublicationStage       string
	// Error is a fixed outcome category; raw transport/peer error text is excluded.
	Error string
}

// takeEarlyShareAttempt runs only under c.mu, after a successful positive Read.
// The counter saturates; a no-op still consumes the single lifetime attempt.
func (c *GrowingConn) takeEarlyShareAttempt(n uint64) bool {
	if c.earlyShareAttempted {
		return false
	}
	remaining := earlyShareThreshold - c.earlyShareConsumed
	c.earlyShareConsumed += min(n, remaining)
	if c.earlyShareConsumed < earlyShareThreshold {
		return false
	}
	c.earlyShareAttempted = true
	return true
}

func backedEarlyShareTarget(pool, maximum, negotiated uint32) uint32 {
	target := min(earlyShareAbsoluteMaximum, pool/4, maximum, negotiated)
	unit := uint32(yamux.AggregateWindowUnitSize)
	return target / unit * unit
}

// tryBackedReceiveShare NEVER reserves or grows the session pool. Its only
// publication calls are the existing checked stream allocation then byte APIs.
// Caller holds the receive wrapper mutex; lock order matches ordinary Grow.
func (g *AggregateReceiveWindowGrowth) tryBackedReceiveShare(stream *yamux.Stream, maximum uint32, enabled bool) (result BackedEarlyShareObservation, err error) {
	defer func() {
		if err != nil {
			result.Error = result.Outcome
		}
	}()
	result.Enabled, result.Maximum = enabled, maximum
	result.Outcome, result.PublicationStage = "invalid_owner", "none"
	if g == nil || g.session == nil || g.reserve == nil || stream == nil {
		return result, errors.New("aggregate early-share missing owner or stream")
	}
	if stream.Session() != g.session {
		return result, errors.New("aggregate early-share stream belongs to another session")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	snapshot := func() BackedEarlyShareState {
		stats := g.session.AggregateReceiveWindowStats()
		allocation, _ := stream.AggregateReceiveAllocationWindow()
		return BackedEarlyShareState{stream.MaxReceiveWindow(), allocation, stats.ReceiveLimit, stats.TotalUnits, len(g.releases)}
	}
	result.Before = snapshot()
	defer func() { result.After = snapshot() }()
	stats := g.session.AggregateReceiveWindowStats()
	if g.closing || !stats.Enabled || stats.Closed {
		result.Outcome = "owner_closed"
		return result, yamux.ErrSessionShutdown
	}
	negotiated, active := stream.AggregateReceiveAllocationMaximum()
	if !active {
		return result, errors.New("aggregate early-share used on a legacy stream")
	}
	result.NegotiatedMaximum = negotiated
	result.Target = backedEarlyShareTarget(stats.ReceiveLimit, maximum, negotiated)
	if !enabled {
		result.Outcome = "disabled"
		return result, nil
	}
	// Even a target with no delta passes the allocation API's terminal-state
	// check, so a peer-closed stream cannot silently be called an eligible no-op.
	result.PublicationStage = "allocation_attempted"
	if allocationErr := stream.GrowReceiveAllocationWindow(max(result.Target, result.Before.AllocationWindow)); allocationErr != nil {
		afterAllocation, _ := stream.AggregateReceiveAllocationWindow()
		if allocationErr == yamux.ErrStreamClosed && afterAllocation == result.Before.AllocationWindow {
			result.Outcome = "stream_closed"
			return result, nil // Buffered successful Read bytes remain successful.
		}
		result.Outcome = "allocation_error"
		return result, allocationErr
	}
	result.PublicationStage = "allocation_complete"
	if result.Target <= result.Before.AllocationWindow && result.Target <= result.Before.ByteWindow {
		result.Outcome = "no_larger_target"
		return result, nil
	}
	result.PublicationStage = "byte_attempted"
	if byteErr := stream.GrowReceiveWindow(max(result.Target, result.Before.ByteWindow)); byteErr != nil {
		result.Outcome = "byte_error"
		return result, byteErr // Allocation publication cannot be rolled back.
	}
	result.PublicationStage = "byte_complete"
	result.Outcome = "granted"
	return result, nil
}
