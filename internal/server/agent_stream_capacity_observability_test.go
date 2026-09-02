package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestTerminalAgentStreamCapacityFailureLogReportsConstraintAndRateLimits(t *testing.T) {
	var output bytes.Buffer
	previousLogger := log.Logger
	previousLevel := zerolog.GlobalLevel()
	log.Logger = zerolog.New(&output)
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	t.Cleanup(func() {
		log.Logger = previousLogger
		zerolog.SetGlobalLevel(previousLevel)
	})

	app := NewApp(nil, nil)
	agent := &AgentConn{
		AgentID:                        17,
		PublicID:                       "agent-safe-id",
		AdvertisedMaxConcurrentStreams: 512,
		NegotiatedMaxConcurrentStreams: 256,
	}
	sessionKey := agentStreamCapacitySessionKey(agent, agent.Session)
	app.agentStreamCapacity.registerSessionWithLimit(sessionKey, 256)
	t.Cleanup(func() { app.agentStreamCapacity.unregisterSession(sessionKey) })

	capacityErr := agentDialError{
		Kind: "server_capacity",
		Err:  "capacity unavailable",
		cause: newAgentStreamCapacityAcquireError(
			errors.New("admission timeout"),
			errAgentStreamCapacitySessionBudget,
			"public:route-target:42",
			sessionKey,
		),
	}
	target := publicRouteTargetConfig{
		ID:   42,
		Name: "attacker-controlled-target-name",
		URL:  "http://attacker-controlled.example/private",
	}

	app.logTerminalAgentStreamCapacityFailure(capacityErr, agent, target, "request-safe-id")
	app.logTerminalAgentStreamCapacityFailure(capacityErr, agent, target, "request-suppressed")
	app.agentCapacityLogLastUnixNano.Store(0)
	app.logTerminalAgentStreamCapacityFailure(capacityErr, agent, target, "request-after-interval")

	raw := strings.TrimSpace(output.String())
	if strings.Contains(raw, target.Name) || strings.Contains(raw, target.URL) {
		t.Fatalf("capacity log contains attacker-controlled target input: %s", raw)
	}
	lines := strings.Split(raw, "\n")
	if len(lines) != 2 {
		t.Fatalf("capacity log lines = %d, want 2: %s", len(lines), raw)
	}
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse capacity log %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	first := entries[0]
	if first["message"] != "Agent stream capacity rejected a public request" ||
		first["error_kind"] != "agent_server_capacity" ||
		first["constraint"] != "session_budget" {
		t.Fatalf("capacity log classification = %+v", first)
	}
	if first["route_target_id"] != float64(42) ||
		first["agent_id"] != float64(17) ||
		first["agent_negotiated_max_streams"] != float64(256) ||
		first["total_capacity"] != float64(256) ||
		first["selected_session_limit"] != float64(256) {
		t.Fatalf("capacity log state = %+v", first)
	}
	if entries[1]["suppressed_since_last"] != float64(1) {
		t.Fatalf("suppressed count = %v, want 1", entries[1]["suppressed_since_last"])
	}
}
