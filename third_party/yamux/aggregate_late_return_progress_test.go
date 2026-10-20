package yamux

import (
	"sync"
	"testing"
	"time"
)

// This contract is shared by coalesced and eager refund policies. A Read may
// detach its chunks while live, then return them after peer FIN or RST. No
// subsequent Read, incoming frame or progress timer may be needed to refund
// the session; terminal streams must not receive obsolete stream credit.
func TestAggregateLateReturnedChunksAfterTerminalPublishSessionCredit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags uint16
		state streamState
	}{
		{name: "FIN", flags: flagFIN, state: streamRemoteClose},
		{name: "RST", flags: flagRST, state: streamReset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newGatherRecordingConn()
			session := newAggregateUnitSession(nil)
			session.conn = conn
			session.sendCh = make(chan *sendReady, 8)
			session.controlCh = make(chan *sendReady, 8)
			session.sendDoneCh = make(chan struct{})
			loopDone := make(chan error, 1)
			go func() { loopDone <- session.sendLoop() }()
			go session.publishAggregateCredit()

			var stopOnce sync.Once
			var publisherJoined, senderJoined bool
			stop := func() {
				stopOnce.Do(func() {
					close(session.shutdownCh)
					select {
					case <-session.aggregate.publishDone:
						publisherJoined = true
					case <-time.After(time.Second):
						t.Error("aggregate refund publisher did not stop")
					}
					select {
					case err := <-loopDone:
						senderJoined = true
						if err != nil {
							t.Errorf("send loop: %v", err)
						}
					case <-time.After(time.Second):
						t.Error("send loop did not stop")
					}
				})
			}
			t.Cleanup(func() {
				stop()
				if publisherJoined && senderJoined {
					session.closeAggregatePool()
				}
			})

			stream := newStream(session, 1, streamEstablished)
			session.streams[stream.id] = stream
			refundStream := stream.acceptsAggregateData()
			if !refundStream {
				t.Fatal("stream must accept data when chunks are captured")
			}
			byteWindow := stream.recvWindow
			allocationWindow := stream.allocationRecvWindow
			initial := session.AggregateReceiveWindowStats()

			// Model chunks detached by a Read that captured the live state. Keep
			// them outside the stream buffer so terminal discard cannot return them.
			charge, err := session.reserveAggregateReceive(stream, 1)
			if err != nil {
				t.Fatal(err)
			}
			head, tail, units, err := session.borrowAggregateChunks(charge)
			if err != nil {
				t.Fatal(err)
			}
			if charge != AggregateWindowUnitSize || units != 1 || head == nil || tail != head {
				t.Fatalf("captured charge=%d units=%d head=%p tail=%p", charge, units, head, tail)
			}
			if err := stream.processFlags(tc.flags); err != nil {
				t.Fatalf("terminal transition: %v", err)
			}
			stream.stateLock.Lock()
			state := stream.state
			stream.stateLock.Unlock()
			if state != tc.state {
				t.Fatalf("terminal state=%d want=%d", state, tc.state)
			}
			beforeReturn := session.AggregateReceiveWindowStats()
			if beforeReturn.ReceiveAvailable != initial.ReceiveAvailable-charge ||
				beforeReturn.PendingRefund != 0 || beforeReturn.BufferedUnits != 1 ||
				beforeReturn.FreeUnits+1 != initial.FreeUnits {
				t.Fatalf("before late return: %+v initial=%+v", beforeReturn, initial)
			}
			conn.mu.Lock()
			writesBeforeReturn := len(conn.writes)
			conn.mu.Unlock()
			if writesBeforeReturn != 0 {
				t.Fatalf("credit published before captured chunks returned: %d writes", writesBeforeReturn)
			}

			// refundStream=true is deliberately the pre-terminal captured value.
			// No further Read/frame/manual wake follows. The timer only bounds a
			// failure; it cannot notify or publish credit.
			session.returnAggregateChunks(stream, head, tail, units, true, refundStream)
			select {
			case <-conn.wrote:
			case <-time.After(time.Second):
				t.Fatal("late return did not publish session credit")
			}
			stop()
			if !publisherJoined || !senderJoined {
				t.Fatal("cannot inspect ledger before both workers join")
			}

			conn.mu.Lock()
			frames := append([][]byte(nil), conn.writes...)
			conn.mu.Unlock()
			if len(frames) != 1 || len(frames[0]) != headerSize {
				t.Fatalf("late refund physical writes=%d want one header", len(frames))
			}
			hdr := header(frames[0])
			if hdr.Version() != protoVersion || hdr.MsgType() != typeWindowUpdate ||
				hdr.Flags() != 0 || hdr.StreamID() != 0 || hdr.Length() != charge {
				t.Fatalf("late refund frame=%x want only session credit=%d", frames[0], charge)
			}
			if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
				t.Fatalf("terminal stream retained buffer=%d allocated=%d", buffered, allocated)
			}
			a := session.aggregate
			// Both workers have joined; inspect all ledger owners, including the
			// fixed publishing slots, without depending on a policy-specific bit.
			if a.recvWindow != initial.ReceiveAvailable || a.pendingGlobal != 0 || a.publishingGlobal != 0 ||
				a.freeUnits != initial.TotalUnits || a.totalUnits != initial.TotalUnits ||
				a.pendingStreamHead != nil || a.pendingStreamTail != nil ||
				a.publishingRefundsCount != 0 || a.ownerHead != nil || a.poolClosed {
				t.Fatalf("session ledger not conserved: stats=%+v publishing=%d count=%d", session.AggregateReceiveWindowStats(), a.publishingGlobal, a.publishingRefundsCount)
			}
			for i, part := range a.publishingRefunds {
				if part != (aggregateRefundPart{}) {
					t.Fatalf("publishing slot %d retained ownership: %+v", i, part)
				}
			}
			if stream.allocationRecvWindow != allocationWindow-charge || stream.recvWindow != byteWindow ||
				stream.allocationRefundPending != 0 || stream.allocationRefundPublishing != 0 ||
				stream.allocationRefundQueued || stream.allocationRefundNext != nil ||
				stream.aggregateBufferTracked || stream.aggregateOwnerPrev != nil || stream.aggregateOwnerNext != nil {
				t.Fatalf("obsolete stream credit or ownership: allocation=%d want=%d byte=%d want=%d pending=%d publishing=%d queued=%t", stream.allocationRecvWindow, allocationWindow-charge, stream.recvWindow, byteWindow, stream.allocationRefundPending, stream.allocationRefundPublishing, stream.allocationRefundQueued)
			}
			var freeCount uint32
			for chunk := a.free; chunk != nil; chunk = chunk.next {
				if freeCount >= a.totalUnits {
					t.Fatal("free chunk chain repeats or exceeds pool")
				}
				freeCount++
			}
			if freeCount != a.totalUnits {
				t.Fatalf("free chain units=%d want=%d", freeCount, a.totalUnits)
			}
		})
	}
}
