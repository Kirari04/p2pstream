//go:build p2pstream_qualification

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

const qualificationCreditSnapshotSchemaVersion = 1

type qualificationCreditStreamSnapshot struct {
	ID                      uint32 `json:"id"`
	ByteSendAvailable       uint32 `json:"byte_send_available"`
	ByteReceiveLimit        uint32 `json:"byte_receive_limit"`
	AllocationSendAvailable uint32 `json:"allocation_send_available"`
	AllocationSendLimit     uint32 `json:"allocation_send_limit"`
	AllocationReceiveLimit  uint32 `json:"allocation_receive_limit"`
}

type qualificationCreditSessionSnapshot struct {
	AggregateEnabled   bool                                `json:"aggregate_enabled"`
	LocalAddress       string                              `json:"local_address"`
	RemoteAddress      string                              `json:"remote_address"`
	RTTNanoseconds     int64                               `json:"rtt_nanoseconds"`
	SendAvailable      uint32                              `json:"send_available"`
	SendLimit          uint32                              `json:"send_limit"`
	SendWakeRegistered bool                                `json:"send_wake_registered"`
	SendQueueLength    int                                 `json:"send_queue_length"`
	ControlQueueLength int                                 `json:"control_queue_length"`
	TotalStreams       int                                 `json:"total_streams"`
	CapturedStreams    int                                 `json:"captured_streams"`
	Truncated          bool                                `json:"truncated"`
	Streams            []qualificationCreditStreamSnapshot `json:"streams"`
}

type qualificationCreditSession struct {
	AgentID         int64                              `json:"agent_id"`
	AgentPublicID   string                             `json:"agent_public_id"`
	Lane            int                                `json:"lane"`
	SessionIdentity string                             `json:"session_identity"`
	Snapshot        qualificationCreditSessionSnapshot `json:"snapshot"`
}

type qualificationCreditResponse struct {
	SchemaVersion    int                          `json:"schema_version"`
	CapturedUnixNano int64                        `json:"captured_unix_nano"`
	Sessions         []qualificationCreditSession `json:"sessions"`
}

type qualificationCreditObservedSession struct {
	agentID       int64
	agentPublicID string
	lane          int
	session       *yamux.Session
}

type qualificationGrowthEvent struct {
	SampleUnixNano           int64   `json:"sample_unix_nano"`
	AgentID                  int64   `json:"agent_id,omitempty"`
	AgentPublicID            string  `json:"agent_public_id,omitempty"`
	Lane                     int     `json:"lane"`
	SessionIdentity          string  `json:"session_identity"`
	LocalAddress             string  `json:"local_address,omitempty"`
	RemoteAddress            string  `json:"remote_address,omitempty"`
	StreamID                 uint32  `json:"stream_id"`
	Aggregate                bool    `json:"aggregate"`
	Current                  uint32  `json:"current"`
	Proposed                 uint32  `json:"proposed"`
	Maximum                  uint32  `json:"maximum"`
	RTTNanoseconds           int64   `json:"rtt_nanoseconds"`
	RateBytesPerSecond       float64 `json:"rate_bytes_per_second"`
	Previous                 uint32  `json:"previous"`
	DeniedUntilUnixNano      int64   `json:"denied_until_unix_nano"`
	DenialActiveAfterOutcome bool    `json:"denial_active_after_outcome"`
	Outcome                  string  `json:"outcome"`
}

type qualificationGrowthResponse struct {
	SchemaVersion    int                        `json:"schema_version"`
	CapturedUnixNano int64                      `json:"captured_unix_nano"`
	Capacity         int                        `json:"capacity"`
	DroppedEvents    uint64                     `json:"dropped_events"`
	Events           []qualificationGrowthEvent `json:"events"`
}

func writeQualificationCreditSnapshot(w http.ResponseWriter, app *App, agentIDs []int64) {
	observed := copyQualificationCreditSessions(app, agentIDs)
	result := qualificationCreditResponse{
		SchemaVersion:    qualificationCreditSnapshotSchemaVersion,
		CapturedUnixNano: time.Now().UnixNano(),
		Sessions:         make([]qualificationCreditSession, 0, len(observed)),
	}
	for _, entry := range observed {
		snapshot := entry.session.QualificationAggregateSnapshot()
		streams := make([]qualificationCreditStreamSnapshot, snapshot.CapturedStreams)
		for i := 0; i < snapshot.CapturedStreams; i++ {
			stream := snapshot.Streams[i]
			streams[i] = qualificationCreditStreamSnapshot{
				ID: stream.ID, ByteSendAvailable: stream.ByteSendAvailable,
				ByteReceiveLimit:        stream.ByteReceiveLimit,
				AllocationSendAvailable: stream.AllocationSendAvailable,
				AllocationSendLimit:     stream.AllocationSendLimit,
				AllocationReceiveLimit:  stream.AllocationReceiveLimit,
			}
		}
		result.Sessions = append(result.Sessions, qualificationCreditSession{
			AgentID: entry.agentID, AgentPublicID: entry.agentPublicID,
			Lane: entry.lane, SessionIdentity: fmt.Sprintf("%p", entry.session),
			Snapshot: qualificationCreditSessionSnapshot{
				AggregateEnabled: snapshot.AggregateEnabled,
				LocalAddress:     snapshot.LocalAddress, RemoteAddress: snapshot.RemoteAddress,
				RTTNanoseconds: snapshot.RTT.Nanoseconds(),
				SendAvailable:  snapshot.SendAvailable, SendLimit: snapshot.SendLimit,
				SendWakeRegistered: snapshot.SendWakeRegistered,
				SendQueueLength:    snapshot.SendQueueLength, ControlQueueLength: snapshot.ControlQueueLength,
				TotalStreams: snapshot.TotalStreams, CapturedStreams: snapshot.CapturedStreams,
				Truncated: snapshot.Truncated, Streams: streams,
			},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func writeQualificationGrowthSnapshot(w http.ResponseWriter, app *App, agentIDs []int64) {
	observed := copyQualificationCreditSessions(app, agentIDs)
	bySession := make(map[*yamux.Session]qualificationCreditObservedSession, len(observed))
	for _, entry := range observed {
		bySession[entry.session] = entry
	}
	drain := tunnel.DrainQualificationReceiveWindowGrowthEvents()
	result := qualificationGrowthResponse{
		SchemaVersion:    qualificationCreditSnapshotSchemaVersion,
		CapturedUnixNano: time.Now().UnixNano(), Capacity: drain.Capacity,
		DroppedEvents: drain.DroppedEvents,
		Events:        make([]qualificationGrowthEvent, 0, len(drain.Events)),
	}
	for _, event := range drain.Events {
		lane := -1
		var agentID int64
		var agentPublicID string
		if current, ok := bySession[event.Session]; ok {
			lane, agentID, agentPublicID = current.lane, current.agentID, current.agentPublicID
		}
		localAddress, remoteAddress := qualificationGrowthSessionAddresses(event.Session)
		result.Events = append(result.Events, qualificationGrowthEvent{
			SampleUnixNano: event.SampleUnixNano, AgentID: agentID, AgentPublicID: agentPublicID,
			Lane: lane, SessionIdentity: event.SessionIdentity,
			LocalAddress: localAddress, RemoteAddress: remoteAddress,
			StreamID: event.StreamID, Aggregate: event.Aggregate,
			Current: event.Current, Proposed: event.Proposed, Maximum: event.Maximum,
			RTTNanoseconds: event.RTTNanoseconds, RateBytesPerSecond: event.RateBytesPerSecond,
			Previous: event.Previous, DeniedUntilUnixNano: event.DeniedUntilUnixNano,
			DenialActiveAfterOutcome: event.DenialActiveAfterOutcome, Outcome: event.Outcome,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func qualificationGrowthSessionAddresses(session *yamux.Session) (string, string) {
	if session == nil {
		return "", ""
	}
	return session.LocalAddr().String(), session.RemoteAddr().String()
}

func copyQualificationCreditSessions(app *App, agentIDs []int64) []qualificationCreditObservedSession {
	if app == nil {
		return nil
	}
	result := make([]qualificationCreditObservedSession, 0, len(agentIDs)*5)
	for _, agentID := range agentIDs {
		agent := app.AgentHub.connectedByID(agentID)
		if agent == nil {
			continue
		}
		agent.tunnelMu.RLock()
		for lane, session := range agent.tunnels {
			if session == nil || session.IsClosed() {
				continue
			}
			result = append(result, qualificationCreditObservedSession{
				agentID: agentID, agentPublicID: agent.PublicID, lane: lane, session: session,
			})
		}
		agent.tunnelMu.RUnlock()
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].agentID != result[j].agentID {
			return result[i].agentID < result[j].agentID
		}
		return result[i].lane < result[j].lane
	})
	return result
}

func TestQualificationCreditSnapshotHasBoundedVersionedSchema(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeQualificationCreditSnapshot(recorder, nil, nil)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	var got qualificationCreditResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != qualificationCreditSnapshotSchemaVersion || got.CapturedUnixNano <= 0 || len(got.Sessions) != 0 {
		t.Fatalf("credit snapshot = %+v", got)
	}
}

func TestQualificationGrowthSnapshotHasBoundedVersionedSchema(t *testing.T) {
	_ = tunnel.DrainQualificationReceiveWindowGrowthEvents()
	recorder := httptest.NewRecorder()
	writeQualificationGrowthSnapshot(recorder, nil, nil)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	var got qualificationGrowthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != qualificationCreditSnapshotSchemaVersion || got.CapturedUnixNano <= 0 || got.Capacity != 4096 || got.DroppedEvents != 0 || len(got.Events) != 0 {
		t.Fatalf("growth snapshot = %+v", got)
	}
}
