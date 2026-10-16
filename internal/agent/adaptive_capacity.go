package agent

import (
	"math"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

type agentTunnelCapacitySnapshot struct {
	Adaptive                      bool
	ResourceLimited               bool
	Maximum                       int
	AdmissionLimit                int
	InUse                         int
	Pressure                      sysmetrics.MemoryPressureLevel
	MemoryUsedBytes               int64
	MemoryLimitBytes              int64
	MemorySource                  string
	FileDescriptorsUsed           int64
	FileDescriptorsLimit          int64
	PressureReason                string
	SampleError                   string
	LastGoodSampleAt              time.Time
	AdmissionRole                 sysmetrics.AdaptiveAdmissionRole
	StreamChargeBytes             int64
	StreamChargeFDs               int64
	ReceiveWindowBytes            int64
	SessionResourceBytes          int64
	AggregateInitialPoolBytes     int64
	AggregateGrowthPoolBytes      int64
	AggregateCurrentPoolBytes     int64
	AggregateSessionMetadataBytes int64
	LegacyControlSupplementBytes  int64
	LegacySessionEnvelopeBytes    int64
	LegacyStreamSupplementBytes   int64
	RejectedPressure              uint64
	RejectedFixedLimit            uint64
}

// agentTunnelCapacityRuntime owns the agent-side lifetime admission count.
// Adaptive mode has no static memory-derived stream ceiling: its live ceiling
// follows sampled memory and descriptor headroom. Explicit production limits
// also retain resource protection, while adding a hard operator ceiling.
type agentTunnelCapacityRuntime struct {
	mu                            sync.Mutex
	adaptive                      bool
	maximum                       int
	inUse                         int
	windowBytes                   int64
	sessionBytes                  int64
	aggregateInitialPoolBytes     int64
	aggregateGrowthPoolBytes      int64
	aggregateSessionMetadataBytes int64
	legacyControlSupplementBytes  int64
	legacySessionEnvelopeBytes    int64
	legacyStreamSupplementBytes   int64
	controller                    *sysmetrics.AdaptiveMemoryController

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

func (r *agentTunnelCapacityRuntime) tryReserveWindowGrowth(bytes int64) (func(), bool) {
	return r.tryReserveOwnedReceiveResource(bytes, agentReceiveResourceStreamGrowth)
}

type agentReceiveResourceOwner uint8

const (
	agentReceiveResourceStreamGrowth agentReceiveResourceOwner = iota
	agentReceiveResourceAggregateInitialPool
	agentReceiveResourceAggregateGrowthPool
	agentReceiveResourceAggregateSessionMetadata
	agentReceiveResourceLegacyControlSupplement
	agentReceiveResourceLegacySessionEnvelope
	agentReceiveResourceLegacyStreamSupplement
)

func (r *agentTunnelCapacityRuntime) tryReserveOwnedReceiveResource(bytes int64, owner agentReceiveResourceOwner) (func(), bool) {
	if r == nil || r.controller == nil {
		return func() {}, true
	}
	r.mu.Lock()
	resource := r.controller.Snapshot(int(tunnel.MaxAdaptiveConcurrentStreamsLimit), r.inUse)
	if bytes < 0 || r.windowBytes < 0 || r.sessionBytes < 0 || r.windowBytes > math.MaxInt64-r.sessionBytes {
		r.mu.Unlock()
		return nil, false
	}
	external := r.windowBytes + r.sessionBytes
	if bytes > math.MaxInt64-external {
		r.mu.Unlock()
		return nil, false
	}
	proposed := external + bytes
	if resource.RejectNew || resource.AdmissionLimitWithExternal(proposed, 0) < r.inUse {
		r.mu.Unlock()
		return nil, false
	}
	switch owner {
	case agentReceiveResourceStreamGrowth:
		r.windowBytes += bytes
	case agentReceiveResourceAggregateInitialPool:
		r.sessionBytes += bytes
		r.aggregateInitialPoolBytes += bytes
	case agentReceiveResourceAggregateGrowthPool:
		r.sessionBytes += bytes
		r.aggregateGrowthPoolBytes += bytes
	case agentReceiveResourceAggregateSessionMetadata:
		r.sessionBytes += bytes
		r.aggregateSessionMetadataBytes += bytes
	case agentReceiveResourceLegacyControlSupplement:
		r.sessionBytes += bytes
		r.legacyControlSupplementBytes += bytes
	case agentReceiveResourceLegacySessionEnvelope:
		r.sessionBytes += bytes
		r.legacySessionEnvelopeBytes += bytes
	case agentReceiveResourceLegacyStreamSupplement:
		r.windowBytes += bytes
		r.legacyStreamSupplementBytes += bytes
	default:
		r.mu.Unlock()
		return nil, false
	}
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			switch owner {
			case agentReceiveResourceStreamGrowth:
				r.windowBytes -= bytes
			case agentReceiveResourceAggregateInitialPool:
				r.sessionBytes -= bytes
				r.aggregateInitialPoolBytes -= bytes
			case agentReceiveResourceAggregateGrowthPool:
				r.sessionBytes -= bytes
				r.aggregateGrowthPoolBytes -= bytes
			case agentReceiveResourceAggregateSessionMetadata:
				r.sessionBytes -= bytes
				r.aggregateSessionMetadataBytes -= bytes
			case agentReceiveResourceLegacyControlSupplement:
				r.sessionBytes -= bytes
				r.legacyControlSupplementBytes -= bytes
			case agentReceiveResourceLegacySessionEnvelope:
				r.sessionBytes -= bytes
				r.legacySessionEnvelopeBytes -= bytes
			case agentReceiveResourceLegacyStreamSupplement:
				r.windowBytes -= bytes
				r.legacyStreamSupplementBytes -= bytes
			}
			r.mu.Unlock()
		})
	}, true
}

// tryReserveSessionResource places a receive-pool or negotiated control
// promise in the same lifetime ledger as stream-window growth. Callers retain
// the returned lease until Session.Close has completed, including when credit
// publication fails after local allocation.
func (r *agentTunnelCapacityRuntime) tryReserveSessionResource(bytes int64) (func(), bool) {
	return r.tryReserveOwnedReceiveResource(bytes, agentReceiveResourceAggregateInitialPool)
}

func (r *agentTunnelCapacityRuntime) tryReserveAggregateSessionResource(poolBytes, metadataBytes int64) (func(), bool) {
	releasePool, ok := r.tryReserveOwnedReceiveResource(poolBytes, agentReceiveResourceAggregateInitialPool)
	if !ok {
		return nil, false
	}
	releaseMetadata, ok := r.tryReserveOwnedReceiveResource(metadataBytes, agentReceiveResourceAggregateSessionMetadata)
	if !ok {
		releasePool()
		return nil, false
	}
	return func() { releaseMetadata(); releasePool() }, true
}

func (r *agentTunnelCapacityRuntime) tryReserveLegacyControlResource(bytes int64) (func(), bool) {
	return r.tryReserveOwnedReceiveResource(bytes, agentReceiveResourceLegacyControlSupplement)
}

func (r *agentTunnelCapacityRuntime) tryReserveLegacySessionEnvelope(bytes int64) (func(), bool) {
	return r.tryReserveOwnedReceiveResource(bytes, agentReceiveResourceLegacySessionEnvelope)
}

func (r *agentTunnelCapacityRuntime) tryReserveLegacyStreamSupplement(bytes int64) (func(), bool) {
	return r.tryReserveOwnedReceiveResource(bytes, agentReceiveResourceLegacyStreamSupplement)
}

func (r *agentTunnelCapacityRuntime) tryReserveAggregateSessionGrowth(bytes int64) (func(), bool) {
	return r.tryReserveOwnedReceiveResource(bytes, agentReceiveResourceAggregateGrowthPool)
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
		if snapshot.Adaptive || snapshot.ResourceLimited {
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
			if r.controller != nil && r.inUse == 0 {
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

// recordPressureRejection accounts for a request rejected before base admission
// because its independently owned receive resource could not be reserved.
// Session setup and receive-window growth failures are not request rejections
// and must not call this method.
func (r *agentTunnelCapacityRuntime) recordPressureRejection() {
	if r == nil {
		return
	}
	r.rejectedPressure.Add(1)
	r.scavengeNeeded.Store(true)
}

func (r *agentTunnelCapacityRuntime) requestMemoryScavenge() bool {
	if r == nil || r.controller == nil || !r.scavengeRunning.CompareAndSwap(false, true) {
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
		Adaptive:                      r.adaptive,
		Maximum:                       maximum,
		AdmissionLimit:                maximum,
		InUse:                         r.inUse,
		Pressure:                      sysmetrics.MemoryPressureUnknown,
		RejectedPressure:              r.rejectedPressure.Load(),
		RejectedFixedLimit:            r.rejectedFixed.Load(),
		ReceiveWindowBytes:            r.windowBytes,
		SessionResourceBytes:          r.sessionBytes,
		AggregateInitialPoolBytes:     r.aggregateInitialPoolBytes,
		AggregateGrowthPoolBytes:      r.aggregateGrowthPoolBytes,
		AggregateCurrentPoolBytes:     r.aggregateInitialPoolBytes + r.aggregateGrowthPoolBytes,
		AggregateSessionMetadataBytes: r.aggregateSessionMetadataBytes,
		LegacyControlSupplementBytes:  r.legacyControlSupplementBytes,
		LegacySessionEnvelopeBytes:    r.legacySessionEnvelopeBytes,
		LegacyStreamSupplementBytes:   r.legacyStreamSupplementBytes,
	}
	if r.controller == nil {
		return snapshot
	}
	var resource sysmetrics.AdaptiveMemorySnapshot
	if force {
		resource = r.controller.ForceRefresh(int(tunnel.MaxAdaptiveConcurrentStreamsLimit), r.inUse)
	} else {
		resource = r.controller.Snapshot(int(tunnel.MaxAdaptiveConcurrentStreamsLimit), r.inUse)
	}
	snapshot.AdmissionLimit = min(maximum, resource.AdmissionLimitWithExternal(r.windowBytes+r.sessionBytes, 0))
	snapshot.ResourceLimited = snapshot.AdmissionLimit < maximum
	snapshot.Pressure = resource.Level
	snapshot.MemoryUsedBytes = resource.Usage.UsedBytes
	snapshot.MemoryLimitBytes = resource.Usage.LimitBytes
	snapshot.MemorySource = resource.Usage.Source
	snapshot.FileDescriptorsUsed = resource.FDUsed
	snapshot.FileDescriptorsLimit = resource.FDLimit
	snapshot.PressureReason = resource.PressureReason
	snapshot.SampleError = resource.SampleError
	snapshot.LastGoodSampleAt = resource.LastGoodSampleAt
	snapshot.AdmissionRole = resource.AdmissionRole
	snapshot.StreamChargeBytes = resource.StreamChargeByte
	snapshot.StreamChargeFDs = resource.StreamChargeFD
	if resource.RejectNew {
		snapshot.AdmissionLimit = r.inUse
		snapshot.ResourceLimited = true
	}
	return snapshot
}
