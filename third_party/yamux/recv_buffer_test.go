package yamux

import (
	"bytes"
	"io"
	"log"
	"sync"
	"testing"
)

func TestRecvBufferReusesDrainedChunksWhileQueueRemainsBusy(t *testing.T) {
	var buffer recvBuffer
	payload := make([]byte, 2*recvChunkSize)
	source := bytes.NewReader(payload)
	if _, err := buffer.readFrom(source, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	read := make([]byte, recvChunkSize)
	allocations := testing.AllocsPerRun(100, func() {
		if n, err := buffer.Read(read); n != len(read) || err != nil {
			t.Fatalf("read=%d,%v", n, err)
		}
		source.Reset(payload[:recvChunkSize])
		if _, err := buffer.readFrom(source, recvChunkSize); err != nil {
			t.Fatal(err)
		}
		if buffer.Len() != len(payload) {
			t.Fatal("busy queue lost bytes")
		}
		if uint64(buffer.Cap()) > ReceiveWindowMemory(uint32(len(payload))) {
			t.Fatal("busy queue exceeded storage bound")
		}
	})
	if allocations != 0 {
		t.Fatalf("steady busy queue allocated %.2f objects per cycle", allocations)
	}
	if _, err := buffer.Read(payload); err != nil {
		t.Fatal(err)
	}
	if buffer.Cap() != 2*(recvChunkSize+recvChunkAccounting) {
		t.Fatalf("drained queue retained %d bytes", buffer.Cap())
	}
}

func TestRecvBufferBurstDrainRefillCache(t *testing.T) {
	const size = 8 * recvChunkSize
	payload := bytes.Repeat([]byte{0x3c}, size)
	var buffer recvBuffer
	source := bytes.NewReader(payload)
	if n, err := buffer.readFrom(source, size); err != nil || n != size {
		t.Fatalf("initial fill: n=%d err=%v", n, err)
	}
	read := make([]byte, size)
	allocations := testing.AllocsPerRun(100, func() {
		if n, err := buffer.Read(read); n != size || err != nil {
			t.Fatalf("drain: n=%d err=%v", n, err)
		}
		source.Reset(payload)
		if n, err := buffer.readFrom(source, size); err != nil || n != size {
			t.Fatalf("refill: n=%d err=%v", n, err)
		}
	})
	if allocations != 0 {
		t.Fatalf("burst refill allocated %.2f objects per cycle", allocations)
	}
	if buffer.Cap() > int(ReceiveWindowMemory(size)) {
		t.Fatalf("cached burst capacity %d exceeds bound %d", buffer.Cap(), ReceiveWindowMemory(size))
	}
	if n, err := buffer.Read(read); n != size || err != nil {
		t.Fatalf("final drain: n=%d err=%v", n, err)
	}
	if buffer.Cap() > recvChunkCacheSize*(recvChunkSize+recvChunkAccounting) {
		t.Fatalf("final drain retained %d bytes", buffer.Cap())
	}
	stream := &Stream{recvBuf: &buffer}
	stream.Shrink()
	if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
		t.Fatalf("after Shrink: buffered=%d allocated=%d", buffered, allocated)
	}
}

func TestRecvBufferLargeBurstCacheAndExcessBound(t *testing.T) {
	const burstSize = recvChunkCacheSize * recvChunkSize
	payload := bytes.Repeat([]byte{0x5b}, burstSize)
	var buffer recvBuffer
	source := bytes.NewReader(payload)
	if n, err := buffer.readFrom(source, burstSize); err != nil || n != burstSize {
		t.Fatalf("initial 4 MiB fill: n=%d err=%v", n, err)
	}
	read := make([]byte, burstSize)
	allocations := testing.AllocsPerRun(20, func() {
		if n, err := buffer.Read(read); n != burstSize || err != nil {
			t.Fatalf("4 MiB drain: n=%d err=%v", n, err)
		}
		source.Reset(payload)
		if n, err := buffer.readFrom(source, burstSize); err != nil || n != burstSize {
			t.Fatalf("4 MiB refill: n=%d err=%v", n, err)
		}
	})
	if allocations != 0 {
		t.Fatalf("4 MiB burst allocated %.2f objects per cycle", allocations)
	}

	// A burst larger than the cache may allocate temporarily, but excess
	// drained chunks must be released instead of extending retained capacity.
	const excessSize = (recvChunkCacheSize + 4) * recvChunkSize
	excess := bytes.Repeat([]byte{0x6d}, excessSize)
	if n, err := buffer.Read(read); n != burstSize || err != nil {
		t.Fatalf("pre-excess drain: n=%d err=%v", n, err)
	}
	source = bytes.NewReader(excess)
	if n, err := buffer.readFrom(source, excessSize); err != nil || n != excessSize {
		t.Fatalf("excess fill: n=%d err=%v", n, err)
	}
	if n, err := buffer.Read(make([]byte, excessSize)); n != excessSize || err != nil {
		t.Fatalf("excess drain: n=%d err=%v", n, err)
	}
	cacheCapacity := recvChunkCacheSize * (recvChunkSize + recvChunkAccounting)
	if buffer.Cap() > cacheCapacity || uint64(buffer.Cap()) > ReceiveWindowMemory(uint32(excessSize)) {
		t.Fatalf("excess cache capacity=%d cacheBound=%d windowBound=%d", buffer.Cap(), cacheCapacity, ReceiveWindowMemory(uint32(excessSize)))
	}
	stream := &Stream{recvBuf: &buffer}
	stream.Shrink()
	if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
		t.Fatalf("after Shrink: buffered=%d allocated=%d", buffered, allocated)
	}
}

func TestRecvBufferFragmentedHeadTailAndCacheBound(t *testing.T) {
	const window = 8 * recvChunkSize
	payload := bytes.Repeat([]byte{0x6a}, window)
	var buffer recvBuffer
	if n, err := buffer.readFrom(bytes.NewReader(payload), window); err != nil || n != window {
		t.Fatalf("initial fill: n=%d err=%v", n, err)
	}
	partial := make([]byte, 17)
	if n, err := buffer.Read(partial); n != len(partial) || err != nil {
		t.Fatalf("partial head read: n=%d err=%v", n, err)
	}
	// The head is now fragmented and the full tail forces one alignment chunk
	// when this frame is appended. The logical queue remains at window bytes.
	if n, err := buffer.readFrom(bytes.NewReader(partial), int64(len(partial))); err != nil || n != int64(len(partial)) {
		t.Fatalf("tail append: n=%d err=%v", n, err)
	}
	if buffer.Len() != window || buffer.Cap() > int(ReceiveWindowMemory(window)) {
		t.Fatalf("fragmented queue: buffered=%d capacity=%d bound=%d", buffer.Len(), buffer.Cap(), ReceiveWindowMemory(window))
	}
	if n, err := buffer.Read(make([]byte, window)); n != window || err != nil {
		t.Fatalf("fragmented queue drain: n=%d err=%v", n, err)
	}
	if buffer.Cap() > recvChunkCacheSize*(recvChunkSize+recvChunkAccounting) {
		t.Fatalf("cache retained %d bytes after excess chunks were drained", buffer.Cap())
	}
}

type fragmentedRecvReader struct {
	data []byte
	step int
	off  int
}

func (r *fragmentedRecvReader) Read(p []byte) (int, error) {
	if r.off == len(r.data) {
		return 0, io.EOF
	}
	n := len(r.data) - r.off
	if n > r.step {
		n = r.step
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}

func TestRecvBufferFragmentedCrossChunkAndReclaim(t *testing.T) {
	size := 3*recvChunkSize + 123
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i)
	}

	var got recvBuffer
	n, err := got.readFrom(&fragmentedRecvReader{data: want, step: 137}, int64(len(want)))
	if err != nil || n != int64(len(want)) {
		t.Fatalf("readFrom: n=%d err=%v", n, err)
	}
	if got.Buffered() != len(want) {
		t.Fatalf("buffered: got %d want %d", got.Buffered(), len(want))
	}
	if got.AllocatedCapacity() > int(ReceiveWindowMemory(uint32(size))) {
		t.Fatalf("capacity %d exceeds receive-window bound %d", got.AllocatedCapacity(), ReceiveWindowMemory(uint32(size)))
	}

	var read bytes.Buffer
	buf := make([]byte, 997)
	for got.Len() > 0 {
		n, err := got.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		read.Write(buf[:n])
	}
	if !bytes.Equal(read.Bytes(), want) {
		t.Fatal("cross-chunk payload was corrupted")
	}
	if got.Buffered() != 0 || got.AllocatedCapacity() != 4*(recvChunkSize+recvChunkAccounting) {
		t.Fatalf("drained buffer retained buffered=%d capacity=%d", got.Buffered(), got.AllocatedCapacity())
	}
	retained := got.AllocatedCapacity()
	if n, err := got.readFrom(bytes.NewReader([]byte("reuse")), 5); err != nil || n != 5 {
		t.Fatalf("spare reuse: n=%d err=%v", n, err)
	}
	if got.AllocatedCapacity() != retained {
		t.Fatalf("spare reuse allocated %d bytes after retaining %d", got.AllocatedCapacity(), retained)
	}
}

func TestRecvBufferTruncatedFrameRetainsPartialPayload(t *testing.T) {
	want := bytes.Repeat([]byte("payload"), recvChunkSize/len("payload")+3)
	frameSize := len(want) + 17
	var got recvBuffer
	n, err := got.readFrom(bytes.NewReader(want), int64(frameSize))
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("ReadFrom error: got %v want %v", err, io.ErrUnexpectedEOF)
	}
	if n != int64(len(want)) || got.Buffered() != len(want) {
		t.Fatalf("partial frame: n=%d buffered=%d want=%d", n, got.Buffered(), len(want))
	}
	readBack := make([]byte, len(want))
	if n, err := got.Read(readBack); err != nil || n != len(want) || !bytes.Equal(readBack, want) {
		t.Fatalf("partial payload read: n=%d err=%v match=%v", n, err, bytes.Equal(readBack, want))
	}
	if got.AllocatedCapacity() != 2*(recvChunkSize+recvChunkAccounting) {
		t.Fatalf("partial payload drain retained capacity %d", got.AllocatedCapacity())
	}
}

func TestRecvBufferEmptyTruncatedFrameReclaimsTail(t *testing.T) {
	var got recvBuffer
	if n, err := got.readFrom(bytes.NewReader(nil), 1); err != io.ErrUnexpectedEOF || n != 0 {
		t.Fatalf("empty frame: n=%d err=%v", n, err)
	}
	if got.Buffered() != 0 || got.AllocatedCapacity() != 0 {
		t.Fatalf("empty truncated frame retained buffered=%d capacity=%d", got.Buffered(), got.AllocatedCapacity())
	}
}

func TestStreamRejectsOverWindowFrameBeforeAllocation(t *testing.T) {
	stream := &Stream{
		session:    &Session{logger: log.New(io.Discard, "", 0)},
		state:      streamEstablished,
		recvWindow: 4,
	}
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, 0, stream.id, 5)
	body := bytes.NewReader([]byte("12345"))
	if err := stream.readData(hdr, 0, body); err != ErrRecvWindowExceeded {
		t.Fatalf("readData error: got %v want %v", err, ErrRecvWindowExceeded)
	}
	if stream.recvBuf != nil {
		t.Fatal("over-window frame allocated receive storage")
	}
	if body.Len() != 5 {
		t.Fatalf("over-window frame consumed %d bytes", 5-body.Len())
	}
}

func TestStreamReceiveBufferStatsAndShrink(t *testing.T) {
	var got recvBuffer
	if _, err := got.readFrom(bytes.NewReader([]byte("stats")), 5); err != nil {
		t.Fatal(err)
	}
	stream := &Stream{recvBuf: &got}
	buffered, allocated := stream.ReceiveBufferStats()
	if buffered != 5 || allocated != uint64(recvChunkSize+recvChunkAccounting) {
		t.Fatalf("stats: buffered=%d allocated=%d", buffered, allocated)
	}
	readBack := make([]byte, 5)
	stream.recvLock.Lock()
	if n, err := stream.recvBuf.Read(readBack); err != nil || n != len(readBack) {
		stream.recvLock.Unlock()
		t.Fatalf("read: n=%d err=%v", n, err)
	}
	stream.recvLock.Unlock()
	stream.Shrink()
	if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
		t.Fatalf("after Shrink: buffered=%d allocated=%d", buffered, allocated)
	}
}

func TestRecvBufferSlowConsumerBoundAcrossFrames(t *testing.T) {
	const window = 4 * recvChunkSize
	var got recvBuffer
	frame := bytes.Repeat([]byte{0x5a}, 777)
	for i := 0; i < window/len(frame); i++ {
		if n, err := got.readFrom(bytes.NewReader(frame), int64(len(frame))); err != nil || n != int64(len(frame)) {
			t.Fatalf("frame %d: n=%d err=%v", i, n, err)
		}
	}
	if got.Buffered() > window {
		t.Fatalf("buffered=%d exceeds window=%d", got.Buffered(), window)
	}
	if got.AllocatedCapacity() > int(ReceiveWindowMemory(window)) {
		t.Fatalf("capacity %d exceeds receive-window bound %d", got.AllocatedCapacity(), ReceiveWindowMemory(window))
	}
}

func TestRecvBufferConcurrentAccessWithStreamLocking(t *testing.T) {
	// Stream.recvLock serializes the receive loop and application reads. Keep
	// this test close to that contract while exercising fragmented writes and
	// reads concurrently under the same lock.
	var got recvBuffer
	var lock sync.Mutex
	const total = 8 * recvChunkSize
	want := bytes.Repeat([]byte("yamux"), total/5)
	if len(want) < total {
		want = append(want, bytes.Repeat([]byte{'x'}, total-len(want))...)
	}

	readBack := make([]byte, 0, total)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for off := 0; off < len(want); {
			step := 431
			if remain := len(want) - off; remain < step {
				step = remain
			}
			lock.Lock()
			n, err := got.readFrom(bytes.NewReader(want[off:off+step]), int64(step))
			lock.Unlock()
			if err != nil || n != int64(step) {
				t.Errorf("write: n=%d err=%v", n, err)
				return
			}
			off += step
		}
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, 503)
		for len(readBack) < len(want) {
			lock.Lock()
			n, _ := got.Read(buf)
			if n > 0 {
				readBack = append(readBack, buf[:n]...)
			}
			lock.Unlock()
		}
	}()
	wg.Wait()
	if !bytes.Equal(readBack, want) {
		t.Fatal("concurrent receive payload was corrupted")
	}
}
