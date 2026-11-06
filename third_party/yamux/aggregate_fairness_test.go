package yamux

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every healthy reader returns whole units repeatedly while keeping at least
// 3/4 of its window buffered. Occupancy samples therefore remain above half
// throughout the pressured phase; actual read progress must prevent eviction.
func TestAggregatePressureHealthyRefillAndFreshStream(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) { testAggregateHealthyRefill(t, reverse) })
	}
}

func testAggregateHealthyRefill(t *testing.T, reverse bool) {
	const lease = 200 * time.Millisecond
	// Startup allocation is not healthy consumption. Prepare large buffers
	// before the pressure clock starts, and keep the measured phase backlogged.
	payloads := make([][]byte, 4)
	readBuffers := make([][]byte, 4)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte(i + 1)}, 8<<20)
		readBuffers[i] = make([]byte, 0, len(payloads[i]))
	}
	sender, receiver := pressurePair(t, lease)
	if reverse {
		sender, receiver = receiver, sender
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopReads := func() { stopOnce.Do(func() { close(stop) }) }
	defer func() { stopReads(); sender.Close(); receiver.Close(); wg.Wait() }()
	readers := make([]*Stream, 4)
	writes := make(chan error, 4)
	for i := 0; i < 4; i++ {
		writer, err := sender.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := receiver.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		readers[i] = reader
		payload := payloads[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := writer.Write(payload)
			if err == nil && n != len(payload) {
				err = io.ErrShortWrite
			}
			writer.Close()
			writes <- err
		}()
	}
	awaitPressure(t, "healthy holders did not fill pool", func() bool { return receiver.AggregateReceiveWindowStats().FreeUnits == 0 })
	results := make(chan error, 4)
	var phaseBytes [4]atomic.Uint64
	for i, reader := range readers {
		payload := payloads[i]
		progress := &phaseBytes[i]
		readBuffer := readBuffers[i]
		wg.Add(1)
		go func(reader *Stream) {
			defer wg.Done()
			got := readBuffer
			chunk := make([]byte, 128<<10)
			tick := time.NewTicker(25 * time.Millisecond)
			defer tick.Stop()
		paced:
			for {
				select {
				case <-stop:
					break paced
				case <-tick.C:
				}
				if _, err := io.ReadFull(reader, chunk); err != nil {
					results <- err
					return
				}
				got = append(got, chunk...)
				progress.Add(uint64(len(chunk)))
				deadline := time.Now().Add(time.Second)
				for {
					reader.recvLock.Lock()
					units := reader.aggregateUnits
					reader.recvLock.Unlock()
					if units >= 128 {
						break
					}
					if time.Now().After(deadline) {
						results <- fmt.Errorf("healthy refill stopped at %d units", units)
						return
					}
					select {
					case <-stop:
						break paced
					default:
					}
					time.Sleep(time.Millisecond)
				}
			}
			tail, err := io.ReadAll(reader)
			got = append(got, tail...)
			if err == nil && !bytes.Equal(got, payload) {
				err = fmt.Errorf("healthy payload corrupted: %d/%d", len(got), len(payload))
			}
			results <- err
		}(reader)
	}
	fresh, err := sender.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	freshPeer, err := receiver.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	freshDone := make(chan error, 1)
	wg.Add(1)
	go func() { defer wg.Done(); _, err := fresh.Write([]byte("fresh")); freshDone <- err }()
	freshPeer.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(freshPeer, got); err != nil || string(got) != "fresh" {
		t.Fatalf("fresh request starved: %q %v", got, err)
	}
	if err := <-freshDone; err != nil {
		t.Fatal(err)
	}
	// Keep saturation alive well beyond the original eviction period.
	select {
	case err := <-results:
		t.Fatalf("healthy transfer terminated during saturation: %v", err)
	case <-time.After(5 * lease):
	}
	if stats := receiver.AggregateReceiveWindowStats(); stats.PressureResets != 0 {
		t.Fatalf("healthy consumption reset: %+v", stats)
	}
	for i := range phaseBytes {
		if phaseBytes[i].Load() < 1<<20 {
			t.Fatalf("healthy reader %d did not consume multiple full windows: %d", i, phaseBytes[i].Load())
		}
	}
	stopReads()
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
		if err := <-writes; err != nil {
			t.Fatal(err)
		}
	}
	if sender.IsClosed() || receiver.IsClosed() {
		t.Fatal("healthy lane closed")
	}
}

// Deliberately schedule every non-head incumbent first on every refund. The
// fresh writer still gets its turn after exactly four bounded earlier grants.
func TestAggregateSendFairCreditCannotBeStolenByRefill(t *testing.T) {
	config := aggregateTestConfig()
	session := &Session{config: config, aggregate: newAggregateWindow(config)}
	a := session.aggregate
	a.sendWindow = 0
	streams := make([]*Stream, 5)
	for i := range streams {
		streams[i] = newStream(session, uint32(2*i+1), streamEstablished)
		if n, _ := streams[i].reserveAggregateSend(AggregateMaxDataFrameSize); n != 0 {
			t.Fatal("reserved nonexistent credit")
		}
	}
	for turn := 0; turn < 5; turn++ {
		hdr := header(make([]byte, headerSize))
		hdr.encode(typeWindowUpdate, 0, 0, AggregateMaxDataFrameSize)
		if err := session.handleAggregateWindowUpdate(hdr); err != nil {
			t.Fatal(err)
		}
		for attempts := 0; attempts < 20; attempts++ {
			for i := 4; i >= 0; i-- {
				if i == turn {
					continue
				}
				if n, _ := streams[i].reserveAggregateSend(AggregateMaxDataFrameSize); n != 0 {
					t.Fatalf("stream %d stole turn %d: %d", i, turn, n)
				}
			}
		}
		want := AggregateMaxDataFrameSize
		if turn == 4 {
			want = 1
		}
		if n, _ := streams[turn].reserveAggregateSend(want); n != want {
			t.Fatalf("turn %d got %d want %d", turn, n, want)
		}
		if turn < 4 {
			if n, _ := streams[turn].reserveAggregateSend(AggregateMaxDataFrameSize); n != 0 {
				t.Fatal("incumbent bypassed queued fresh writer")
			}
		}
	}
	if a.sendWindow != AggregateMaxDataFrameSize-AggregateWindowUnitSize {
		t.Fatalf("session credit not conserved: %d", a.sendWindow)
	}
	for i, stream := range streams {
		spent := AggregateMaxDataFrameSize
		if i == 4 {
			spent = AggregateWindowUnitSize
		}
		if stream.allocationSendWindow != config.InitialStreamAllocationWindowSize-spent {
			t.Fatal("per-stream credit not conserved")
		}
		stream.cancelAggregateSendWait()
	}
	if a.sendWaitHead != nil || a.sendWaitTail != nil {
		t.Fatal("waiters leaked")
	}
}

func TestAggregateSendFairWaiterTerminalCleanup(t *testing.T) {
	for _, terminal := range []string{"deadline", "close", "peer-reset", "shutdown"} {
		t.Run(terminal, func(t *testing.T) {
			session, stream, conn := ownedSendFixture(t, streamEstablished)
			session.aggregate.sendWindow = 0
			close(conn.releaseFirst)
			go session.send()
			defer session.Close()
			done := make(chan error, 1)
			go func() { _, err := stream.Write([]byte("waiting")); done <- err }()
			awaitPressure(t, "writer did not join FIFO", func() bool {
				session.aggregate.mu.Lock()
				defer session.aggregate.mu.Unlock()
				return session.aggregate.sendWaitHead == stream
			})
			switch terminal {
			case "deadline":
				stream.SetWriteDeadline(time.Now())
			case "close":
				stream.Close()
			case "peer-reset":
				stream.processFlags(flagRST)
			case "shutdown":
				session.Close()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("terminal waiter unexpectedly wrote")
				}
			case <-time.After(time.Second):
				t.Fatal("waiter did not stop")
			}
			session.aggregate.mu.Lock()
			defer session.aggregate.mu.Unlock()
			if stream.aggregateSendQueued || session.aggregate.sendWaitHead != nil || session.aggregate.sendWaitTail != nil {
				t.Fatal("terminal FIFO ownership leaked")
			}
			if session.aggregate.sendWindow != 0 || stream.allocationSendWindow != session.config.InitialStreamAllocationWindowSize {
				t.Fatal("ungranted waiter changed credit")
			}
		})
	}
}

func TestAggregateUsefulConsumptionIsBoundedAndNotBanked(t *testing.T) {
	session, stream, _ := ownedSendFixture(t, streamEstablished)
	// Six actual incoming DATA frames, without a physical sender or publisher.
	for i := 0; i < 6; i++ {
		charge, err := session.reserveAggregateReceive(stream, AggregateMaxDataFrameSize)
		if err != nil {
			t.Fatal(err)
		}
		if _, accepted, err := stream.readAggregateDataPayload(AggregateMaxDataFrameSize, charge, bytes.NewReader(make([]byte, AggregateMaxDataFrameSize))); err != nil || !accepted {
			t.Fatalf("DATA: %t %v", accepted, err)
		}
	}
	// A partial-unit read does not prove any allocation consumption.
	if n, err := stream.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("tiny Read: %d %v", n, err)
	}
	a := session.aggregate
	a.mu.Lock()
	if stream.pressureReadUnits != 0 {
		t.Fatal("byte read renewed unit progress")
	}
	a.mu.Unlock()
	// An unused borrowed unit is returned, with credit, but no useful Read.
	charge, err := session.reserveAggregateReceive(stream, AggregateWindowUnitSize)
	if err != nil {
		t.Fatal(err)
	}
	head, tail, units, err := session.borrowAggregateChunks(charge)
	if err != nil {
		t.Fatal(err)
	}
	session.returnAggregateChunks(stream, head, tail, units, true, false, false)
	a.mu.Lock()
	if stream.pressureReadUnits != 0 {
		t.Fatal("non-consumption refund renewed progress")
	}
	a.mu.Unlock()
	// Complete one chunk through the real Read path (still below byte-update
	// threshold, so this fixture needs no physical send loop).
	if n, err := stream.Read(make([]byte, AggregateWindowUnitSize-1)); n != int(AggregateWindowUnitSize-1) || err != nil {
		t.Fatalf("unit Read: %d %v", n, err)
	}
	a.mu.Lock()
	if stream.pressureReadUnits != 1 {
		t.Fatalf("whole-unit reads counted %d", stream.pressureReadUnits)
	}
	// Exercise counter saturation independently of pool accounting.
	stream.recordAggregateConsumptionLocked(^uint32(0))
	stream.recordAggregateConsumptionLocked(^uint32(0))
	if stream.pressureReadUnits != 32 {
		t.Fatalf("counter overflow: %d", stream.pressureReadUnits)
	}
	now := stream.pressureSince.Add(time.Second)
	stream.sampleAggregateProgressLocked(now, time.Second)
	if stream.pressureStalled || stream.pressureReadUnits != 0 {
		t.Fatal("productive interval not renewed/reset")
	}
	stream.sampleAggregateProgressLocked(now.Add(time.Second), time.Second)
	if !stream.pressureStalled {
		t.Fatal("past consumption banked another interval")
	}
	a.mu.Unlock()
	stream.Reset()
	a.mu.Lock()
	defer a.mu.Unlock()
	if stream.pressureReadUnits != 0 || stream.aggregateBufferTracked {
		t.Fatal("reset retained progress ownership")
	}
}

func TestAggregateSendCancelledHeadWakesEligibleFollower(t *testing.T) {
	config := aggregateTestConfig()
	session := &Session{config: config, aggregate: newAggregateWindow(config)}
	a := session.aggregate
	a.sendWindow = 0
	head := newStream(session, 1, streamEstablished)
	follower := newStream(session, 3, streamEstablished)
	head.reserveAggregateSend(1)
	follower.reserveAggregateSend(1)
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowUpdate, 0, 0, AggregateWindowUnitSize)
	if err := session.handleAggregateWindowUpdate(hdr); err != nil {
		t.Fatal(err)
	}
	// The follower ran first on the refund broadcast and must wait again.
	if n, _ := follower.reserveAggregateSend(1); n != 0 {
		t.Fatal("follower overtook head")
	}
	head.cancelAggregateSendWait()
	select {
	case <-follower.sendNotifyCh:
	case <-time.After(time.Second):
		t.Fatal("head cancellation stranded available credit")
	}
	if n, _ := follower.reserveAggregateSend(1); n != 1 {
		t.Fatal("woken follower did not receive credit")
	}
	if a.sendWindow != 0 || a.sendWaitHead != nil || a.sendWaitTail != nil {
		t.Fatal("cancel/grant did not conserve FIFO and credit")
	}
}

func TestAggregateUsefulReadFencesSelectedPressureEviction(t *testing.T) {
	sender, receiver := pressurePair(t, time.Hour) // deterministic manual selection
	var reader *Stream
	for i := 0; i < 4; i++ {
		writer, err := sender.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		r, err := receiver.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(make([]byte, 512<<10)); err != nil {
			t.Fatal(err)
		}
		reader = r
	}
	a := receiver.aggregate
	a.mu.Lock()
	reader.pressureStalled = true
	a.mu.Unlock() // monitor selected it
	if _, err := io.ReadFull(reader, make([]byte, 128<<10)); err != nil {
		t.Fatal(err)
	}
	if receiver.AggregateReceiveWindowStats().FreeUnits > 128 {
		t.Fatal("test left pressure before recheck")
	}
	if reader.evictAggregatePressure() {
		t.Fatal("selected eviction ignored useful consumption before terminal commit")
	}
	if receiver.AggregateReceiveWindowStats().PressureResets != 0 {
		t.Fatal("productive owner counted as evicted")
	}
}
