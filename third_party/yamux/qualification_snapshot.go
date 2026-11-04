//go:build p2pstream_qualification

package yamux

import (
	"sync/atomic"
	"time"
)

// QualificationAggregateStreamSnapshotLimit keeps diagnostic work bounded
// even if an authenticated peer has many live streams. Qualification callers
// must treat Truncated as incomplete evidence rather than sampling past it.
const QualificationAggregateStreamSnapshotLimit = 8

// QualificationAggregateStreamSnapshot reports sender credit and the local
// receive limits granted by the growth controller for one stream. It is
// available only in explicitly tagged qualification binaries.
type QualificationAggregateStreamSnapshot struct {
	ID                      uint32
	ByteSendAvailable       uint32
	ByteReceiveLimit        uint32
	AllocationSendAvailable uint32
	AllocationSendLimit     uint32
	AllocationReceiveLimit  uint32
}

// QualificationAggregateSessionSnapshot reports the sender-side credit and
// physical tuple needed to distinguish flow-control waits from TCP stalls.
// Receiver-owned pool state remains available through
// AggregateReceiveWindowStats. AggregateEnabled distinguishes the wire mode:
// legacy sessions still report their byte-send credit, while aggregate
// session and allocation-credit fields remain zero.
type QualificationAggregateSessionSnapshot struct {
	AggregateEnabled   bool
	LocalAddress       string
	RemoteAddress      string
	RTT                time.Duration
	SendAvailable      uint32
	SendLimit          uint32
	SendWakeRegistered bool
	SendQueueLength    int
	ControlQueueLength int
	TotalStreams       int
	CapturedStreams    int
	Truncated          bool
	Streams            [QualificationAggregateStreamSnapshotLimit]QualificationAggregateStreamSnapshot
}

// QualificationAggregateSnapshot returns a fixed-cardinality diagnostic
// snapshot without instrumenting DATA or credit hot paths. Stream pointers are
// copied while holding streamLock, then aggregate sender credit is read under
// the aggregate ledger lock. No stream, state, receive, control, or publish
// lock is held while acquiring the aggregate lock. The separately locked and
// atomic fields are observational samples, not one atomic conservation view;
// a stream closed just after the map copy can appear in this bounded result.
func (s *Session) QualificationAggregateSnapshot() QualificationAggregateSessionSnapshot {
	if s == nil {
		return QualificationAggregateSessionSnapshot{}
	}
	result := QualificationAggregateSessionSnapshot{
		LocalAddress:       s.LocalAddr().String(),
		RemoteAddress:      s.RemoteAddr().String(),
		RTT:                s.RTT(),
		SendQueueLength:    len(s.sendCh),
		ControlQueueLength: len(s.controlCh),
	}
	var streams [QualificationAggregateStreamSnapshotLimit]*Stream
	s.streamLock.Lock()
	result.TotalStreams = len(s.streams)
	for _, stream := range s.streams {
		if result.CapturedStreams == len(streams) {
			break
		}
		streams[result.CapturedStreams] = stream
		result.CapturedStreams++
	}
	s.streamLock.Unlock()
	result.Truncated = result.CapturedStreams < result.TotalStreams

	for i := 0; i < result.CapturedStreams; i++ {
		result.Streams[i].ID = streams[i].id
	}

	if a := s.aggregate; a != nil {
		a.mu.Lock()
		result.AggregateEnabled = true
		result.SendAvailable = a.sendWindow
		result.SendLimit = a.sendLimit
		result.SendWakeRegistered = a.sendNotifyRegistered
		for i := 0; i < result.CapturedStreams; i++ {
			stream := streams[i]
			result.Streams[i].AllocationSendAvailable = stream.allocationSendWindow
			result.Streams[i].AllocationSendLimit = stream.allocationSendLimit
			result.Streams[i].AllocationReceiveLimit = stream.allocationRecvLimit
		}
		a.mu.Unlock()
	}

	for i := 0; i < result.CapturedStreams; i++ {
		result.Streams[i].ByteSendAvailable = atomic.LoadUint32(&streams[i].sendWindow)
		result.Streams[i].ByteReceiveLimit = streams[i].MaxReceiveWindow()
	}
	return result
}
