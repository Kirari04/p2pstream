package server

import "time"

const publicAdmissionReclaimCooldown = 100 * time.Millisecond

// tryReservePublicResource schedules a bounded idle-pool sweep after a failed
// early public admission, then retries the ledger once without waiting for
// socket or protocol close. Each sweep touches at most one agent shard and one
// direct transport; both pool operations preserve active requests and
// control-plane capacity.
func (a *App) tryReservePublicResource(memoryBytes, fileDescriptors int64) (release func(), ok, constrained bool) {
	if a == nil || a.agentStreamCapacity == nil {
		return func() {}, true, false
	}
	release, ok, constrained = a.agentStreamCapacity.tryReserveAdaptiveExternal(memoryBytes, fileDescriptors)
	if ok || !constrained {
		return release, ok, constrained
	}

	a.schedulePublicAdmissionReclaim()
	return a.agentStreamCapacity.tryReserveAdaptiveExternal(memoryBytes, fileDescriptors)
}

func (a *App) schedulePublicAdmissionReclaim() {
	if a == nil || !a.publicAdmissionReclaimMu.TryLock() {
		return
	}
	now := time.Now()
	if a.publicAdmissionReclaimNow != nil {
		now = a.publicAdmissionReclaimNow()
	}
	if a.publicAdmissionReclaimInFlight ||
		(!a.publicAdmissionLastReclaim.IsZero() && now.Sub(a.publicAdmissionLastReclaim) < publicAdmissionReclaimCooldown) {
		a.publicAdmissionReclaimMu.Unlock()
		return
	}
	a.publicAdmissionReclaimInFlight = true
	a.publicAdmissionReclaimMu.Unlock()

	go func() {
		defer func() {
			completedAt := time.Now()
			if a.publicAdmissionReclaimNow != nil {
				completedAt = a.publicAdmissionReclaimNow()
			}
			a.publicAdmissionReclaimMu.Lock()
			// Measure cooldown from completion so a slow protocol close cannot
			// be followed immediately by another sweep. The clock hook is fixed
			// during production setup and mutated by tests only after inFlight
			// becomes false.
			a.publicAdmissionLastReclaim = completedAt
			a.publicAdmissionReclaimInFlight = false
			a.publicAdmissionReclaimMu.Unlock()
		}()
		a.reclaimIdleAgentTransports(1)
		if a.DirectTransports != nil {
			a.DirectTransports.reclaimOldestIdle()
		}
	}()
}
