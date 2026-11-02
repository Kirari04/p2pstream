package yamux

import "time"

const DefaultAggregateReceivePressureTimeout = time.Second
const aggregatePressureBatch = 16

// One session worker, no stream workers and no unbounded retirement queue.
// A marked stream waits in this fixed array until its prior DATA writer leaves
// sendLock and an ordered RST can be queued. Never block the monitor on I/O.
func (s *Session) monitorAggregatePressure() {
	a := s.aggregate
	defer close(a.pressureDone)
	timeout := s.config.AggregateReceivePressureTimeout
	if timeout == 0 {
		timeout = DefaultAggregateReceivePressureTimeout
	}
	ticker := time.NewTicker(timeout / 4)
	defer ticker.Stop()
	var pending [aggregatePressureBatch]*Stream
	for {
		select {
		case <-s.shutdownCh:
			return
		case now := <-ticker.C:
			for i, stream := range pending {
				if stream == nil || !stream.sendLock.TryLock() {
					continue
				}
				hdr := header(make([]byte, headerSize))
				hdr.encode(typeWindowUpdate, flagRST, stream.id, 0)
				select {
				case s.sendCh <- &sendReady{Hdr: hdr}:
					pending[i] = nil
				default:
				}
				stream.sendLock.Unlock()
			}
			var candidates [aggregatePressureBatch]*Stream
			available := 0
			for _, stream := range pending {
				if stream == nil {
					available++
				}
			}
			n := 0
			a.mu.Lock()
			for stream := a.ownerHead; stream != nil; stream = stream.aggregateOwnerNext {
				units := stream.pressureUnits
				stream.sampleAggregateProgressLocked(now, timeout)
				if a.freeUnits <= a.totalUnits/4 && units > 0 && stream.pressureStalled && n < available {
					candidates[n] = stream
					n++
				}
			}
			a.mu.Unlock()
			for _, stream := range candidates[:n] {
				if !stream.evictAggregatePressure() {
					continue
				}
				for i := range pending {
					if pending[i] == nil {
						pending[i] = stream
						break
					}
				}
			}
		}
	}
}

func (s *Stream) evictAggregatePressure() bool {
	// All receive paths use state -> recv -> aggregate. Try-locks keep a partial
	// physical frame or a concurrent Read/Reset from blocking this worker.
	if !s.stateLock.TryLock() {
		return false
	}
	defer s.stateLock.Unlock()
	if !s.recvLock.TryLock() {
		return false
	}
	a := s.session.aggregate
	a.mu.Lock()
	eligible := !a.poolClosed && a.freeUnits <= a.totalUnits/4 && s.aggregateBufferTracked && s.pressureUnits > 0 &&
		s.pressureStalled && s.pressureReadUnits < s.pressureReadTarget
	if eligible {
		a.pressureResets++
	}
	a.mu.Unlock()
	if !eligible {
		s.recvLock.Unlock()
		return false
	}
	s.pressureErr = ErrReceivePressure
	s.state = streamReset
	if s.closeTimer != nil {
		s.closeTimer.Stop()
		s.closeTimer = nil
	}
	s.discardReceiveBufferLocked(true)
	s.recvLock.Unlock()
	s.notifyWaiting()
	s.session.closeStream(s.id)
	return true
}

// Require one quarter of the stream allowance per interval, capped at 128KiB.
// Large historical growth must not manufacture a huge minimum transfer rate.
// Small windows still require at least one completely consumed allocation unit.
func (s *Stream) aggregateProgressTargetLocked() uint32 {
	units := s.allocationRecvLimit / AggregateWindowUnitSize / 4
	if units < 1 {
		return 1
	}
	if units > 32 {
		return 32
	}
	return units
}

// Called only for chunks actually consumed by Read, not discard/reset/Close or
// publisher accounting. Saturation avoids overflow and prevents banking an
// arbitrarily fast burst for future intervals. Caller holds aggregate.mu.
func (s *Stream) recordAggregateConsumptionLocked(units uint32) {
	target := s.pressureReadTarget
	if target == 0 {
		return
	}
	if s.pressureReadUnits >= target || units >= target-s.pressureReadUnits {
		s.pressureReadUnits = target
	} else {
		s.pressureReadUnits += units
	}
	if s.pressureReadUnits == target {
		s.pressureStalled = false
	}
}

func (s *Stream) sampleAggregateProgressLocked(now time.Time, interval time.Duration) {
	if now.Sub(s.pressureSince) < interval {
		return
	}
	s.pressureStalled = s.pressureReadUnits < s.pressureReadTarget
	s.pressureSince = now
	s.pressureReadUnits = 0
	s.pressureReadTarget = s.aggregateProgressTargetLocked()
}
