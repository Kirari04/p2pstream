package server

import "p2pstream/internal/tunnel"

const (
	defaultAgentStreamCapacityControlStreams = 4
	defaultAgentStreamCapacityWaiters        = 64
	defaultAgentStreamCapacityWaitersPerKey  = 16
)

// defaultAgentStreamCapacityConfig derives the server-side stream budgets from
// the legacy total while the dedicated server settings are rolled out. The
// budgets are structural: pooled connections can never consume the public
// one-shot headroom, and public work can never consume the trusted health
// reserve.
func defaultAgentStreamCapacityConfig(total int64) agentStreamCapacityConfig {
	if total < 1 || total > tunnel.MaxConcurrentAgentRequestsLimit {
		total = tunnel.DefaultMaxConcurrentAgentRequests
	}
	totalStreams := int(total)
	controlStreams := 0
	if totalStreams > 1 {
		controlStreams = totalStreams / 16
		if controlStreams < 1 {
			controlStreams = 1
		}
		if controlStreams > defaultAgentStreamCapacityControlStreams {
			controlStreams = defaultAgentStreamCapacityControlStreams
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
	maxWaiters := defaultAgentStreamCapacityWaiters
	maxWaitersPerKey := defaultAgentStreamCapacityWaitersPerKey
	if maxWaitersPerKey > maxWaiters {
		maxWaitersPerKey = maxWaiters
	}
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
		manager, _ = newAgentStreamCapacityManager(defaultAgentStreamCapacityConfig(tunnel.DefaultMaxConcurrentAgentRequests))
	}
	return manager
}
