package server

import "time"

const publicAdmissionReclaimCooldown = 100 * time.Millisecond

// tryReservePublicResource retries one failed early public admission after a
// bounded idle-pool sweep. The mutex coalesces concurrent misses and the
// cooldown prevents a wave of requests from turning into sequential cleanup
// work. Each sweep touches at most one agent shard and one direct transport;
// both pool operations preserve active requests and control-plane capacity.
func (a *App) tryReservePublicResource(memoryBytes, fileDescriptors int64) (release func(), ok, constrained bool) {
	if a == nil || a.agentStreamCapacity == nil {
		return func() {}, true, false
	}
	release, ok, constrained = a.agentStreamCapacity.tryReserveAdaptiveExternal(memoryBytes, fileDescriptors)
	if ok || !constrained {
		return release, ok, constrained
	}

	if !a.publicAdmissionReclaimMu.TryLock() {
		// Public input is attacker-controlled. Never queue accept or request
		// goroutines behind a sweep that may close several protocol layers; a
		// cheap retry can still observe credit that the in-flight sweep returned.
		return a.agentStreamCapacity.tryReserveAdaptiveExternal(memoryBytes, fileDescriptors)
	}
	defer a.publicAdmissionReclaimMu.Unlock()

	now := time.Now()
	if a.publicAdmissionReclaimNow != nil {
		now = a.publicAdmissionReclaimNow()
	}
	if a.publicAdmissionLastReclaim.IsZero() || now.Sub(a.publicAdmissionLastReclaim) >= publicAdmissionReclaimCooldown {
		a.publicAdmissionLastReclaim = now
		a.reclaimIdleAgentTransports(1)
		if a.DirectTransports != nil {
			a.DirectTransports.reclaimOldestIdle()
		}
	}
	return a.agentStreamCapacity.tryReserveAdaptiveExternal(memoryBytes, fileDescriptors)
}
