package tunnel

import (
	"testing"
	"time"
)

func TestReceiveWindowGrowthTracksDemand(t *testing.T) {
	const initial, maximum = uint32(512 << 10), uint32(1 << 30)
	for _, tc := range []struct {
		name             string
		rtt              time.Duration
		rate             uint64
		wantMin, wantMax uint32
	}{
		{"unknown RTT", 0, 1 << 30, initial, initial},
		{"gigabit LAN", 200 * time.Microsecond, 125_000_000, 1_600_000, 2_100_000},
		{"ten gigabit LAN", 200 * time.Microsecond, 1_250_000_000, 16_000_000, 20_100_000},
		{"slow VPN", 80 * time.Millisecond, 1_000_000, initial, initial},
		{"gigabit VPN", 80 * time.Millisecond, 125_000_000, 64_000_000, 80_100_000},
		{"ten gigabit VPN", 80 * time.Millisecond, 1_250_000_000, 640_000_000, 800_100_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			g := receiveWindowGrowth{start: now}
			current := initial
			interval := max(10*time.Millisecond, tc.rtt)
			for range 80 {
				now = now.Add(interval)
				// Model a conservative window-limited delivery rate. The
				// estimator must escape this limit without knowing link speed.
				rate := tc.rate
				if tc.rtt > 0 {
					rate = min(rate, uint64(float64(current)/(2*tc.rtt.Seconds())))
				}
				n := uint64(float64(rate) * interval.Seconds())
				next, _ := g.observe(now, n, current, maximum, tc.rtt)
				if next < current || uint64(next) > 2*uint64(current) || next > maximum {
					t.Fatalf("invalid growth %d -> %d", current, next)
				}
				current = next
			}
			if current < tc.wantMin || current > tc.wantMax {
				t.Fatalf("window %d outside [%d,%d]", current, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestReceiveWindowGrowthRejectsBurstsAndBacksOff(t *testing.T) {
	const current, maximum = uint32(512 << 10), uint32(1 << 30)
	now := time.Now()
	g := receiveWindowGrowth{start: now}
	step := func(bytes uint64, wait time.Duration) uint32 {
		t.Helper()
		now = now.Add(wait)
		next, _ := g.observe(now, bytes, current, maximum, 80*time.Millisecond)
		return next
	}
	if got := step(8<<20, 80*time.Millisecond); got != current {
		t.Fatal("one burst grew window")
	}
	if got := step(8<<20, 2*time.Second); got != current {
		t.Fatal("burst survived idle gap")
	}
	if got := step(8<<20, 80*time.Millisecond); got != current {
		t.Fatal("idle gap retained growth candidate")
	}
	if got := step(8<<20, 80*time.Millisecond); got != 2*current {
		t.Fatal("sustained traffic did not grow")
	}
	g.deniedUntil = now.Add(time.Second)
	for range 12 {
		if got := step(8<<20, 80*time.Millisecond); got != current {
			t.Fatal("growth during denied cooldown")
		}
	}
	if got := step(8<<20, 80*time.Millisecond); got != current {
		t.Fatal("denial retained growth candidate")
	}
	if got := step(8<<20, 80*time.Millisecond); got != 2*current {
		t.Fatal("growth did not recover after denial")
	}
}

func TestReceiveWindowGrowthIncludesDownstreamTimeAndBoundsArithmetic(t *testing.T) {
	const current, maximum = uint32(512 << 10), uint32(1 << 30)
	now := time.Now()
	g := receiveWindowGrowth{start: now}
	for range 100 {
		now = now.Add(100 * time.Millisecond)
		next, _ := g.observe(now, 32<<10, current, maximum, 80*time.Millisecond)
		if next != current {
			t.Fatal("slow downstream grew window")
		}
	}
	// Backwards clocks discard the old sample; real time.Now uses monotonic
	// timestamps, but a test clock or a bad observation must not overflow.
	next, _ := g.observe(now.Add(-time.Second), ^uint64(0), current, maximum, 80*time.Millisecond)
	if next != current {
		t.Fatal("backwards clock grew window")
	}
	g = receiveWindowGrowth{start: now, previous: maximum}
	next, _ = g.observe(now.Add(80*time.Millisecond), ^uint64(0), maximum-65536, maximum, 80*time.Millisecond)
	if next > maximum {
		t.Fatal("overflowed ceiling")
	}
}

func TestReceiveWindowGrowthUsesTwoSlidingSamples(t *testing.T) {
	const maximum = uint32(1 << 30)
	now := time.Now()
	g := receiveWindowGrowth{start: now}
	current := uint32(512 << 10)
	step := func(bytes uint64) uint32 {
		t.Helper()
		now = now.Add(80 * time.Millisecond)
		next, _ := g.observe(now, bytes, current, maximum, 80*time.Millisecond)
		current = next
		return next
	}
	if got := step(1 << 20); got != 512<<10 {
		t.Fatal("first sample granted credit")
	}
	if got := step(1 << 20); got != 1<<20 {
		t.Fatal("two supported samples did not grow")
	}
	if got := step(1 << 20); got != 2<<20 {
		t.Fatal("continuous demand restarted a two-RTT wait after growth")
	}
	if got := step(0); got != 2<<20 {
		t.Fatal("zero consumption grew window")
	}
	if got := step(1 << 20); got != 2<<20 {
		t.Fatal("one burst after low demand grew window")
	}
	if got := step(1 << 20); got != 4<<20 {
		t.Fatal("sustained demand did not recover")
	}
}

func TestReceiveWindowGrowthSmoothsQueuedReadBursts(t *testing.T) {
	// A relay alternates slow writes with fast drains of already queued data.
	// Consecutive burst samples alone must not turn this ~90 MB/s workload
	// into a several-hundred-MiB irrevocable receive window.
	const maximum = uint32(1 << 30)
	now := time.Now()
	g := receiveWindowGrowth{start: now}
	current := uint32(512 << 10)
	for i := range 160 {
		rate := uint64(40_000_000)
		if i%8 >= 6 {
			rate = 240_000_000
		}
		now = now.Add(80 * time.Millisecond)
		current, _ = g.observe(now, rate*80/1000, current, maximum, 80*time.Millisecond)
	}
	if current > 96<<20 {
		t.Fatalf("bursty reads inflated receive window to %d bytes", current)
	}
}
