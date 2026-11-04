package tunnel

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Local resource ceilings, not negotiated promises. Growth cannot spend the
// two prepaid baseline slots of a lane (serving plus replacement/draining).
const AggregateLanePoolLimit uint32 = 16 << 20
const AggregateGenerationGrowthBytes int64 = 64 << 20

var ErrLaneOwnerUnavailable = errors.New("tunnel lane ownership capacity unavailable")

type LaneOwnerState uint8

const (
	LaneReplacement LaneOwnerState = iota
	LaneServing
	LaneDraining
)

type laneOwnerSlot struct {
	owners   map[*LaneOwner]struct{}
	serving  *LaneOwner
	release  func()
	growth   int64
	reserved int64
}

// LaneOwners is the authoritative lifetime registry for one authenticated
// generation. Routing arrays are projections, never growth/lease owners.
// Close fences acquisition and joins every physical session before refunding.
// A generation must be joined before a fresh generation may acquire its budget.
type LaneOwners struct {
	mu     sync.Mutex
	closed bool
	lanes  map[int]*laneOwnerSlot
	growth int64
}

type LaneOwner struct {
	group        *LaneOwners
	lane         int
	ID           string
	state        LaneOwnerState
	session      *yamux.Session
	growth       *AggregateReceiveWindowGrowth
	closed       bool
	once         sync.Once
	openings     sync.WaitGroup
	openingCount int
	openingsDone chan struct{}
	activity     uint64
	idleSince    time.Time
	application  map[*yamux.Stream]struct{}
}

// Acquire prepays BOTH baseline slots before the first session starts. The
// reserve callback is the endpoint's global admission ledger, never bypassed.
// Failed handshakes release their owner; an empty lane returns its headroom.
func (g *LaneOwners) Acquire(lane int, id, replaces string, reserved int64, reserve func() (func(), bool)) (*LaneOwner, error) {
	if lane < 0 || lane > MaxParallelTunnels || id == "" || reserved < 0 {
		return nil, ErrLaneOwnerUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, ErrLaneOwnerUnavailable
	}
	if g.lanes == nil {
		g.lanes = make(map[int]*laneOwnerSlot)
	}
	slot := g.lanes[lane]
	if slot == nil {
		if replaces != "" {
			return nil, ErrLaneOwnerUnavailable
		}
		release, ok := reserve()
		if !ok {
			return nil, ErrLaneOwnerUnavailable
		}
		slot = &laneOwnerSlot{owners: make(map[*LaneOwner]struct{}), release: release, reserved: reserved}
		g.lanes[lane] = slot
	}
	if slot.reserved != reserved || len(slot.owners) >= 2 {
		return nil, ErrLaneOwnerUnavailable
	}
	if slot.serving != nil {
		if replaces == "" || slot.serving.ID != replaces {
			return nil, ErrLaneOwnerUnavailable
		}
	} else if len(slot.owners) > 0 {
		// A committed replacement can lose its transport before the agent sees
		// commit. Retry the still-owned draining predecessor, not a new budget.
		matched := false
		for old := range slot.owners {
			if old.state == LaneDraining && old.ID == replaces {
				matched = true
			}
		}
		if !matched {
			return nil, ErrLaneOwnerUnavailable
		}
	} else if replaces != "" {
		return nil, ErrLaneOwnerUnavailable
	}
	owner := &LaneOwner{group: g, lane: lane, ID: id, state: LaneReplacement}
	slot.owners[owner] = struct{}{}
	return owner, nil
}

func (o *LaneOwner) Attach(s *yamux.Session, reserve func(int64) (func(), bool)) bool {
	g := o.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || o.closed || o.session != nil {
		return false
	}
	o.session = s
	if !s.AggregateFlowControlEnabled() {
		return true
	}
	o.growth = NewAggregateReceiveWindowGrowth(s, func(bytes int64) (func(), bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		slot := g.lanes[o.lane]
		laneLimit := int64(yamux.AggregateReceiveWindowMemory(AggregateLanePoolLimit) - yamux.AggregateReceiveWindowMemory(AggregateInitialSessionWindowBytes))
		if g.closed || o.closed || o.state == LaneDraining || slot == nil || bytes < 0 || bytes > laneLimit-slot.growth || bytes > AggregateGenerationGrowthBytes-g.growth {
			return nil, false
		}
		release, ok := reserve(bytes)
		if !ok {
			return nil, false
		}
		slot.growth += bytes
		g.growth += bytes
		var once sync.Once
		return func() {
			once.Do(func() {
				release()
				g.mu.Lock()
				slot.growth -= bytes
				g.growth -= bytes
				g.mu.Unlock()
			})
		}, true
	})
	o.growth.allow = func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return !g.closed && !o.closed && o.state != LaneDraining
	}
	o.growth.SetPoolLimit(AggregateLanePoolLimit)
	return true
}

// Publish occurs only after the replacement's handshake/session is ready.
func (o *LaneOwner) Publish() (*LaneOwner, bool) {
	return o.publish(nil, 0, false)
}

// PublishWhenIdle revalidates the quiescence observation under the same mutex
// as BeginOpen. No Yamux lock is acquired under the ownership mutex.
func (o *LaneOwner) PublishWhenIdle(quiet time.Duration) (*LaneOwner, bool) {
	g := o.group
	g.mu.Lock()
	slot := g.lanes[o.lane]
	if g.closed || o.closed || slot == nil {
		g.mu.Unlock()
		return nil, false
	}
	old := slot.serving
	g.mu.Unlock()
	if old == nil {
		return o.publish(nil, 0, true)
	}
	epoch, ok := old.idleObservation(quiet)
	if !ok {
		return nil, false
	}
	return o.publish(old, epoch, true)
}

func (o *LaneOwner) publish(expected *LaneOwner, epoch uint64, idleOnly bool) (*LaneOwner, bool) {
	g := o.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || o.closed || o.session == nil {
		return nil, false
	}
	slot := g.lanes[o.lane]
	old := slot.serving
	if idleOnly && (old != expected || old != nil && (old.activity != epoch || old.openingCount != 0 || len(old.application) != 0)) {
		return nil, false
	}
	if old == o {
		return nil, true
	}
	if old != nil {
		old.state = LaneDraining
	}
	slot.serving = o
	o.state = LaneServing
	return old, true
}
func (o *LaneOwner) Session() *yamux.Session {
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	return o.session
}
func (o *LaneOwner) Growth() *AggregateReceiveWindowGrowth {
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	return o.growth
}
func (o *LaneOwner) State() LaneOwnerState {
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	return o.state
}
func (g *LaneOwners) Find(s *yamux.Session) *LaneOwner {
	if s == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, slot := range g.lanes {
		for o := range slot.owners {
			if o.session == s {
				return o
			}
		}
	}
	return nil
}
func (g *LaneOwners) Snapshot() []*LaneOwner {
	g.mu.Lock()
	defer g.mu.Unlock()
	var result []*LaneOwner
	for _, slot := range g.lanes {
		for o := range slot.owners {
			result = append(result, o)
		}
	}
	return result
}
func (o *LaneOwner) Close() {
	if o == nil {
		return
	}
	o.once.Do(func() {
		g := o.group
		g.mu.Lock()
		o.closed = true
		s, growth := o.session, o.growth
		g.mu.Unlock()
		if s != nil {
			_ = s.Close()
		}
		// Closing the socket first wakes outstanding Open calls. Their final
		// registration/reset completes before any lifetime promise is refunded.
		o.openings.Wait()
		g.mu.Lock()
		application := o.application
		o.application = nil
		g.mu.Unlock()
		for stream := range application {
			_ = stream.Reset()
		}
		growth.Release() // joins in-progress growth after physical shutdown
		g.mu.Lock()
		slot := g.lanes[o.lane]
		delete(slot.owners, o)
		if slot.serving == o {
			slot.serving = nil
		}
		var release func()
		if len(slot.owners) == 0 {
			delete(g.lanes, o.lane)
			release = slot.release
		}
		g.mu.Unlock()
		if release != nil {
			release()
		}
	})
}
func (g *LaneOwners) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.closed = true
	var owners []*LaneOwner
	for _, slot := range g.lanes {
		for o := range slot.owners {
			owners = append(owners, o)
		}
	}
	g.mu.Unlock()
	for _, o := range owners {
		o.Close()
	}
}

// ReservedBytes includes both baseline slots and growth still owned by serving,
// handshaking or draining owners. A failed generation cannot reset this ledger.
func (g *LaneOwners) ReservedBytes() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	total := g.growth
	for _, slot := range g.lanes {
		total += slot.reserved
	}
	return total
}

func (o *LaneOwner) ReservedBaselineBytes() int64 {
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	if slot := o.group.lanes[o.lane]; slot != nil {
		return slot.reserved
	}
	return 0
}

// BeginOpen pins the interval from selecting an owner through Open and stream
// registration. Close fences Add under the same mutex before it starts Wait.
func (o *LaneOwner) BeginOpen() bool {
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	if o.closed || o.group.closed || o.state != LaneServing {
		return false
	}
	o.activity++
	o.idleSince = time.Time{}
	if o.openingCount == 0 {
		o.openingsDone = make(chan struct{})
	}
	o.openings.Add(1)
	o.openingCount++
	return true
}

// FinishOpen consumes exactly one BeginOpen. It registers a successful stream
// even after FIN/FIN removes it from Yamux's map. On a shutdown race it explicitly
// resets the buffer before releasing the opening pin, never handing bytes whose
// reservation was refunded back to the application.
func (o *LaneOwner) FinishOpen(stream *yamux.Stream) (func(), bool) {
	defer func() {
		o.group.mu.Lock()
		o.openingCount--
		o.activity++
		o.idleSince = time.Time{}
		if o.openingCount == 0 {
			close(o.openingsDone)
		}
		o.group.mu.Unlock()
		o.openings.Done()
	}()
	g := o.group
	g.mu.Lock()
	if stream == nil {
		g.mu.Unlock()
		return func() {}, false
	}
	if o.closed || g.closed {
		g.mu.Unlock()
		_ = stream.Reset()
		return func() {}, false
	}
	if o.application == nil {
		o.application = make(map[*yamux.Stream]struct{})
	}
	o.application[stream] = struct{}{}
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(o.application, stream)
			o.activity++
			o.idleSince = time.Time{}
			g.mu.Unlock()
		})
	}, true
}

// Quiescent includes application owners detached by FIN/FIN and outstanding
// opens, as well as Yamux's map and receive buffers. It never discards data.
func (o *LaneOwner) Quiescent() bool {
	o.group.mu.Lock()
	session := o.session
	idle := o.openingCount == 0 && len(o.application) == 0
	o.group.mu.Unlock()
	return idle && session != nil && session.ReceiveOwnersDrained()
}

func (g *LaneOwners) Predecessor(lane int, id string) *LaneOwner {
	g.mu.Lock()
	defer g.mu.Unlock()
	slot := g.lanes[lane]
	if slot == nil {
		return nil
	}
	if slot.serving != nil {
		if slot.serving.ID == id {
			return slot.serving
		}
		return nil
	}
	for o := range slot.owners {
		if o.ID == id && o.state == LaneDraining && !o.closed {
			return o
		}
	}
	return nil
}

// IdleFor starts the quiet period only once both application and receive
// ownership have drained. Pooled HTTP keep-alives remain application owners
// until their idle timeout and FIN disposal complete.
func (o *LaneOwner) IdleFor(quiet time.Duration) bool {
	_, ok := o.idleObservation(quiet)
	return ok
}
func (o *LaneOwner) idleObservation(quiet time.Duration) (uint64, bool) {
	g := o.group
	g.mu.Lock()
	epoch, session := o.activity, o.session
	empty := !g.closed && !o.closed && o.openingCount == 0 && len(o.application) == 0
	g.mu.Unlock()
	drained := empty && session != nil && session.ReceiveOwnersDrained()
	g.mu.Lock()
	defer g.mu.Unlock()
	if !drained || o.activity != epoch || g.closed || o.closed {
		if !o.idleSince.IsZero() {
			o.activity++
		}
		o.idleSince = time.Time{}
		return 0, false
	}
	if o.idleSince.IsZero() {
		o.idleSince = time.Now()
	}
	return epoch, time.Since(o.idleSince) >= quiet
}

// AcknowledgeOpeningFence joins all pre-publication Open calls before asking
// the old wire's reader to acknowledge them. Publish must have fenced BeginOpen
// and the routing projection must have called StopOpening first. In particular
// a priority Ping alone cannot overtake an Open whose SYN has not been sent yet.
// No helper goroutine, ownership/projection lock, or lifetime refund is involved.
func (o *LaneOwner) AcknowledgeOpeningFence(ctx context.Context) error {
	g := o.group
	g.mu.Lock()
	if o.state != LaneDraining {
		g.mu.Unlock()
		return ErrLaneOwnerUnavailable
	}
	session, done, pending := o.session, o.openingsDone, o.openingCount != 0
	g.mu.Unlock()
	if session == nil {
		return ErrLaneOwnerUnavailable
	}
	session.StopOpening() // idempotent local fence; never hold group.mu across it
	if pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		case <-session.CloseChan():
			return yamux.ErrSessionShutdown
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := session.Ping() // bounded by the session's ConnectionWriteTimeout
	return err
}
