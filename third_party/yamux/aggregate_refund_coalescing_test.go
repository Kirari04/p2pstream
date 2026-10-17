package yamux

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type coalescingChunks struct {
	head, tail *aggregateChunk
	units      uint32
}

func coalescingReceive(t *testing.T, session *Session, stream *Stream, length uint32) coalescingChunks {
	t.Helper()
	charge, err := session.reserveAggregateReceive(stream, length)
	if err != nil {
		t.Fatal(err)
	}
	head, tail, units, err := session.borrowAggregateChunks(charge)
	if err != nil {
		t.Fatal(err)
	}
	return coalescingChunks{head, tail, units}
}

func coalescingReturn(session *Session, stream *Stream, chunks coalescingChunks) {
	session.returnAggregateChunks(stream, chunks.head, chunks.tail, chunks.units, true, true)
}

func coalescingWake(t *testing.T, session *Session, want bool) {
	t.Helper()
	select {
	case <-session.aggregate.publishWake:
		if !want {
			t.Fatal("small refund woke publisher while both allowances have room")
		}
	default:
		if want {
			t.Fatal("usable pending credit did not wake publisher")
		}
	}
}

func TestAggregateRefundCoalescingReducesPhysicalWrites(t *testing.T) {
	session, conn := newAggregateRefundPublishTestSession(t)
	session.config = aggregateTestConfig()
	session.config.InitialSessionWindowSize = 8 << 20
	session.config.MaxSessionWindowSize = 8 << 20
	session.config.InitialStreamAllocationWindowSize = 2 << 20
	session.config.MaxStreamAllocationWindowSize = 2 << 20
	session.aggregate = newAggregateWindow(session.config)
	stream := newStream(session, 1, streamEstablished)
	const rounds, readsPerRefund = 4, 16
	for round := 0; round < rounds; round++ {
		for read := 0; read < readsPerRefund; read++ {
			chunks := coalescingReceive(t, session, stream, AggregateMaxDataFrameSize)
			coalescingReturn(session, stream, chunks)
			coalescingWake(t, session, read == readsPerRefund-1)
		}
		work := session.takeAggregateRefundWork()
		if work.count != 1 || work.global != readsPerRefund*uint64(AggregateMaxDataFrameSize) {
			t.Fatalf("refund count=%d global=%d", work.count, work.global)
		}
		if err := session.publishAggregateRefund(work); err != nil {
			t.Fatal(err)
		}
	}
	conn.mu.Lock()
	writes := append([][]byte(nil), conn.writes...)
	conn.mu.Unlock()
	if len(writes) != rounds {
		t.Fatalf("physical writes=%d for %d reads, want %d", len(writes), rounds*readsPerRefund, rounds)
	}
	for _, written := range writes {
		if len(written) != 2*headerSize {
			t.Fatalf("paired refund bytes=%d", len(written))
		}
		streamHeader, globalHeader := header(written[:headerSize]), header(written[headerSize:])
		if streamHeader.MsgType() != typeAllocationWindowUpdate || streamHeader.StreamID() != stream.id ||
			globalHeader.MsgType() != typeWindowUpdate || globalHeader.StreamID() != 0 ||
			streamHeader.Length() != readsPerRefund*AggregateMaxDataFrameSize || globalHeader.Length() != streamHeader.Length() {
			t.Fatalf("incorrect paired refund: %v", written)
		}
	}
	stats := session.AggregateReceiveWindowStats()
	if stats.ReceiveAvailable != stats.ReceiveLimit || stats.FreeUnits != stats.TotalUnits || stats.PendingRefund != 0 || stream.allocationRecvWindow != stream.allocationRecvLimit {
		t.Fatalf("unconserved completed refunds: %+v stream=%d/%d", stats, stream.allocationRecvWindow, stream.allocationRecvLimit)
	}
}

func TestAggregateRefundCoalescingIncomingFrameWakesDeferredStream(t *testing.T) {
	session := newAggregateUnitSession(nil)
	stream := newStream(session, 1, streamEstablished)
	coalescingReturn(session, stream, coalescingReceive(t, session, stream, 1))
	coalescingWake(t, session, false)
	// Stop reading after returning one unit. Later arrivals alone must wake the
	// publisher before allocation exhaustion can strand that already free unit.
	for stream.allocationRecvWindow > stream.allocationRecvLimit/4 {
		_ = coalescingReceive(t, session, stream, AggregateWindowUnitSize)
	}
	coalescingWake(t, session, true)
	work := session.takeAggregateRefundWork()
	if work.count != 1 || work.parts[0].stream != stream || work.global != uint64(AggregateWindowUnitSize) {
		t.Fatalf("deferred stream credit lost: %+v", work)
	}
	session.closeAggregatePool()
}

func TestAggregateRefundCoalescingOtherStreamsExhaustSession(t *testing.T) {
	session := newAggregateUnitSession(nil)
	first := newStream(session, 1, streamEstablished)
	coalescingReturn(session, first, coalescingReceive(t, session, first, 1))
	coalescingWake(t, session, false)
	// The pending stream remains mostly idle. Three different streams consume
	// the session allowance; none owns a pending refund of its own.
	for i := 0; i < 3; i++ {
		other := newStream(session, uint32(3+2*i), streamEstablished)
		for n := uint32(0); n < other.allocationRecvLimit; n += AggregateMaxDataFrameSize {
			_ = coalescingReceive(t, session, other, AggregateMaxDataFrameSize)
		}
	}
	coalescingWake(t, session, true)
	work := session.takeAggregateRefundWork()
	if work.count != 1 || work.parts[0].stream != first || work.global != uint64(AggregateWindowUnitSize) {
		t.Fatalf("idle stream's session refund stranded: %+v", work)
	}
	session.closeAggregatePool()
}

func TestAggregateRefundCoalescingOneUnitProgress(t *testing.T) {
	for _, units := range []uint32{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("units_%d", units), func(t *testing.T) {
			session := newAggregateUnitSession(nil)
			stream := newStream(session, 1, streamEstablished)
			stream.allocationRecvLimit = units * AggregateWindowUnitSize
			stream.allocationRecvWindow = stream.allocationRecvLimit
			coalescingReturn(session, stream, coalescingReceive(t, session, stream, 1))
			coalescingWake(t, session, true)
			work := session.takeAggregateRefundWork()
			if work.global != uint64(AggregateWindowUnitSize) {
				t.Fatalf("refund=%d", work.global)
			}
			session.closeAggregatePool()
		})
	}
}

func TestAggregateRefundCoalescingTailTriggerDrainsBoundedFIFO(t *testing.T) {
	session, conn := newAggregateRefundPublishTestSession(t)
	session.config = aggregateTestConfig()
	session.aggregate = newAggregateWindow(session.config)
	streams := make([]*Stream, aggregateRefundBatchStreams+2)
	for i := range streams {
		streams[i] = newStream(session, uint32(2*i+1), streamEstablished)
		coalescingReturn(session, streams[i], coalescingReceive(t, session, streams[i], 1))
		coalescingWake(t, session, false)
	}
	tail := streams[len(streams)-1]
	for tail.allocationRecvWindow > tail.allocationRecvLimit/4 {
		_ = coalescingReceive(t, session, tail, AggregateWindowUnitSize)
	}
	coalescingWake(t, session, true)
	for _, count := range []uint8{aggregateRefundBatchStreams, 2} {
		work := session.takeAggregateRefundWork()
		if work.count != count || work.global != uint64(count)*uint64(AggregateWindowUnitSize) {
			t.Fatalf("FIFO refund count=%d global=%d", work.count, work.global)
		}
		if err := session.publishAggregateRefund(work); err != nil {
			t.Fatal(err)
		}
	}
	if work := session.takeAggregateRefundWork(); work.count != 0 || work.global != 0 {
		t.Fatalf("extra work: %+v", work)
	}
	for _, stream := range streams {
		if stream.allocationRefundPending != 0 || stream.allocationRefundPublishing != 0 || stream.allocationRefundQueued {
			t.Fatalf("stream %d retained a refund", stream.id)
		}
	}
	conn.mu.Lock()
	count := len(conn.writes)
	conn.mu.Unlock()
	if count != 2 {
		t.Fatalf("physical batches=%d", count)
	}
}

func TestAggregateRefundCoalescingTerminalTailAndRacingRead(t *testing.T) {
	for _, flags := range []uint16{flagFIN, flagRST} {
		for _, lateReturn := range []bool{false, true} {
			t.Run(fmt.Sprintf("flags_%d_late_%t", flags, lateReturn), func(t *testing.T) {
				session := newAggregateUnitSession(nil)
				stream := newStream(session, 1, streamEstablished)
				session.streams[stream.id] = stream
				chunks := coalescingReceive(t, session, stream, 1)
				if !lateReturn {
					coalescingReturn(session, stream, chunks)
					coalescingWake(t, session, false)
				}
				if err := stream.processFlags(flags); err != nil {
					t.Fatal(err)
				}
				if lateReturn {
					// FIN's wake observes no pending refund. A Read which captured live
					// state earlier returns afterward; its stale refundStream=true must
					// still flush, without needing another frame or application operation.
					coalescingWake(t, session, false)
					coalescingReturn(session, stream, chunks)
				}
				coalescingWake(t, session, true)
				if !stream.allocationRefundTerminal {
					t.Fatal("terminal state not recorded in allocation ledger")
				}
				session.closeAggregatePool()
				if stream.allocationRefundPending != 0 || stream.allocationRefundQueued || stream.allocationRefundPublishing != 0 || stream.allocationRefundNext != nil {
					t.Fatal("shutdown retained terminal refund ownership")
				}
			})
		}
	}
}

func TestAggregateRefundCoalescingLocalHalfCloseStillProgresses(t *testing.T) {
	session := newAggregateUnitSession(nil)
	stream := newStream(session, 1, streamLocalClose)
	session.streams[stream.id] = stream
	coalescingReturn(session, stream, coalescingReceive(t, session, stream, 1))
	coalescingWake(t, session, false)
	for stream.allocationRecvWindow > stream.allocationRecvLimit/4 {
		_ = coalescingReceive(t, session, stream, AggregateWindowUnitSize)
	}
	coalescingWake(t, session, true)
	if err := stream.processFlags(flagFIN); err != nil {
		t.Fatal(err)
	}
	coalescingWake(t, session, true)
	if stream.state != streamClosed {
		t.Fatalf("state=%d", stream.state)
	}
	session.closeAggregatePool()
}

func TestAggregateRefundCoalescingConcurrentTinyFrames(t *testing.T) {
	clientConn, serverConn := testConnPipe(t)
	config := aggregateTestConfig()
	client, server := testClientServerConfig(t, clientConn, serverConn, config.Clone(), config.Clone())
	const streams, rounds = 8, 64
	var wg sync.WaitGroup
	errors := make(chan error, streams*2)
	for i := 0; i < streams; i++ {
		writer, err := client.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := server.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		if err := writer.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		if err := reader.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				want := []byte{byte(round)}
				if _, err := writer.Write(want); err != nil {
					errors <- err
					return
				}
				got := make([]byte, 1)
				if _, err := io.ReadFull(writer, got); err != nil {
					errors <- err
					return
				}
				if !bytes.Equal(got, want) {
					errors <- fmt.Errorf("echo %v want %v", got, want)
					return
				}
			}
			if err := writer.Close(); err != nil {
				errors <- err
			}
		}()
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				got := make([]byte, 1)
				if _, err := io.ReadFull(reader, got); err != nil {
					errors <- err
					return
				}
				if _, err := reader.Write(got); err != nil {
					errors <- err
					return
				}
			}
			if _, err := reader.Read(make([]byte, 1)); err != io.EOF {
				errors <- fmt.Errorf("final read=%v want EOF", err)
				return
			}
			if err := reader.Close(); err != nil {
				errors <- err
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
