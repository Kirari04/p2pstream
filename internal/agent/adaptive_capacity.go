package agent

import (
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

type agentTunnelCapacitySnapshot struct {
	Adaptive             bool
	Maximum              int
	AdmissionLimit       int
	InUse                int
	Pressure             sysmetrics.MemoryPressureLevel
	MemoryUsedBytes      int64
	MemoryLimitBytes     int64
	MemorySource         string
	FileDescriptorsUsed  int64
	FileDescriptorsLimit int64
	PressureReason       string
	SampleError          string
	LastGoodSampleAt     time.Time
	RejectedPressure     uint64
	RejectedFixedLimit   uint64
}

// agentTunnelCapacityRuntime owns the agent-side lifetime admission count.
// Adaptive mode has no static memory-derived stream ceiling: its live ceiling
// follows sampled memory and descriptor headroom. Explicit configuration uses
// the same implementation with no controller and remains a hard operator limit.
type agentTunnelCapacityRuntime struct {
	mu         sync.Mutex
	adaptive   bool
	maximum    int
	inUse      int
	controller *sysmetrics.AdaptiveMemoryController

	rejectedPressure atomic.Uint64
	rejectedFixed    atomic.Uint64
	scavengeRunning  atomic.Bool
	scavengeNeeded   atomic.Bool
	freeOSMemory     func()
}

func newAgentTunnelCapacityRuntime(maximum int64, adaptive bool, controller *sysmetrics.AdaptiveMemoryController) *agentTunnelCapacityRuntime {
	runtime := &agentTunnelCapacityRuntime{
		adaptive:     adaptive,
		maximum:      agentTunnelCapacityMaximumInt(maximum),
		controller:   controller,
		freeOSMemory: debug.FreeOSMemory,
	}
	if adaptive && runtime.controller == nil {
		runtime.controller = sysmetrics.MustNewAdaptiveMemoryController(
			sysmetrics.DefaultAdaptiveMemoryConfig(),
			nil,
		)
	}
	return runtime
}

func (r *agentTunnelCapacityRuntime) setMaximum(maximum int64) {
	if r == nil || maximum < 1 {
		return
	}
	r.mu.Lock()
	r.maximum = agentTunnelCapacityMaximumInt(maximum)
	r.mu.Unlock()
}

// agentTunnelCapacityMaximumInt keeps the int64 protocol/config boundary safe
// on every supported architecture. Negotiated values are normally much lower,
// but constructors and setters must remain total even when called directly.
func agentTunnelCapacityMaximumInt(maximum int64) int {
	if maximum < 1 {
		return 1
	}
	if maximum > tunnel.MaxAdaptiveConcurrentStreamsLimit {
		maximum = tunnel.MaxAdaptiveConcurrentStreamsLimit
	}
	// Atoi performs the architecture-width check instead of narrowing an int64
	// directly. The value is already within the protocol implementation guard,
	// so this can fail only on an unsupported architecture with a smaller int.
	converted, err := strconv.Atoi(strconv.FormatInt(maximum, 10))
	if err != nil {
		return 1
	}
	return converted
}

func (r *agentTunnelCapacityRuntime) tryAcquire() (func(), agentTunnelCapacitySnapshot, bool) {
	if r == nil {
		return func() {}, agentTunnelCapacitySnapshot{}, true
	}
	r.mu.Lock()
	snapshot := r.snapshotLocked()
	if snapshot.AdmissionLimit < 1 || r.inUse >= snapshot.AdmissionLimit {
		if snapshot.Adaptive {
			r.rejectedPressure.Add(1)
			// Headroom or descriptor admission can be exhausted below the soft
			// percentage threshold. Remember that the drained generation still
			// needs its unused buffers returned to the OS.
			r.scavengeNeeded.Store(true)
		} else {
			r.rejectedFixed.Add(1)
		}
		snapshot.RejectedPressure = r.rejectedPressure.Load()
		snapshot.RejectedFixedLimit = r.rejectedFixed.Load()
		r.mu.Unlock()
		return nil, snapshot, false
	}
	r.inUse++
	snapshot.InUse = r.inUse
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if r.inUse > 0 {
				r.inUse--
			}
			shouldScavenge := r.scavengeNeeded.Swap(false)
			if r.adaptive && r.inUse == 0 {
				pressure := r.snapshotLocked().Pressure
				shouldScavenge = shouldScavenge || pressure == sysmetrics.MemoryPressureSoft || pressure == sysmetrics.MemoryPressureCritical
			} else {
				// Only the complete generation drain is a low-impact point for a
				// forced scavenging pass. Preserve the signal for its final release.
				if shouldScavenge {
					r.scavengeNeeded.Store(true)
				}
				shouldScavenge = false
			}
			r.mu.Unlock()
			if shouldScavenge {
				if !r.requestMemoryScavenge() {
					r.scavengeNeeded.Store(true)
				}
			}
		})
	}, snapshot, true
}

func (r *agentTunnelCapacityRuntime) requestMemoryScavenge() bool {
	if r == nil || !r.adaptive || !r.scavengeRunning.CompareAndSwap(false, true) {
		return false
	}
	freeOSMemory := r.freeOSMemory
	if freeOSMemory == nil {
		freeOSMemory = debug.FreeOSMemory
	}
	go func() {
		for {
			freeOSMemory()
			if r.scavengeNeeded.Swap(false) {
				continue
			}
			r.scavengeRunning.Store(false)
			// Close the signal-vs-running transition race. If another drained
			// generation arrived just before Store(false), either reclaim this
			// worker or let the concurrently started worker own the pass.
			if !r.scavengeNeeded.Swap(false) {
				return
			}
			if !r.scavengeRunning.CompareAndSwap(false, true) {
				return
			}
		}
	}()
	return true
}

func (r *agentTunnelCapacityRuntime) snapshot() agentTunnelCapacitySnapshot {
	if r == nil {
		return agentTunnelCapacitySnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *agentTunnelCapacityRuntime) forceRefresh() agentTunnelCapacitySnapshot {
	if r == nil {
		return agentTunnelCapacitySnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLockedWithForce(true)
}

func (r *agentTunnelCapacityRuntime) snapshotLocked() agentTunnelCapacitySnapshot {
	return r.snapshotLockedWithForce(false)
}

func (r *agentTunnelCapacityRuntime) snapshotLockedWithForce(force bool) agentTunnelCapacitySnapshot {
	maximum := r.maximum
	if maximum < 1 {
		maximum = 1
	}
	snapshot := agentTunnelCapacitySnapshot{
		Adaptive:           r.adaptive,
		Maximum:            maximum,
		AdmissionLimit:     maximum,
		InUse:              r.inUse,
		Pressure:           sysmetrics.MemoryPressureUnknown,
		RejectedPressure:   r.rejectedPressure.Load(),
		RejectedFixedLimit: r.rejectedFixed.Load(),
	}
	if !r.adaptive || r.controller == nil {
		return snapshot
	}
	var resource sysmetrics.AdaptiveMemorySnapshot
	if force {
		resource = r.controller.ForceRefresh(maximum, r.inUse)
	} else {
		resource = r.controller.Snapshot(maximum, r.inUse)
	}
	snapshot.AdmissionLimit = resource.AdmissionLimit
	snapshot.Pressure = resource.Level
	snapshot.MemoryUsedBytes = resource.Usage.UsedBytes
	snapshot.MemoryLimitBytes = resource.Usage.LimitBytes
	snapshot.MemorySource = resource.Usage.Source
	snapshot.FileDescriptorsUsed = resource.FDUsed
	snapshot.FileDescriptorsLimit = resource.FDLimit
	snapshot.PressureReason = resource.PressureReason
	snapshot.SampleError = resource.SampleError
	snapshot.LastGoodSampleAt = resource.LastGoodSampleAt
	if resource.RejectNew {
		snapshot.AdmissionLimit = r.inUse
	}
	return snapshot
}
