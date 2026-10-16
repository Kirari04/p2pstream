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

	"p2pstream/internal/tunnel"
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

type externalQualificationGrowthEvent struct {
	SampleUnixNano           int64   `json:"sample_unix_nano"`
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

type externalQualificationGrowthResponse struct {
	SchemaVersion    int                                `json:"schema_version"`
	CapturedUnixNano int64                              `json:"captured_unix_nano"`
	Capacity         int                                `json:"capacity"`
	DroppedEvents    uint64                             `json:"dropped_events"`
	Events           []externalQualificationGrowthEvent `json:"events"`
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

func writeExternalQualificationGrowthSnapshot(w http.ResponseWriter, registry *externalQualificationSessionRegistry) {
	observed := copyExternalQualificationCreditSessions(registry)
	lanes := make(map[*yamux.Session]int, len(observed))
	for _, entry := range observed {
		lanes[entry.session] = entry.lane
	}
	drain := tunnel.DrainQualificationReceiveWindowGrowthEvents()
	result := externalQualificationGrowthResponse{
		SchemaVersion:    externalQualificationCreditSnapshotSchemaVersion,
		CapturedUnixNano: time.Now().UnixNano(), Capacity: drain.Capacity,
		DroppedEvents: drain.DroppedEvents,
		Events:        make([]externalQualificationGrowthEvent, 0, len(drain.Events)),
	}
	for _, event := range drain.Events {
		lane := -1
		if current, ok := lanes[event.Session]; ok {
			lane = current
		}
		localAddress, remoteAddress := externalQualificationSessionAddresses(event.Session)
		result.Events = append(result.Events, externalQualificationGrowthEvent{
			SampleUnixNano: event.SampleUnixNano, Lane: lane,
			SessionIdentity: event.SessionIdentity, LocalAddress: localAddress, RemoteAddress: remoteAddress,
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

func externalQualificationSessionAddresses(session *yamux.Session) (string, string) {
	if session == nil {
		return "", ""
	}
	return session.LocalAddr().String(), session.RemoteAddr().String()
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

func TestExternalQualificationGrowthSnapshotHasBoundedVersionedSchema(t *testing.T) {
	_ = tunnel.DrainQualificationReceiveWindowGrowthEvents()
	recorder := httptest.NewRecorder()
	writeExternalQualificationGrowthSnapshot(recorder, newExternalQualificationSessionRegistry(5))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	var got externalQualificationGrowthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != externalQualificationCreditSnapshotSchemaVersion || got.CapturedUnixNano <= 0 || got.Capacity != 4096 || got.DroppedEvents != 0 || len(got.Events) != 0 {
		t.Fatalf("growth snapshot = %+v", got)
	}
}
