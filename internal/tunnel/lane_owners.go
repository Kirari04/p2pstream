package tunnel

import (
	"errors"
	"sync"

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
	g := o.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || o.closed || o.session == nil {
		return nil, false
	}
	slot := g.lanes[o.lane]
	old := slot.serving
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
	return func() { once.Do(func() { g.mu.Lock(); delete(o.application, stream); g.mu.Unlock() }) }, true
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
