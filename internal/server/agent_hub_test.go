package server

import (
	"testing"

	"github.com/google/uuid"
)

func TestLateAgentResponseTrackerRecordsCompletedRequests(t *testing.T) {
	tracker := newLateAgentResponseTracker()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new request id: %v", err)
	}
	tracker.record(id, "agent_timeout")

	reason, ok := tracker.lookup(id)
	if !ok || reason != "agent_timeout" {
		t.Fatalf("late response lookup = %q %v, want agent_timeout true", reason, ok)
	}
	if _, ok := tracker.lookup(uuid.New()); ok {
		t.Fatal("unexpected lookup hit for unknown request")
	}
}
