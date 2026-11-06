package tunnel

import "time"

const (
	receiveWindowHeadroom = 8
	// A Ping measures control-frame latency, while DATA credit must also cover
	// relay batching and goroutine scheduling. A small allowance prevents a
	// sub-millisecond RTT from undersizing a fast local stream's pipeline.
	receiveWindowSchedulingAllowance = 2 * time.Millisecond
)

// receiveWindowGrowth measures consumption over wall time, including time the
// downstream spends outside Read. Lifetime byte counts would grow even a slow
// stream to its ceiling. Two consecutive samples must justify growth instead.
// It has no timer or goroutine: only useful reads can propose more credit.
type receiveWindowGrowth struct {
	start       time.Time
	bytes       uint64
	rate        float64
	previous    uint32
	deniedUntil time.Time
}

// observe returns a proposed window and whether an epoch completed. Headroom
// covers batched credit updates, scheduling and short transport stalls.
// A window-limited stream can therefore probe beyond its current rate. Credit
// never shrinks, since the peer may already be using all advertised bytes.
func (g *receiveWindowGrowth) observe(now time.Time, n uint64, current, maximum uint32, rtt time.Duration) (uint32, bool) {
	if g.start.IsZero() || now.Before(g.start) {
		g.start = now
		g.bytes = 0
		g.previous = 0
		g.rate = 0
	}
	elapsed := now.Sub(g.start)
	if elapsed > max(time.Second, 8*rtt) {
		// Do not carry a burst across a pause or a long blocked read.
		g.start, g.bytes, g.previous = now, 0, 0
		g.rate = 0
		return current, true
	}
	g.bytes += min(n, uint64(maximum))
	if elapsed < max(10*time.Millisecond, rtt) {
		return current, false
	}
	consumed := g.bytes
	g.start, g.bytes = now, 0
	if rtt <= 0 || now.Before(g.deniedUntil) {
		g.previous = 0
		g.rate = 0
		return current, true
	}
	// Read can drain a queued batch much faster than the end-to-end transfer.
	// Smooth that rate before granting irrevocable credit, while requiring the
	// current sample to support it too. A pause cannot borrow earlier demand.
	rate := float64(consumed) / elapsed.Seconds()
	if g.rate == 0 {
		g.rate = rate
	} else {
		g.rate += (rate - g.rate) / 4
	}
	// Convert before multiplying to avoid overflowing byte*nanosecond math.
	horizon := max(rtt, receiveWindowSchedulingAllowance)
	target := min(float64(maximum), receiveWindowHeadroom*min(rate, g.rate)*horizon.Seconds())
	desired := uint32(target)
	prior := g.previous
	g.previous = desired
	threshold := uint64(current) * 5 / 4
	if uint64(desired) <= threshold || uint64(prior) <= threshold {
		return current, true
	}
	// Require both samples to support the proposal, then limit any one growth
	// to a doubling. A single burst or RTT outlier cannot grant a huge window.
	next := min(uint64(desired), uint64(prior), uint64(current)*2, uint64(maximum))
	// Round up to receive chunks so small differences do not produce repeated
	// protocol updates with no additional storage capacity.
	next = min((next+65535)&^uint64(65535), uint64(maximum))
	// Keep the preceding sample for the next decision. Consecutive decisions
	// still require two supporting samples, without restarting observation
	// after each increase and paying two extra RTTs at every window size.
	return uint32(next), true
}
