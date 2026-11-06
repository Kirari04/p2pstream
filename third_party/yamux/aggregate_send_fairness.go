package yamux

// The intrusive FIFO needs no scheduler goroutine or per-grant allocation.
// Stream.sendLock permits at most one waiting Write per stream. Only writers
// with byte and stream allocation credit enter; each takes at most one DATA
// frame and then rejoins behind existing contenders on its next write step.
func (a *aggregateWindow) enqueueAggregateSendLocked(s *Stream) {
	if s.aggregateSendQueued {
		return
	}
	s.aggregateSendQueued = true
	s.aggregateSendPrev = a.sendWaitTail
	if a.sendWaitTail == nil {
		a.sendWaitHead = s
	} else {
		a.sendWaitTail.aggregateSendNext = s
	}
	a.sendWaitTail = s
}

func (a *aggregateWindow) removeAggregateSendLocked(s *Stream) {
	if !s.aggregateSendQueued {
		return
	}
	wasHead := s.aggregateSendPrev == nil
	if wasHead {
		a.sendWaitHead = s.aggregateSendNext
	} else {
		s.aggregateSendPrev.aggregateSendNext = s.aggregateSendNext
	}
	if s.aggregateSendNext == nil {
		a.sendWaitTail = s.aggregateSendPrev
	} else {
		s.aggregateSendNext.aggregateSendPrev = s.aggregateSendPrev
	}
	s.aggregateSendQueued = false
	s.aggregateSendPrev = nil
	s.aggregateSendNext = nil
	if wasHead && a.sendWaitHead != nil {
		asyncNotify(a.sendWaitHead.sendNotifyCh)
	}
}

// All exits (deadline, shutdown, reset, Close and successful reservation) remove
// the contender. In particular, a cancelled head cannot strand usable credit.
func (s *Stream) cancelAggregateSendWait() {
	a := s.session.aggregate
	a.mu.Lock()
	a.removeAggregateSendLocked(s)
	a.mu.Unlock()
}
