//go:build linux && p2pstream_qualification

package agent

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

const externalQualificationCreditSnapshotSchemaVersion = 1

type externalQualificationCreditStreamSnapshot struct {
	ID                      uint32 `json:"id"`
	ByteSendAvailable       uint32 `json:"byte_send_available"`
	ByteReceiveLimit        uint32 `json:"byte_receive_limit"`
	AllocationSendAvailable uint32 `json:"allocation_send_available"`
	AllocationSendLimit     uint32 `json:"allocation_send_limit"`
	AllocationReceiveLimit  uint32 `json:"allocation_receive_limit"`
}

type externalQualificationCreditSessionSnapshot struct {
	AggregateEnabled   bool                                        `json:"aggregate_enabled"`
	LocalAddress       string                                      `json:"local_address"`
	RemoteAddress      string                                      `json:"remote_address"`
	RTTNanoseconds     int64                                       `json:"rtt_nanoseconds"`
	SendAvailable      uint32                                      `json:"send_available"`
	SendLimit          uint32                                      `json:"send_limit"`
	SendWakeRegistered bool                                        `json:"send_wake_registered"`
	SendQueueLength    int                                         `json:"send_queue_length"`
	ControlQueueLength int                                         `json:"control_queue_length"`
	TotalStreams       int                                         `json:"total_streams"`
	CapturedStreams    int                                         `json:"captured_streams"`
	Truncated          bool                                        `json:"truncated"`
	Streams            []externalQualificationCreditStreamSnapshot `json:"streams"`
}

type externalQualificationCreditSession struct {
	Lane            int                                        `json:"lane"`
	Aggregate       bool                                       `json:"aggregate"`
	SessionIdentity string                                     `json:"session_identity"`
	Snapshot        externalQualificationCreditSessionSnapshot `json:"snapshot"`
}

type externalQualificationCreditResponse struct {
	SchemaVersion    int                                  `json:"schema_version"`
	CapturedUnixNano int64                                `json:"captured_unix_nano"`
	Sessions         []externalQualificationCreditSession `json:"sessions"`
}

type externalQualificationCreditObservedSession struct {
	lane      int
	aggregate bool
	session   *yamux.Session
}

func writeExternalQualificationCreditSnapshot(w http.ResponseWriter, registry *externalQualificationSessionRegistry) {
	observed := copyExternalQualificationCreditSessions(registry)
	result := externalQualificationCreditResponse{
		SchemaVersion:    externalQualificationCreditSnapshotSchemaVersion,
		CapturedUnixNano: time.Now().UnixNano(),
		Sessions:         make([]externalQualificationCreditSession, 0, len(observed)),
	}
	for _, entry := range observed {
		snapshot := entry.session.QualificationAggregateSnapshot()
		streams := make([]externalQualificationCreditStreamSnapshot, snapshot.CapturedStreams)
		for i := 0; i < snapshot.CapturedStreams; i++ {
			stream := snapshot.Streams[i]
			streams[i] = externalQualificationCreditStreamSnapshot{
				ID: stream.ID, ByteSendAvailable: stream.ByteSendAvailable,
				ByteReceiveLimit:        stream.ByteReceiveLimit,
				AllocationSendAvailable: stream.AllocationSendAvailable,
				AllocationSendLimit:     stream.AllocationSendLimit,
				AllocationReceiveLimit:  stream.AllocationReceiveLimit,
			}
		}
		result.Sessions = append(result.Sessions, externalQualificationCreditSession{
			Lane: entry.lane, Aggregate: entry.aggregate, SessionIdentity: fmt.Sprintf("%p", entry.session),
			Snapshot: externalQualificationCreditSessionSnapshot{
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

func copyExternalQualificationCreditSessions(registry *externalQualificationSessionRegistry) []externalQualificationCreditObservedSession {
	registry.mu.Lock()
	result := make([]externalQualificationCreditObservedSession, 0, len(registry.sessions))
	for lane, observed := range registry.sessions {
		result = append(result, externalQualificationCreditObservedSession{
			lane: lane, aggregate: observed.aggregate, session: observed.session,
		})
	}
	registry.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].lane < result[j].lane })
	return result
}

func TestExternalQualificationCreditSnapshotHasBoundedVersionedSchema(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeExternalQualificationCreditSnapshot(recorder, newExternalQualificationSessionRegistry(5))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	var got externalQualificationCreditResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != externalQualificationCreditSnapshotSchemaVersion || got.CapturedUnixNano <= 0 || len(got.Sessions) != 0 {
		t.Fatalf("credit snapshot = %+v", got)
	}
}
