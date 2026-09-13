package server

import "p2pstream/internal/tunnel"

const (
	defaultAgentStreamCapacityControlStreams = 4
	minimumAgentStreamCapacityWaiters        = 64
	maximumAgentStreamCapacityWaiters        = int(tunnel.MaxServerConcurrentStreamsLimit)
)

// defaultAgentStreamCapacityConfig derives the server-side stream budgets from
// the legacy total while the dedicated server settings are rolled out. The
// budgets are structural: pooled connections can never consume the public
// one-shot headroom, and public work can never consume the trusted health
// reserve.
func defaultAgentStreamCapacityConfig(total int64) agentStreamCapacityConfig {
	if total < 1 || total > tunnel.MaxServerConcurrentStreamsLimit {
		total = tunnel.DefaultServerMaxConcurrentStreams
	}
	totalStreams := int(total)
	controlStreams := 0
	if totalStreams > 1 {
		controlStreams = totalStreams / 16
		if controlStreams < 1 {
			controlStreams = 1
		}
		maximumControlStreams := totalStreams / 64
		if maximumControlStreams < defaultAgentStreamCapacityControlStreams {
			maximumControlStreams = defaultAgentStreamCapacityControlStreams
		}
		if maximumControlStreams > 64 {
			maximumControlStreams = 64
		}
		if controlStreams > maximumControlStreams {
			controlStreams = maximumControlStreams
		}
	}
	publicStreams := totalStreams - controlStreams
	pooledStreams := 0
	reservedPublicForOtherSessions := 0
	if publicStreams > 1 {
		reservedOneShot := (publicStreams + 3) / 4
		pooledStreams = publicStreams - reservedOneShot
		reservedPublicForOtherSessions = reservedOneShot
	}
	maxWaiters := totalStreams
	if maxWaiters < minimumAgentStreamCapacityWaiters {
		maxWaiters = minimumAgentStreamCapacityWaiters
	}
	if maxWaiters > maximumAgentStreamCapacityWaiters {
		maxWaiters = maximumAgentStreamCapacityWaiters
	}
	// A single hot target may use the queue when other targets have no demand.
	// Dispatch remains round-robin across keys; request admission accounts for
	// queued request memory before reaching this layer.
	maxWaitersPerKey := maxWaiters
	maxOpeningPerSession := tunnel.DefaultYamuxConfig(nil).AcceptBacklog
	if maxOpeningPerSession > totalStreams {
		maxOpeningPerSession = totalStreams
	}
	return agentStreamCapacityConfig{
		Total:                          totalStreams,
		Public:                         publicStreams,
		Pooled:                         pooledStreams,
		Control:                        controlStreams,
		MaxWaiters:                     maxWaiters,
		MaxWaitersPerKey:               maxWaitersPerKey,
		MaxOpeningPerSession:           maxOpeningPerSession,
		ReservedPublicForOtherSessions: reservedPublicForOtherSessions,
	}
}

func mustNewDefaultAgentStreamCapacityManager(total int64) *agentStreamCapacityManager {
	manager, err := newAgentStreamCapacityManager(defaultAgentStreamCapacityConfig(total))
	if err != nil {
		// The derived configuration is entirely internal and validated by unit
		// tests. Keep construction total for embedded/test callers with an empty
		// Config, matching the legacy limiter's behavior.
		manager, _ = newAgentStreamCapacityManager(defaultAgentStreamCapacityConfig(tunnel.DefaultServerMaxConcurrentStreams))
	}
	return manager
}
