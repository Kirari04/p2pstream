package yamux

import (
	"fmt"
	"io"
)

// recvChunkSize is deliberately small enough to bound a stream's unused tail,
// while still amortizing allocations for the usual data frame sizes.
const recvChunkSize = 64 * 1024

// Keep up to 4 MiB of drained chunks per stream. This cache is populated only
// by chunks already charged at an earlier receive-window peak; it does not
// advertise or reserve additional protocol credit.
const recvChunkCacheSize = 64

// recvChunk is linked instead of stored in a growing slice. A growing slice
// would temporarily retain (and copy) its old backing array whenever it grows.
type recvChunk struct {
	buf   []byte
	read  int
	write int
	next  *recvChunk
}

// recvChunkAccounting includes the chunk object and allocator rounding in the
// capacity reported by recvBuffer. It is intentionally conservative: the
// exact runtime allocation is an implementation detail, but this keeps the
// reported bound inclusive of per-chunk bookkeeping.
const recvChunkAccounting = 64

// ReceiveWindowMemory returns a conservative upper bound for receive storage
// needed by a stream with the supplied logical receive window. It includes
// payload capacity, per-chunk bookkeeping and head/tail slack. Cached chunks
// stay accounted and are reused before any new allocation: allocating occurs
// only with an empty cache, when live chunks are bounded by the window and
// partial head/tail. Retaining cached chunks cannot increase that peak.
// Callers reserving memory for GrowReceiveWindow can use the difference
// between this value and the previous window's value.
func ReceiveWindowMemory(window uint32) uint64 {
	if window == 0 {
		return 0
	}
	chunks := (uint64(window)+recvChunkSize-1)/recvChunkSize + 2
	return chunks * uint64(recvChunkSize+recvChunkAccounting)
}

type recvBuffer struct {
	head       *recvChunk
	tail       *recvChunk
	spares     *recvChunk
	spareCount uint8
	buffered   int
	allocated  int
}

func (b *recvBuffer) Len() int {
	if b == nil {
		return 0
	}
	return b.buffered
}

// Cap returns the total accounted capacity retained by the receive buffer.
// It includes chunk bookkeeping and is therefore a stricter bound than the
// sum of byte-slice capacities alone.
func (b *recvBuffer) Cap() int {
	if b == nil {
		return 0
	}
	return b.allocated
}

// AllocatedCapacity is intended for package-level diagnostics and tests.
func (b *recvBuffer) AllocatedCapacity() int { return b.Cap() }

// Buffered is intended for package-level diagnostics and tests.
func (b *recvBuffer) Buffered() int { return b.Len() }

func (b *recvBuffer) allocateChunk() *recvChunk {
	c := b.spares
	if c != nil {
		b.spares = c.next
		b.spareCount--
		c.read = 0
		c.write = 0
		c.next = nil
	} else {
		c = &recvChunk{buf: make([]byte, recvChunkSize)}
		b.allocated += recvChunkSize + recvChunkAccounting
	}
	if b.tail == nil {
		b.head = c
	} else {
		b.tail.next = c
	}
	b.tail = c
	return c
}

// releaseDrained drops excess bulk chunks and keeps a bounded per-stream cache
// of drained chunks. The cache avoids repeated allocation for bursty streams;
// Stream.Shrink drops it when an idle stream must release all memory.
func (b *recvBuffer) releaseDrained() {
	for b.head != nil && b.head.read == b.head.write {
		c := b.head
		b.head = c.next
		c.next = nil
		if b.spareCount < recvChunkCacheSize {
			// Keep bounded reusable chunks per stream to avoid allocating a
			// full 64 KiB object for every burst. It remains included in
			// allocated and is dropped by Stream.Shrink.
			c.read = 0
			c.write = 0
			c.next = b.spares
			b.spares = c
			b.spareCount++
		} else {
			b.allocated -= recvChunkSize + recvChunkAccounting
		}
	}
	if b.head == nil {
		b.tail = nil
		if b.spareCount == 0 {
			b.allocated = 0
		}
		b.buffered = 0
	}
}

func (b *recvBuffer) discardEmptyTail() {
	if b.tail == nil || b.tail.read != b.tail.write {
		return
	}
	if b.head == b.tail {
		b.head = nil
		b.tail = nil
	} else {
		prev := b.head
		for prev.next != b.tail {
			prev = prev.next
		}
		prev.next = nil
		b.tail = prev
	}
	b.allocated -= recvChunkSize + recvChunkAccounting
}

func (b *recvBuffer) Read(p []byte) (int, error) {
	if b == nil || b.buffered == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}

	total := 0
	for len(p) > 0 && b.head != nil {
		c := b.head
		n := copy(p, c.buf[c.read:c.write])
		c.read += n
		b.buffered -= n
		total += n
		p = p[n:]
		b.releaseDrained()
	}
	return total, nil
}

// readFrom reads exactly size bytes into the chunk chain. It does not use
// io.Copy because the latter treats an early EOF from a LimitedReader as a
// successful copy, which would leave the next frame header misaligned.
func (b *recvBuffer) readFrom(r io.Reader, size int64) (int64, error) {
	var total int64
	zeroReads := 0
	for total < size {
		if b.tail == nil || b.tail.write == len(b.tail.buf) {
			b.allocateChunk()
		}
		c := b.tail
		available := len(c.buf) - c.write
		remaining := size - total
		if int64(available) > remaining {
			available = int(remaining)
		}

		n, err := r.Read(c.buf[c.write : c.write+available])
		if n < 0 || n > available {
			b.discardEmptyTail()
			return total, fmt.Errorf("yamux: invalid read count %d", n)
		}
		if n > 0 {
			c.write += n
			b.buffered += n
			total += int64(n)
			zeroReads = 0
		} else {
			zeroReads++
		}
		if err != nil {
			if n == 0 {
				b.discardEmptyTail()
			}
			if err == io.EOF && total == size {
				return total, nil
			}
			if err == io.EOF && total < size {
				return total, io.ErrUnexpectedEOF
			}
			return total, err
		}
		if zeroReads >= 100 {
			b.discardEmptyTail()
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}
