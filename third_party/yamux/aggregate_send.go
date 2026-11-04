package yamux

import (
	"sync/atomic"
	"time"
)

// The accepted state transfers frame ownership, not physical completion.
// Only rejected sends can refund credit or retry their establishment flags.
type aggregateSendOutcome uint8

const (
	aggregateSendRejected aggregateSendOutcome = iota
	aggregateSendAccepted
	aggregateSendCompleted
)

// sendAggregateData is called with sendLock and allocation credit reserved.
func (s *Stream) sendAggregateData(body []byte) (aggregateSendOutcome, error) {
	session, a := s.session, s.session.aggregate
	timer := time.NewTimer(session.config.ConnectionWriteTimeout)
	defer timer.Stop()
	select {
	case a.sendSlots <- struct{}{}:
	case <-session.shutdownCh:
		return aggregateSendRejected, ErrSessionShutdown
	case <-timer.C:
		return aggregateSendRejected, ErrConnectionWriteTimeout
	}
	accepted := false
	defer func() {
		if !accepted {
			<-a.sendSlots
		}
	}()
	// Controls that publish establishment flags cannot cross the enqueue
	// decision. No state/receive ledger lock is held while awaiting queue space.
	s.controlHdrLock.Lock()
	a.sendOwnedMu.RLock()
	if session.IsClosed() {
		a.sendOwnedMu.RUnlock()
		s.controlHdrLock.Unlock()
		return aggregateSendRejected, ErrSessionShutdown
	}
	select {
	case <-session.shutdownCh:
		a.sendOwnedMu.RUnlock()
		s.controlHdrLock.Unlock()
		return aggregateSendRejected, ErrSessionShutdown
	case <-timer.C:
		a.sendOwnedMu.RUnlock()
		s.controlHdrLock.Unlock()
		return aggregateSendRejected, ErrConnectionWriteTimeout
	default:
	}
	frame := make(header, headerSize+len(body))
	copy(frame[headerSize:], body)
	ready := &sendReady{Hdr: frame, Err: make(chan error, 1), ownedData: true}
	// The worker must not observe the header until acceptance commits flags and
	// byte credit. The caller's bytes have already been detached into the frame.
	ready.mu.Lock()
	var err error
	select {
	case session.sendCh <- ready:
		accepted = true
		s.stateLock.Lock()
		flags := s.sendFlagsLocked()
		s.stateLock.Unlock()
		frame[:headerSize].encode(typeData, flags, s.id, uint32(len(body)))
		atomic.AddUint32(&s.sendWindow, ^uint32(len(body)-1))
	case <-session.shutdownCh:
		err = ErrSessionShutdown
	case <-timer.C:
		err = ErrConnectionWriteTimeout
	}
	if !accepted {
		ready.Hdr = nil
	}
	frame = nil
	ready.mu.Unlock()
	a.sendOwnedMu.RUnlock()
	s.controlHdrLock.Unlock()
	if !accepted {
		return aggregateSendRejected, err
	}
	return session.waitAggregateSendCompletion(ready, timer.C)
}

func (session *Session) waitAggregateSendCompletion(ready *sendReady, timeout <-chan time.Time) (aggregateSendOutcome, error) {
	var err error
	select {
	case err = <-ready.Err:
		return aggregateSendCompleted, err
	case <-session.shutdownCh:
		err = ErrSessionShutdown
	case <-timeout:
		err = ErrConnectionWriteTimeout
	}
	// Prefer a completion already published at the deadline/shutdown boundary.
	select {
	case err = <-ready.Err:
		return aggregateSendCompleted, err
	default:
		return aggregateSendAccepted, err
	}
}

func (s *Session) releaseAggregateSend(ready *sendReady) {
	if ready.ownedData {
		ready.Hdr = nil
		<-s.aggregate.sendSlots
	}
}

// Close joins the sender before clearing accepted frames. The enqueue mutex
// fences submitters that observed shutdown concurrently with queue acceptance.
func (s *Session) closeAggregateSends() {
	a := s.aggregate
	a.sendOwnedMu.Lock()
	defer a.sendOwnedMu.Unlock()
	for {
		select {
		case ready := <-s.sendCh:
			s.releaseAggregateSend(ready)
		default:
			return
		}
	}
}
