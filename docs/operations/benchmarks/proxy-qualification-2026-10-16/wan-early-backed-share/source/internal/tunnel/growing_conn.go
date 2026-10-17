package tunnel

import (
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// GrowingConn retains all granted credit until Release, which the caller must
// invoke only after peer FIN or forced Yamux cleanup. Denied growth leaves the
// stream usable at its existing window instead of failing the request.
type GrowingConn struct {
	net.Conn
	stream              *yamux.Stream
	maximum             uint32
	reserve             func(int64) (func(), bool)
	aggregateGrowth     *AggregateReceiveWindowGrowth
	mu                  sync.Mutex
	growth              receiveWindowGrowth
	closing             bool
	earlyShareConsumed  uint64
	earlyShareAttempted bool
	releases            []func()
}

func NewGrowingConn(conn net.Conn, maximum int64, reserve func(int64) (func(), bool)) *GrowingConn {
	window, err := NormalizeMaxStreamWindowSizeBytes(maximum)
	stream, _ := conn.(*yamux.Stream)
	if err != nil && stream != nil {
		// Runtime callers validate configuration before opening a session. If
		// another caller supplies an invalid ceiling, grant no extra credit.
		window = stream.MaxReceiveWindow()
	}
	c := &GrowingConn{Conn: conn, stream: stream, maximum: window, reserve: reserve}
	c.growth.start = time.Now()
	if stream != nil && reserve != nil && stream.MaxReceiveWindow() < window {
		stream.Session().RequestRTT()
	}
	return c
}

// NewAggregateGrowingConn grows the session-owned payload pool and this
// stream's unit allowance before publishing additional byte credit. The
// shared growth owner retains every successful reservation until the entire
// Yamux session has completed shutdown.
func NewAggregateGrowingConn(conn net.Conn, maximum int64, growth *AggregateReceiveWindowGrowth) *GrowingConn {
	window, err := NormalizeMaxStreamWindowSizeBytes(maximum)
	stream, _ := conn.(*yamux.Stream)
	if err != nil && stream != nil {
		window = stream.MaxReceiveWindow()
	}
	c := &GrowingConn{Conn: conn, stream: stream, maximum: window, aggregateGrowth: growth}
	c.growth.start = time.Now()
	if stream != nil && growth != nil {
		if allocationMaximum, enabled := stream.AggregateReceiveAllocationMaximum(); enabled {
			c.maximum = min(c.maximum, allocationMaximum)
		}
		current, enabled := stream.AggregateReceiveAllocationWindow()
		if enabled && current < c.maximum {
			stream.Session().RequestRTT()
		}
	}
	return c
}

func (c *GrowingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && err == nil && c.stream != nil && (c.reserve != nil || c.aggregateGrowth != nil) {
		if growErr := c.grow(uint64(n)); growErr != nil {
			return n, growErr
		}
	}
	return n, err
}

func (c *GrowingConn) grow(n uint64) error {
	return c.growMeasured(n, time.Now(), c.stream.Session().RTT())
}

func (c *GrowingConn) growMeasured(n uint64, now time.Time, rtt time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.stream.MaxReceiveWindow()
	if c.aggregateGrowth != nil {
		allocation, enabled := c.stream.AggregateReceiveAllocationWindow()
		if !enabled {
			return fmt.Errorf("aggregate receive-window growth used on a legacy Yamux stream")
		}
		current = allocation
	}
	if c.closing || current >= c.maximum {
		return nil
	}
	if enabled, configured := qualificationEarlyShareMode(); configured && c.aggregateGrowth != nil && c.takeEarlyShareAttempt(n) {
		qualificationEarlyShareAttemptStarted()
		observation, err := c.aggregateGrowth.tryBackedReceiveShare(c.stream, c.maximum, enabled)
		observation.ThresholdProgressBytes = c.earlyShareConsumed
		c.recordQualificationEarlyShare(now, observation)
		if err != nil && enabled {
			return err
		}
		if enabled && observation.Outcome == "stream_closed" {
			return nil // Do not retry through the ordinary estimator on this Read.
		}
		// The early attempt owns neither estimator history nor its denial timer.
		// Refresh both actual limits before observing these consumed bytes once.
		current = c.stream.MaxReceiveWindow()
		allocation, active := c.stream.AggregateReceiveAllocationWindow()
		if !active {
			return errors.New("aggregate early-share lost allocation capability")
		}
		current = allocation
	}
	if current >= c.maximum {
		return nil
	}
	next, sampled := c.growth.observe(now, n, current, c.maximum, rtt)
	if sampled {
		c.stream.Session().RequestRTT()
	}
	if next <= current {
		if qualificationGrowthTraceEnabled && sampled {
			c.recordQualificationGrowthSample(now, rtt, current, next, qualificationGrowthOutcomeNoProposal)
		}
		return nil
	}
	if c.aggregateGrowth != nil {
		granted, err := c.aggregateGrowth.Grow(c.stream, next)
		if !granted {
			c.growth.deniedUntil = now.Add(time.Second)
		}
		if qualificationGrowthTraceEnabled && sampled {
			outcome := qualificationGrowthOutcomeGrown
			if err != nil {
				outcome = qualificationGrowthOutcomePublicationError
			} else if !granted {
				outcome = qualificationGrowthOutcomeReservationDenied
			}
			c.recordQualificationGrowthSample(now, rtt, current, next, outcome)
		}
		return err
	}
	// The initial stream charge includes chunk slack. Charge every additional
	// chunk and its bookkeeping before publishing the new receive credit.
	extra := yamux.ReceiveWindowMemory(next) - yamux.ReceiveWindowMemory(current)
	release, ok := c.reserve(int64(extra))
	if !ok {
		c.growth.deniedUntil = now.Add(time.Second)
		if qualificationGrowthTraceEnabled && sampled {
			c.recordQualificationGrowthSample(now, rtt, current, next, qualificationGrowthOutcomeReservationDenied)
		}
		return nil
	}
	c.releases = append(c.releases, release)
	// Keep the reservation even if the write fails: credit might have reached
	// the peer before the error became observable locally.
	err := c.stream.GrowReceiveWindow(next)
	if qualificationGrowthTraceEnabled && sampled {
		outcome := qualificationGrowthOutcomeGrown
		if err != nil {
			outcome = qualificationGrowthOutcomePublicationError
		}
		c.recordQualificationGrowthSample(now, rtt, current, next, outcome)
	}
	return err
}

// AggregateReceiveWindowGrowth serializes irrevocable pool and window growth
// for one Yamux session. Reserve callbacks run before allocation/publication;
// once accepted, their leases remain owned until Release after Session.Close
// has completed, including when a later publication returns an error.
type AggregateReceiveWindowGrowth struct {
	session  *yamux.Session
	reserve  func(int64) (func(), bool)
	mu       sync.Mutex
	closing  bool
	releases []func()
}

func NewAggregateReceiveWindowGrowth(session *yamux.Session, reserve func(int64) (func(), bool)) *AggregateReceiveWindowGrowth {
	return &AggregateReceiveWindowGrowth{session: session, reserve: reserve}
}

// Grow publishes credit in dependency order: session allocation, stream
// allocation, then byte window. A denied session reservation returns false
// without changing existing credit, so the stream remains usable.
func (g *AggregateReceiveWindowGrowth) Grow(stream *yamux.Stream, target uint32) (bool, error) {
	if g == nil || g.session == nil || g.reserve == nil || stream == nil {
		return false, nil
	}
	if stream.Session() != g.session {
		return false, errors.New("aggregate growth stream belongs to another session")
	}
	unit := uint64(yamux.AggregateWindowUnitSize)
	allocation64 := (uint64(target) + unit - 1) / unit * unit
	if allocation64 > math.MaxUint32 {
		return false, fmt.Errorf("aggregate stream allocation target %d exceeds protocol range", allocation64)
	}
	allocation := uint32(allocation64)
	session64 := allocation64 * 4
	if session64 > uint64(AggregateMaxSessionWindowBytes) {
		return false, fmt.Errorf("aggregate session growth target %d exceeds negotiated maximum", session64)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false, yamux.ErrSessionShutdown
	}
	stats := g.session.AggregateReceiveWindowStats()
	if !stats.Enabled || stats.Closed {
		return false, yamux.ErrSessionShutdown
	}
	if uint32(session64) > stats.ReceiveLimit {
		extra := yamux.AggregateReceiveWindowMemory(uint32(session64)) - yamux.AggregateReceiveWindowMemory(stats.ReceiveLimit)
		release, ok := g.reserve(int64(extra))
		if !ok {
			return false, nil
		}
		// Retain first: every following publication is irrevocable or ambiguous.
		g.releases = append(g.releases, release)
		if err := g.session.GrowReceiveWindow(uint32(session64)); err != nil {
			return true, err
		}
	}
	if err := stream.GrowReceiveAllocationWindow(allocation); err != nil {
		return true, err
	}
	if err := stream.GrowReceiveWindow(target); err != nil {
		return true, err
	}
	return true, nil
}

// Release returns session-owned growth reservations. The caller must invoke it
// only after Session.Close has completed and all receive slabs are cleared.
func (g *AggregateReceiveWindowGrowth) Release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		return
	}
	g.closing = true
	releases := g.releases
	g.releases = nil
	g.mu.Unlock()
	for _, release := range releases {
		if release != nil {
			release()
		}
	}
}

func (c *GrowingConn) Close() error {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *GrowingConn) Release() {
	c.mu.Lock()
	c.closing = true
	releases := c.releases
	c.releases = nil
	c.mu.Unlock()
	for _, release := range releases {
		if release != nil {
			release()
		}
	}
}
