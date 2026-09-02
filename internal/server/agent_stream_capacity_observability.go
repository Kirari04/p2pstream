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

	logger := log.Logger
	if a.agentCapacityLogger != nil {
		logger = *a.agentCapacityLogger
	}
	event := logger.Warn().
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
			Int64("oldest_closing_age_ms", snapshot.OldestClosingAgeMillis).
			Bool("adaptive_capacity", snapshot.Adaptive).
			Int("adaptive_admission_limit", snapshot.AdaptiveAdmissionLimit).
			Int("adaptive_raw_admission_limit", snapshot.AdaptiveRawAdmissionLimit).
			Int("adaptive_public_limit", snapshot.AdaptivePublicLimit).
			Int64("adaptive_external_bytes", snapshot.AdaptiveExternalBytes).
			Int64("adaptive_external_fds", snapshot.AdaptiveExternalFDs).
			Int64("public_connections", a.publicConnections.inUse()).
			Uint64("public_connection_limit_rejected", a.publicConnectionLimitRejected.Load()).
			Uint64("public_connection_resource_rejected", a.publicConnectionResourceReject.Load()).
			Uint64("public_client_request_rejected", a.publicClientRequestRejected.Load()).
			Str("memory_pressure", snapshot.MemoryPressure).
			Str("memory_source", snapshot.MemorySource).
			Int64("memory_used_bytes", snapshot.MemoryUsedBytes).
			Int64("memory_limit_bytes", snapshot.MemoryLimitBytes).
			Int64("file_descriptors_used", snapshot.FileDescriptorsUsed).
			Int64("file_descriptors_limit", snapshot.FileDescriptorsLimit).
			Str("resource_pressure_reason", snapshot.ResourcePressureReason).
			Bool("resource_sensor_degraded", snapshot.ResourceSampleError != "")
		if sessionKey != "" {
			event = event.
				Int("selected_session_public_in_use", snapshot.PublicBySession[sessionKey]).
				Int("selected_session_total_in_use", snapshot.TotalBySession[sessionKey]).
				Int("selected_session_limit", snapshot.SessionLimits[sessionKey])
		}
	}
	event.Msg("Agent stream capacity rejected a public request")
}
