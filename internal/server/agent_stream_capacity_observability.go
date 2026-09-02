package server

import (
	"time"

	"github.com/rs/zerolog/log"
)

const terminalAgentStreamCapacityLogInterval = time.Second

// logTerminalAgentStreamCapacityFailure emits enough bounded, non-public
// context to diagnose a terminal capacity rejection. Public listener input
// such as host, path, target name, and upstream address is intentionally not
// logged. A global sample interval prevents an attacker from turning capacity
// pressure into unbounded log amplification; exact totals remain available in
// the capacity snapshot.
func (a *App) logTerminalAgentStreamCapacityFailure(
	err error,
	agent *AgentConn,
	target publicRouteTargetConfig,
	requestID string,
) {
	if a == nil {
		return
	}
	now := time.Now()
	nowUnixNano := now.UnixNano()
	last := a.agentCapacityLogLastUnixNano.Load()
	if last != 0 && nowUnixNano-last < terminalAgentStreamCapacityLogInterval.Nanoseconds() {
		a.agentCapacityLogSuppressed.Add(1)
		return
	}
	if !a.agentCapacityLogLastUnixNano.CompareAndSwap(last, nowUnixNano) {
		a.agentCapacityLogSuppressed.Add(1)
		return
	}

	event := log.Warn().
		Str("error_kind", agentProxyErrorKind(err)).
		Str("constraint", agentStreamCapacityConstraintName(err)).
		Int64("route_target_id", target.ID).
		Str("request_id", requestID).
		Uint64("suppressed_since_last", a.agentCapacityLogSuppressed.Swap(0))

	var sessionKey string
	if agent != nil {
		sessionKey = agentStreamCapacitySessionKey(agent, agent.Session)
		event = event.
			Int64("agent_id", agent.AgentID).
			Str("agent_public_id", agent.PublicID).
			Int64("agent_advertised_max_streams", agent.AdvertisedMaxConcurrentStreams).
			Int64("agent_negotiated_max_streams", agent.NegotiatedMaxConcurrentStreams)
	}
	if a.agentStreamCapacity != nil {
		snapshot := a.agentStreamCapacity.snapshot()
		event = event.
			Int("total_in_use", snapshot.Total.InUse).
			Int("total_capacity", snapshot.Total.Capacity).
			Int("public_in_use", snapshot.Public.InUse).
			Int("public_capacity", snapshot.Public.Capacity).
			Int("pooled_in_use", snapshot.Pooled.InUse).
			Int("pooled_capacity", snapshot.Pooled.Capacity).
			Int("health_in_use", snapshot.Control.InUse).
			Int("health_capacity", snapshot.Control.Capacity).
			Int("opening_streams", snapshot.States.Opening).
			Int("live_streams", snapshot.States.Live).
			Int("closing_streams", snapshot.States.Closing).
			Int("capacity_waiters", snapshot.Waiters).
			Int("registered_sessions", snapshot.RegisteredSessions).
			Int64("oldest_closing_age_ms", snapshot.OldestClosingAgeMillis)
		if sessionKey != "" {
			event = event.
				Int("selected_session_public_in_use", snapshot.PublicBySession[sessionKey]).
				Int("selected_session_total_in_use", snapshot.TotalBySession[sessionKey]).
				Int("selected_session_limit", snapshot.SessionLimits[sessionKey])
		}
	}
	event.Msg("Agent stream capacity rejected a public request")
}
