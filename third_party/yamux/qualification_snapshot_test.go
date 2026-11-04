//go:build p2pstream_qualification

package yamux

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestQualificationAggregateSnapshotIsBoundedAndPreservesSenderCredit(t *testing.T) {
	now := time.Now()
	session := &Session{
		streams:   make(map[uint32]*Stream),
		sendCh:    make(chan *sendReady, 5),
		controlCh: make(chan *sendReady, 7),
		aggregate: &aggregateWindow{
			sendWindow:           3 * AggregateWindowUnitSize,
			sendLimit:            9 * AggregateWindowUnitSize,
			sendNotifyRegistered: true,
		},
	}
	session.rttNanos.Store(int64(96 * time.Millisecond))
	session.rttUpdatedNanos.Store(now.UnixNano())
	session.sendCh <- &sendReady{}
	session.controlCh <- &sendReady{}
	session.controlCh <- &sendReady{}

	const total = QualificationAggregateStreamSnapshotLimit + 3
	for id := uint32(1); id <= total; id++ {
		stream := &Stream{
			id:                   id,
			session:              session,
			allocationSendWindow: id * AggregateWindowUnitSize,
			allocationSendLimit:  (id + 20) * AggregateWindowUnitSize,
			allocationRecvLimit:  (id + 40) * AggregateWindowUnitSize,
		}
		atomic.StoreUint32(&stream.sendWindow, id*1024)
		atomic.StoreUint32(&stream.maxReceiveWindow, id*2048)
		session.streams[id] = stream
	}

	got := session.QualificationAggregateSnapshot()
	if !got.AggregateEnabled || got.SendAvailable != 3*AggregateWindowUnitSize || got.SendLimit != 9*AggregateWindowUnitSize || !got.SendWakeRegistered {
		t.Fatalf("session sender credit = %+v", got)
	}
	if got.RTT != 96*time.Millisecond || got.SendQueueLength != 1 || got.ControlQueueLength != 2 {
		t.Fatalf("session timing/queues = %+v", got)
	}
	if got.TotalStreams != total || got.CapturedStreams != QualificationAggregateStreamSnapshotLimit || !got.Truncated {
		t.Fatalf("stream bounds = total %d captured %d truncated %t", got.TotalStreams, got.CapturedStreams, got.Truncated)
	}
	seen := make(map[uint32]struct{}, got.CapturedStreams)
	for _, stream := range got.Streams[:got.CapturedStreams] {
		if stream.ID == 0 {
			t.Fatal("captured zero stream ID")
		}
		if _, duplicate := seen[stream.ID]; duplicate {
			t.Fatalf("captured duplicate stream ID %d", stream.ID)
		}
		seen[stream.ID] = struct{}{}
		if stream.ByteSendAvailable != stream.ID*1024 || stream.ByteReceiveLimit != stream.ID*2048 || stream.AllocationSendAvailable != stream.ID*AggregateWindowUnitSize || stream.AllocationSendLimit != (stream.ID+20)*AggregateWindowUnitSize || stream.AllocationReceiveLimit != (stream.ID+40)*AggregateWindowUnitSize {
			t.Fatalf("stream %d sender credit = %+v", stream.ID, stream)
		}
	}
}

func TestQualificationAggregateSnapshotLegacySessionHasNoAggregateCredit(t *testing.T) {
	session := &Session{
		streams:   make(map[uint32]*Stream),
		sendCh:    make(chan *sendReady, 1),
		controlCh: make(chan *sendReady, 1),
	}
	stream := &Stream{id: 7, session: session}
	atomic.StoreUint32(&stream.sendWindow, 8192)
	atomic.StoreUint32(&stream.maxReceiveWindow, 16384)
	session.streams[stream.id] = stream
	got := session.QualificationAggregateSnapshot()
	if got.AggregateEnabled || got.SendAvailable != 0 || got.SendLimit != 0 || got.SendWakeRegistered {
		t.Fatalf("legacy aggregate credit = %+v", got)
	}
	if got.TotalStreams != 1 || got.CapturedStreams != 1 || got.Truncated {
		t.Fatalf("legacy snapshot = %+v", got)
	}
	if stream := got.Streams[0]; stream.ID != 7 || stream.ByteSendAvailable != 8192 || stream.ByteReceiveLimit != 16384 || stream.AllocationSendAvailable != 0 || stream.AllocationSendLimit != 0 || stream.AllocationReceiveLimit != 0 {
		t.Fatalf("legacy stream sender credit = %+v", stream)
	}
}
