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
