//go:build p2pstream_qualification

package yamux

import (
	"errors"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func newQualificationAggregateSendCounterFixture() (*Session, *Stream) {
	a := &aggregateWindow{
		sendWindow:  1 << 30,
		sendLimit:   1 << 30,
		sendNotify:  make(chan struct{}),
		publishWake: make(chan struct{}, 1),
		publishDone: make(chan struct{}),
	}
	session := &Session{aggregate: a, config: &Config{MaxDataFrameSize: AggregateMaxDataFrameSize}}
	stream := &Stream{
		session:              session,
		allocationSendWindow: 1 << 30,
		allocationSendLimit:  1 << 30,
	}
	return session, stream
}

func TestQualificationAggregateSendCountersMeasureReservedPayloadAndCharge(t *testing.T) {
	window := aggregateWindow{}
	t.Logf("tagged aggregateWindow size=%d qualification=%d protocolMax=%d", unsafe.Sizeof(window), unsafe.Offsetof(window.qualification), unsafe.Offsetof(window.protocolMax))
	session, stream := newQualificationAggregateSendCounterFixture()
	for _, payload := range []uint32{1, AggregateWindowUnitSize, AggregateWindowUnitSize + 1, AggregateMaxDataFrameSize} {
		reserved, wake := stream.reserveAggregateSend(payload)
		if reserved != payload || wake != nil {
			t.Fatalf("reserve payload=%d returned reserved=%d wake=%v", payload, reserved, wake != nil)
		}
	}
	got := session.DrainQualificationAggregateSendCounters()
	wantPayload := uint64(1 + AggregateWindowUnitSize + AggregateWindowUnitSize + 1 + AggregateMaxDataFrameSize)
	wantCharge := uint64(AggregateWindowUnitSize + AggregateWindowUnitSize + 2*AggregateWindowUnitSize + AggregateMaxDataFrameSize)
	if !got.Enabled || got.ReservedDataFrames != 4 || got.ReservedPayloadBytes != wantPayload || got.ReservedAllocationChargedBytes != wantCharge {
		t.Fatalf("counters=%+v, want frames=4 payload=%d charge=%d", got, wantPayload, wantCharge)
	}
	if got.ByteCreditZeroObservations != 0 || got.StreamAllocationZeroObservations != 0 || got.SessionAllocationZeroObservations != 0 {
		t.Fatalf("successful reservations reported zero credit: %+v", got)
	}
	if reset := session.DrainQualificationAggregateSendCounters(); !reset.Enabled || reset.ReservedDataFrames != 0 || reset.ReservedPayloadBytes != 0 || reset.ReservedAllocationChargedBytes != 0 {
		t.Fatalf("destructive drain did not reset counters: %+v", reset)
	}
	if legacy := (&Session{}).DrainQualificationAggregateSendCounters(); legacy.Enabled {
		t.Fatalf("legacy session reported aggregate counters: %+v", legacy)
	}
}

func TestQualificationAggregateSendCountersObserveWriteByteCreditZero(t *testing.T) {
	session, stream := newQualificationAggregateSendCounterFixture()
	stream.id = 1
	stream.state = streamEstablished
	stream.sendWindow = 0
	stream.writeDeadline.Store(time.Now().Add(-time.Second))
	if _, err := stream.write([]byte{1}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("write error=%v, want timeout after observing empty byte credit", err)
	}
	got := session.DrainQualificationAggregateSendCounters()
	if got.ByteCreditZeroObservations != 1 || got.ReservedDataFrames != 0 {
		t.Fatalf("byte-zero hook counters=%+v", got)
	}
}

func TestQualificationAggregateSendCountersClassifyOverlappingZeroAttempts(t *testing.T) {
	session, stream := newQualificationAggregateSendCounterFixture()

	session.aggregate.sendWindow = 0
	stream.allocationSendWindow = AggregateWindowUnitSize
	if reserved, _ := stream.reserveAggregateSend(1); reserved != 0 {
		t.Fatalf("session-zero reservation=%d", reserved)
	}
	session.aggregate.sendWindow = AggregateWindowUnitSize
	stream.allocationSendWindow = 0
	if reserved, _ := stream.reserveAggregateSend(1); reserved != 0 {
		t.Fatalf("stream-zero reservation=%d", reserved)
	}
	session.aggregate.sendWindow = 0
	stream.allocationSendWindow = 0
	if reserved, _ := stream.reserveAggregateSend(1); reserved != 0 {
		t.Fatalf("both-zero reservation=%d", reserved)
	}
	session.recordQualificationByteCreditZero()
	session.recordQualificationByteCreditZero()

	got := session.DrainQualificationAggregateSendCounters()
	if got.ByteCreditZeroObservations != 2 || got.StreamAllocationZeroObservations != 2 || got.SessionAllocationZeroObservations != 2 {
		t.Fatalf("zero-credit classifications=%+v, want byte=2 stream=2 session=2", got)
	}
}

func TestQualificationAggregateSendCountersConcurrentRecordAndDrain(t *testing.T) {
	session, stream := newQualificationAggregateSendCounterFixture()
	const goroutines = 8
	const frames = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < frames; j++ {
				reserved, _ := stream.reserveAggregateSend(AggregateWindowUnitSize)
				if reserved != AggregateWindowUnitSize {
					t.Errorf("reservation=%d", reserved)
					return
				}
			}
		}()
	}
	wg.Wait()
	got := session.DrainQualificationAggregateSendCounters()
	if got.ReservedDataFrames != goroutines*frames || got.ReservedPayloadBytes != goroutines*frames*uint64(AggregateWindowUnitSize) || got.ReservedAllocationChargedBytes != got.ReservedPayloadBytes {
		t.Fatalf("concurrent counters=%+v", got)
	}
}

func TestQualificationAggregateSendCounterRecordHasNoAllocation(t *testing.T) {
	_, stream := newQualificationAggregateSendCounterFixture()
	allocations := testing.AllocsPerRun(1000, func() {
		reserved, _ := stream.reserveAggregateSend(1)
		if reserved != 1 {
			panic("reservation failed")
		}
	})
	if allocations != 0 {
		t.Fatalf("reservation counter allocations=%f, want zero", allocations)
	}
}
