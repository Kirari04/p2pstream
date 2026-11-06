package yamux

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkTransportThroughput compares the same Yamux workload over the
// in-memory test pipe, plain loopback TCP, and loopback TCP with TLS. The
// transport-write counter is at Yamux's connection boundary: sendLoop
// currently emits one Write for each frame header and a second Write for its
// body. For TLS cases this counts calls into the TLS API, not TCP syscalls.
func BenchmarkTransportThroughput(b *testing.B) {
	for _, tc := range []struct {
		name     string
		conn     connTypeFunc
		coalesce bool
	}{
		{name: "Pipe", conn: testConnPipe},
		{name: "TCP", conn: testConnTCP},
		{name: "TLS", conn: testConnTLS},
		{name: "TCP_Coalesced", conn: testConnTCP, coalesce: true},
		{name: "TLS_Coalesced", conn: testConnTLS, coalesce: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkTransportThroughput(b, tc.conn, tc.coalesce)
		})
	}
}

// BenchmarkReceiveBufferFrameReuse mirrors Stream.readData's steady-state
// pattern: append one complete frame to recvBuf, then consume it completely.
// It compares the bounded chunked buffer with the bytes.Buffer baseline
// without network scheduling noise. The 4 MiB case exercises the full
// per-stream chunk cache.
func BenchmarkReceiveBufferFrameReuse(b *testing.B) {
	for _, frameSize := range []int{4 << 10, 32 << 10, 128 << 10, 4 << 20} {
		b.Run(fmt.Sprintf("bytes-frame-%dKiB", frameSize>>10), func(b *testing.B) {
			payload := make([]byte, frameSize)
			readBuf := make([]byte, frameSize)
			source := bytes.NewReader(payload)
			var recvBuf bytes.Buffer
			recvBuf.Grow(frameSize)
			b.SetBytes(int64(frameSize))
			b.ReportAllocs()
			source.Reset(payload)
			if n, err := recvBuf.ReadFrom(source); err != nil || n != int64(frameSize) {
				b.Fatalf("warm read n=%d err=%v", n, err)
			}
			if n, err := recvBuf.Read(readBuf); err != nil || n != frameSize {
				b.Fatalf("warm drain n=%d err=%v", n, err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				source.Reset(payload)
				if n, err := recvBuf.ReadFrom(source); err != nil || n != int64(frameSize) {
					b.Fatal(err)
				}
				if n, err := recvBuf.Read(readBuf); err != nil || n != frameSize {
					b.Fatalf("read n=%d err=%v", n, err)
				}
			}
		})
		b.Run(fmt.Sprintf("chunks-frame-%dKiB", frameSize>>10), func(b *testing.B) {
			payload := make([]byte, frameSize)
			readBuf := make([]byte, frameSize)
			source := bytes.NewReader(payload)
			var recvBuf recvBuffer
			b.SetBytes(int64(frameSize))
			b.ReportAllocs()
			if n, err := recvBuf.readFrom(source, int64(frameSize)); err != nil || n != int64(frameSize) {
				b.Fatalf("warm read n=%d err=%v", n, err)
			}
			if n, err := recvBuf.Read(readBuf); err != nil || n != frameSize {
				b.Fatalf("warm drain n=%d err=%v", n, err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				source.Reset(payload)
				if n, err := recvBuf.readFrom(source, int64(frameSize)); err != nil || n != int64(frameSize) {
					b.Fatal(err)
				}
				if n, err := recvBuf.Read(readBuf); err != nil || n != frameSize {
					b.Fatalf("read n=%d err=%v", n, err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(recvBuf.Cap()), "retained-bytes")
		})
	}
}

func benchmarkTransportThroughput(b *testing.B, connFunc connTypeFunc, coalesce bool) {
	const (
		payloadSize = 16 << 20
		chunkSize   = 32 << 10
		maxWindow   = 64 << 20
	)

	rawClient, rawServer := connFunc(b)
	var clientConn io.ReadWriteCloser = rawClient
	var coalescedClient *benchmarkCoalescingConn
	if coalesce {
		coalescedClient = &benchmarkCoalescingConn{ReadWriteCloser: clientConn}
		clientConn = coalescedClient
	}
	countedClient := &benchmarkCountingConn{ReadWriteCloser: clientConn}
	cfg := DefaultConfig()
	cfg.MaxStreamWindowSize = maxWindow
	client, err := Client(countedClient, cfg)
	if err != nil {
		b.Fatal(err)
	}
	server, err := Server(rawServer, cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	writer, err := client.OpenStream()
	if err != nil {
		b.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	payload := make([]byte, payloadSize)
	if err := yamuxBenchmarkTransfer(writer, reader, payload, chunkSize); err != nil {
		b.Fatal(err)
	}
	countedClient.writes.Store(0)
	if coalescedClient != nil {
		coalescedClient.writes.Store(0)
	}

	b.SetBytes(payloadSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := yamuxBenchmarkTransfer(writer, reader, payload, chunkSize); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if bytes := int64(payloadSize) * int64(b.N); bytes > 0 {
		b.ReportMetric(float64(countedClient.writes.Load())/(float64(bytes)/1e6), "transport-writes/MB")
		if coalescedClient != nil {
			b.ReportMetric(float64(coalescedClient.writes.Load())/(float64(bytes)/1e6), "coalesced-transport-writes/MB")
		}
	}
}

func yamuxBenchmarkTransfer(writer io.Writer, reader io.Reader, payload []byte, chunkSize int) error {
	clearDeadline := setYamuxBenchmarkDeadline(writer, reader)
	defer clearDeadline()
	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, chunkSize)
		remaining := len(payload)
		for remaining > 0 {
			want := minInt(remaining, len(buf))
			n, err := io.ReadFull(reader, buf[:want])
			if err != nil {
				errCh <- err
				return
			}
			remaining -= n
		}
		errCh <- nil
	}()
	go func() {
		for offset := 0; offset < len(payload); {
			end := minInt(offset+chunkSize, len(payload))
			if n, err := writer.Write(payload[offset:end]); err != nil {
				errCh <- err
				return
			} else if n != end-offset {
				errCh <- io.ErrShortWrite
				return
			}
			offset = end
		}
		errCh <- nil
	}()
	first := <-errCh
	if first != nil {
		closeYamuxBenchmarkEndpoints(writer, reader)
	}
	second := <-errCh
	if first != nil || second != nil {
		closeYamuxBenchmarkEndpoints(writer, reader)
		return firstOrSecondYamuxError(first, second)
	}
	return nil
}

const yamuxBenchmarkDeadline = 5 * time.Second

type yamuxBenchmarkDeadlineSetter interface {
	SetDeadline(time.Time) error
}

func setYamuxBenchmarkDeadline(writer io.Writer, reader io.Reader) func() {
	deadline := time.Now().Add(yamuxBenchmarkDeadline)
	setters := make([]yamuxBenchmarkDeadlineSetter, 0, 2)
	if setter, ok := writer.(yamuxBenchmarkDeadlineSetter); ok {
		_ = setter.SetDeadline(deadline)
		setters = append(setters, setter)
	}
	if setter, ok := reader.(yamuxBenchmarkDeadlineSetter); ok {
		_ = setter.SetDeadline(deadline)
		setters = append(setters, setter)
	}
	return func() {
		for _, setter := range setters {
			_ = setter.SetDeadline(time.Time{})
		}
	}
}

func closeYamuxBenchmarkEndpoints(writer io.Writer, reader io.Reader) {
	if closer, ok := writer.(io.Closer); ok {
		_ = closer.Close()
	}
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}
}

func firstOrSecondYamuxError(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type benchmarkCountingConn struct {
	io.ReadWriteCloser
	writes atomic.Int64
}

func (c *benchmarkCountingConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.ReadWriteCloser.Write(p)
}

// benchmarkCoalescingConn is a transport-only diagnostic. It combines the
// data frame's 12-byte Yamux header with its immediately following body before
// calling the real connection. Control headers are passed through unchanged.
// This estimates the upper bound from coalescing sendLoop's two writes without
// changing production code or the Yamux wire format.
type benchmarkCoalescingConn struct {
	io.ReadWriteCloser
	pending []byte
	frame   []byte
	writes  atomic.Int64
}

func (c *benchmarkCoalescingConn) Write(p []byte) (int, error) {
	if len(p) == headerSize && p[1] == typeData && binary.BigEndian.Uint32(p[8:12]) > 0 {
		c.pending = append(c.pending[:0], p...)
		return len(p), nil
	}
	if len(c.pending) > 0 {
		if cap(c.frame) < len(c.pending)+len(p) {
			c.frame = make([]byte, len(c.pending)+len(p))
		} else {
			c.frame = c.frame[:len(c.pending)+len(p)]
		}
		copy(c.frame, c.pending)
		copy(c.frame[len(c.pending):], p)
		c.pending = c.pending[:0]
		n, err := c.ReadWriteCloser.Write(c.frame)
		c.writes.Add(1)
		if err != nil {
			return 0, err
		}
		if n != len(c.frame) {
			return 0, io.ErrShortWrite
		}
		return len(p), nil
	}
	n, err := c.ReadWriteCloser.Write(p)
	c.writes.Add(1)
	return n, err
}
