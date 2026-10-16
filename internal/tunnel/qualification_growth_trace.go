//go:build p2pstream_qualification

package tunnel

import (
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	qualificationGrowthTraceEnabled  = true
	qualificationGrowthTraceCapacity = 4096
)

type qualificationGrowthOutcome uint8

const (
	qualificationGrowthOutcomeNoProposal qualificationGrowthOutcome = iota
	qualificationGrowthOutcomeGrown
	qualificationGrowthOutcomeReservationDenied
	qualificationGrowthOutcomePublicationError
)

func (o qualificationGrowthOutcome) String() string {
	switch o {
	case qualificationGrowthOutcomeNoProposal:
		return "no_proposal"
	case qualificationGrowthOutcomeGrown:
		return "grown"
	case qualificationGrowthOutcomeReservationDenied:
		return "reservation_denied"
	case qualificationGrowthOutcomePublicationError:
		return "publication_error"
	default:
		return "unknown"
	}
}

// QualificationReceiveWindowGrowthEvent describes one completed demand
// sampling epoch. SampleUnixNano is the epoch's sample-start timestamp reused
// by the growth controller; the outcome is recorded after any publication.
// Session is exposed only so the tagged qualification adapter can resolve a
// lane without adding labels or state to the production data path.
type QualificationReceiveWindowGrowthEvent struct {
	SampleUnixNano      int64
	Session             *yamux.Session `json:"-"`
	SessionIdentity     string
	StreamID            uint32
	Aggregate           bool
	Current             uint32
	Proposed            uint32
	Maximum             uint32
	RTTNanoseconds      int64
	RateBytesPerSecond  float64
	Previous            uint32
	DeniedUntilUnixNano int64
	DeniedAtSample      bool
	Outcome             string
}

// QualificationReceiveWindowGrowthDrain is a destructive, bounded diagnostic
// snapshot. DroppedEvents counts samples overwritten since the preceding
// drain. Events are ordered from oldest retained to newest.
type QualificationReceiveWindowGrowthDrain struct {
	Capacity      int
	DroppedEvents uint64
	Events        []QualificationReceiveWindowGrowthEvent
}

type qualificationReceiveWindowGrowthEvent struct {
	sampleUnixNano      int64
	session             *yamux.Session
	streamID            uint32
	aggregate           bool
	current             uint32
	proposed            uint32
	maximum             uint32
	rttNanoseconds      int64
	rateBytesPerSecond  float64
	previous            uint32
	deniedUntilUnixNano int64
	deniedAtSample      bool
	outcome             qualificationGrowthOutcome
}

var qualificationReceiveWindowGrowthRing struct {
	mu      sync.Mutex
	events  [qualificationGrowthTraceCapacity]qualificationReceiveWindowGrowthEvent
	head    int
	count   int
	dropped uint64
}

func (c *GrowingConn) recordQualificationGrowthSample(
	now time.Time,
	rtt time.Duration,
	current uint32,
	proposed uint32,
	outcome qualificationGrowthOutcome,
) {
	if c == nil || c.stream == nil {
		return
	}
	deniedUntil := c.growth.deniedUntil
	var deniedUntilUnixNano int64
	if !deniedUntil.IsZero() {
		deniedUntilUnixNano = deniedUntil.UnixNano()
	}
	event := qualificationReceiveWindowGrowthEvent{
		sampleUnixNano:      now.UnixNano(),
		session:             c.stream.Session(),
		streamID:            c.stream.StreamID(),
		aggregate:           c.aggregateGrowth != nil,
		current:             current,
		proposed:            proposed,
		maximum:             c.maximum,
		rttNanoseconds:      rtt.Nanoseconds(),
		rateBytesPerSecond:  c.growth.rate,
		previous:            c.growth.previous,
		deniedUntilUnixNano: deniedUntilUnixNano,
		deniedAtSample:      !deniedUntil.IsZero() && now.Before(deniedUntil),
		outcome:             outcome,
	}

	ring := &qualificationReceiveWindowGrowthRing
	ring.mu.Lock()
	index := (ring.head + ring.count) % qualificationGrowthTraceCapacity
	if ring.count == qualificationGrowthTraceCapacity {
		index = ring.head
		ring.head = (ring.head + 1) % qualificationGrowthTraceCapacity
		ring.dropped++
	} else {
		ring.count++
	}
	ring.events[index] = event
	ring.mu.Unlock()
}

// DrainQualificationReceiveWindowGrowthEvents returns and clears the tagged
// diagnostic ring. Formatting the pointer-derived opaque identity and all
// output allocation happen here, outside the sampled growth path.
func DrainQualificationReceiveWindowGrowthEvents() QualificationReceiveWindowGrowthDrain {
	ring := &qualificationReceiveWindowGrowthRing
	ring.mu.Lock()
	defer ring.mu.Unlock()

	drain := QualificationReceiveWindowGrowthDrain{
		Capacity:      qualificationGrowthTraceCapacity,
		DroppedEvents: ring.dropped,
		Events:        make([]QualificationReceiveWindowGrowthEvent, ring.count),
	}
	for i := 0; i < ring.count; i++ {
		index := (ring.head + i) % qualificationGrowthTraceCapacity
		raw := ring.events[index]
		drain.Events[i] = QualificationReceiveWindowGrowthEvent{
			SampleUnixNano:      raw.sampleUnixNano,
			Session:             raw.session,
			SessionIdentity:     fmt.Sprintf("%p", raw.session),
			StreamID:            raw.streamID,
			Aggregate:           raw.aggregate,
			Current:             raw.current,
			Proposed:            raw.proposed,
			Maximum:             raw.maximum,
			RTTNanoseconds:      raw.rttNanoseconds,
			RateBytesPerSecond:  raw.rateBytesPerSecond,
			Previous:            raw.previous,
			DeniedUntilUnixNano: raw.deniedUntilUnixNano,
			DeniedAtSample:      raw.deniedAtSample,
			Outcome:             raw.outcome.String(),
		}
		ring.events[index] = qualificationReceiveWindowGrowthEvent{}
	}
	ring.head = 0
	ring.count = 0
	ring.dropped = 0
	return drain
}
