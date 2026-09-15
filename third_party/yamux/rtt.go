package yamux

import "time"

const (
	// RTT is used for flow-control sizing, so an old path measurement must not
	// keep increasing a stream's window forever after the path has disappeared.
	rttSampleStaleAfter = 30 * time.Second
	// Keep one minimum per wall-clock second.  This bounds the estimator while
	// allowing the lower envelope to age out over the full sample lifetime even
	// when keepalive is configured more frequently than once per second.
	rttHistoryBuckets     = 32
	rttHistoryBucketWidth = time.Second

	// A successful Ping should be much shorter than this in normal operation.
	// Values above it are normally a stalled or queued control frame, and must
	// not become an unbounded flow-control multiplier.
	rttMaximumSample = 5 * time.Second
	rttMinimumSample = time.Microsecond
)

type rttHistorySample struct {
	value time.Duration
	at    time.Time
}

// rttEstimator is deliberately small and clock-independent. Keeping the
// clock as an argument makes aging and outlier behavior deterministic in unit
// tests and avoids adding a timer or goroutine per stream.
type rttEstimator struct {
	value   time.Duration
	updated time.Time
	history [rttHistoryBuckets]rttHistorySample
}

func (e *rttEstimator) observe(sample time.Duration, now time.Time) bool {
	if e == nil || sample < rttMinimumSample || sample > rttMaximumSample {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	// Keep the lower envelope of successful probes. A delayed Ping measures
	// path RTT plus queueing, and must not permanently inflate flow-control
	// credit. One entry per second makes the history bounded even when a
	// custom keepalive interval is very short.
	slot := rttHistorySlot(now)
	entry := &e.history[slot]
	if entry.at.IsZero() || entry.at.Unix() != now.Unix() {
		*entry = rttHistorySample{value: sample, at: now}
	} else if sample < entry.value {
		entry.value = sample
	}
	e.recompute(now)
	// Monotonic time values must not move backwards. This also keeps a
	// wall-clock adjustment in a test or host from extending sample life.
	if e.updated.IsZero() || now.After(e.updated) {
		e.updated = now
	}
	// recompute sets value from history, but an all-future history after a
	// clock rollback still needs the most recent sample as a usable estimate.
	if e.value == 0 {
		e.value = sample
	}
	return true
}

func rttHistorySlot(now time.Time) int {
	second := now.Unix() / int64(rttHistoryBucketWidth/time.Second)
	slot := second % rttHistoryBuckets
	if slot < 0 {
		slot += rttHistoryBuckets
	}
	return int(slot)
}

func (e *rttEstimator) recompute(now time.Time) {
	minimum := time.Duration(0)
	for i := range e.history {
		entry := &e.history[i]
		if entry.at.IsZero() {
			continue
		}
		if now.After(entry.at) && now.Sub(entry.at) >= rttSampleStaleAfter {
			*entry = rttHistorySample{}
			continue
		}
		if minimum == 0 || entry.value < minimum {
			minimum = entry.value
		}
	}
	e.value = minimum
}

func (e *rttEstimator) valueAt(now time.Time) time.Duration {
	if e == nil || e.value <= 0 {
		return 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	// A backwards wall-clock move is not evidence that a sample is stale.
	// Session callers use monotonic time.Time values; this guard keeps the
	// estimator total for deterministic callers too.
	e.recompute(now)
	if e.value == 0 {
		e.updated = time.Time{}
	}
	return e.value
}

// RTT returns the recent minimum round-trip time of successful session pings, or
// zero before the first sample and after the sample has aged out.
func (s *Session) RTT() time.Duration {
	if s == nil {
		return 0
	}
	nanos := s.rttNanos.Load()
	updated := s.rttUpdatedNanos.Load()
	if nanos <= 0 || updated <= 0 {
		return 0
	}
	// UnixNano is used only for the lock-free cache expiry check. A backwards
	// wall-clock adjustment is treated as age zero; Ping's estimator itself is
	// updated under rttMu and uses monotonic time.Time values.
	age := time.Now().UnixNano() - updated
	if age < 0 || age < rttSampleStaleAfter.Nanoseconds() {
		return time.Duration(nanos)
	}
	return 0
}

// beginRTTProbe claims the session-wide probe gate. RequestRTT is throttled;
// keepalive passes force=true because its configured cadence is already the
// throttle. Both callers still use this gate, so they cannot send concurrent
// Pings.
func (s *Session) beginRTTProbe(force bool) bool {
	if s == nil || s.IsClosed() {
		return false
	}
	now := time.Now()
	s.rttMu.Lock()
	defer s.rttMu.Unlock()
	sinceProbe := now.Sub(s.rttProbed)
	if s.rttProbe || (!force && !s.rttProbed.IsZero() && sinceProbe >= 0 && sinceProbe < time.Second) {
		return false
	}
	s.rttProbe = true
	s.rttProbed = now
	return true
}

func (s *Session) endRTTProbe() {
	if s == nil {
		return
	}
	s.rttMu.Lock()
	s.rttProbe = false
	s.rttMu.Unlock()
}

// runRTTProbe performs one synchronous, session-gated probe. The bool says
// whether this caller owned the gate; keepalive skips a tick when a request
// probe is already in flight rather than treating that normal collision as a
// keepalive failure.
func (s *Session) runRTTProbe(force bool) (time.Duration, error, bool) {
	if !s.beginRTTProbe(force) {
		return 0, nil, false
	}
	defer s.endRTTProbe()
	rtt, err := s.Ping()
	return rtt, err, true
}

// RequestRTT schedules a bounded, asynchronous measurement. Streams share one
// probe at a time and at most one new probe per second. Idle streams do not
// create a polling goroutine; normal keepalives also refresh the estimate.
func (s *Session) RequestRTT() {
	if !s.beginRTTProbe(false) {
		return
	}
	go func() {
		defer s.endRTTProbe()
		_, _ = s.Ping()
	}()
}

func (s *Session) recordRTT(sample time.Duration) {
	if s == nil {
		return
	}
	now := time.Now()
	s.rttMu.Lock()
	if s.rttEstimate.observe(sample, now) {
		s.rttNanos.Store(int64(s.rttEstimate.value))
		s.rttUpdatedNanos.Store(s.rttEstimate.updated.UnixNano())
	}
	s.rttMu.Unlock()
}
