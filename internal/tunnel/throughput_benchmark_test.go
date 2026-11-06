package tunnel

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// BenchmarkTunnelYamuxThroughput uses a loopback TCP connection because the
// relay path runs over TCP. The server starts with the production initial
// window (512 KiB); GrowingConn is allowed to retain credit up to the named
// ceiling. A 16 MiB transfer keeps each timed iteration short while still
// crossing the 512 KiB growth threshold several times.
func BenchmarkTunnelYamuxThroughput(b *testing.B) {
	for _, maximum := range []int64{512 << 10, 4 << 20, 16 << 20, 64 << 20} {
		for _, chunk := range []int{4 << 10, 32 << 10, 128 << 10} {
			name := fmt.Sprintf("max-%dMiB/chunk-%dKiB", maximum>>20, chunk>>10)
			if maximum < 1<<20 {
				name = fmt.Sprintf("max-%dKiB/chunk-%dKiB", maximum>>10, chunk>>10)
			}
			b.Run(name, func(b *testing.B) {
				benchmarkTunnelYamuxThroughput(b, maximum, chunk, chunk)
			})
		}
	}
}

func benchmarkTunnelYamuxThroughput(b *testing.B, maximum int64, readChunk, writeChunk int) {
	const payloadSize = 16 << 20
	payload := make([]byte, payloadSize)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()
	if deadlineListener, ok := listener.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadlineListener.SetDeadline(time.Now().Add(5 * time.Second))
	}

	serverConnCh := make(chan net.Conn, 1)
	acceptErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErrCh <- err
			return
		}
		serverConnCh <- conn
	}()
	clientRaw, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		b.Fatal(err)
	}
	var serverRaw net.Conn
	select {
	case serverRaw = <-serverConnCh:
	case err := <-acceptErrCh:
		b.Fatal(err)
	}
	if deadlineListener, ok := listener.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadlineListener.SetDeadline(time.Time{})
	}

	cfg, err := NewYamuxConfig(nil, DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		b.Fatal(err)
	}
	client, err := yamux.Client(clientRaw, cfg)
	if err != nil {
		b.Fatal(err)
	}
	server, err := yamux.Server(serverRaw, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	defer server.Close()

	writer, err := client.OpenStream()
	if err != nil {
		b.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		b.Fatal(err)
	}
	defer writer.Close()
	defer reader.Close()

	var readConn io.Reader = reader
	if maximum > DefaultAdaptiveReceiveWindowBytes {
		// The reservation is deliberately successful and constant-sized here;
		// this benchmark measures transport cost, not the capacity manager.
		readConn = NewGrowingConn(reader, maximum, func(size int64) (func(), bool) {
			return func() {}, true
		})
	}

	// Warm the transport and estimator before timing steady state. This is a
	// loopback path: the adaptive window may stay well below the configured
	// maximum. WAN growth is qualified by the separate delayed network fixture.
	warmupPayload := payload
	if maximum > int64(len(payload)) {
		warmupPayload = make([]byte, maximum)
	}
	if err := tunnelTransferOnce(writer, readConn, warmupPayload, readChunk, writeChunk); err != nil {
		b.Fatal(err)
	}

	b.SetBytes(payloadSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := tunnelTransferOnce(writer, readConn, payload, readChunk, writeChunk); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(reader.MaxReceiveWindow()), "receive-credit-B")
	_, storage := reader.ReceiveBufferStats()
	b.ReportMetric(float64(storage), "retained-receive-B")
}

func tunnelTransferOnce(writer io.Writer, reader io.Reader, payload []byte, readChunk, writeChunk int) error {
	clearDeadline := setTunnelBenchmarkDeadline(writer, reader)
	defer clearDeadline()
	errCh := make(chan error, 2)
	go func() {
		buf := make([]byte, readChunk)
		remaining := len(payload)
		for remaining > 0 {
			want := min(remaining, len(buf))
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
			end := min(offset+writeChunk, len(payload))
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
		closeTunnelBenchmarkEndpoints(writer, reader)
	}
	second := <-errCh
	if first != nil || second != nil {
		closeTunnelBenchmarkEndpoints(writer, reader)
		return firstOrSecondError(first, second)
	}
	return nil
}

const tunnelBenchmarkDeadline = 5 * time.Second

type tunnelBenchmarkDeadlineSetter interface {
	SetDeadline(time.Time) error
}

func setTunnelBenchmarkDeadline(writer io.Writer, reader io.Reader) func() {
	deadline := time.Now().Add(tunnelBenchmarkDeadline)
	setters := make([]tunnelBenchmarkDeadlineSetter, 0, 2)
	if setter, ok := writer.(tunnelBenchmarkDeadlineSetter); ok {
		_ = setter.SetDeadline(deadline)
		setters = append(setters, setter)
	}
	if setter, ok := reader.(tunnelBenchmarkDeadlineSetter); ok {
		_ = setter.SetDeadline(deadline)
		setters = append(setters, setter)
	}
	return func() {
		for _, setter := range setters {
			_ = setter.SetDeadline(time.Time{})
		}
	}
}

func closeTunnelBenchmarkEndpoints(writer io.Writer, reader io.Reader) {
	if closer, ok := writer.(io.Closer); ok {
		_ = closer.Close()
	}
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}
}

func firstOrSecondError(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

// BenchmarkGrowingConnGrowFastPath isolates the per-read bookkeeping after a
// stream has reached its configured ceiling. It intentionally bypasses I/O;
// the transfer benchmarks above account for the cost of this hook in context.
func BenchmarkGrowingConnGrowFastPath(b *testing.B) {
	left, right := net.Pipe()
	cfg, err := NewYamuxConfig(nil, MaxStreamWindowSizeBytesLimit)
	if err != nil {
		b.Fatal(err)
	}
	client, err := yamux.Client(left, cfg)
	if err != nil {
		b.Fatal(err)
	}
	server, err := yamux.Server(right, cfg)
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
	conn := NewGrowingConn(reader, MaxStreamWindowSizeBytesLimit, func(int64) (func(), bool) {
		return func() {}, true
	})
	if err := reader.GrowReceiveWindow(uint32(MaxStreamWindowSizeBytesLimit)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := conn.grow(32 << 10); err != nil {
			b.Fatal(err)
		}
	}
}
