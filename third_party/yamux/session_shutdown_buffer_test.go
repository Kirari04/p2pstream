package yamux

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestAggregateSessionShutdownBufferedFINDisposition(t *testing.T) {
	for _, bothFIN := range []bool{false, true} {
		for _, disposition := range []string{"drain", "discard", "race"} {
			t.Run(fmt.Sprintf("bothFIN=%t/%s", bothFIN, disposition), func(t *testing.T) {
				testConnTypes(t, func(t testing.TB, left, right io.ReadWriteCloser) {
					client, server := testClientServerConfig(t, left, right, smallAggregateTestConfig(), smallAggregateTestConfig())
					writer, err := client.OpenStream()
					if err != nil {
						t.Fatal(err)
					}
					reader, err := server.AcceptStream()
					if err != nil {
						t.Fatal(err)
					}
					payload := bytes.Repeat([]byte{0x71}, 5000)
					if n, err := writer.Write(payload); err != nil || n != len(payload) {
						t.Fatal(n, err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					if bothFIN {
						if err := reader.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := client.Ping(); err != nil {
						t.Fatal(err)
					}
					if b, _ := reader.ReceiveBufferStats(); b != uint64(len(payload)) || !reader.ReceiveClosed() {
						t.Fatalf("accepted FIN buffer=%d", b)
					}
					if bothFIN && server.NumStreams() != 0 {
						t.Fatal("FIN/FIN owner remained mapped")
					}
					if disposition == "drain" {
						got, err := io.ReadAll(reader)
						if err != nil || !bytes.Equal(got, payload) {
							t.Fatalf("graceful FIN drain: %d %v", len(got), err)
						}
					}
					type result struct {
						data []byte
						err  error
					}
					done := make(chan result, 1)
					if disposition == "race" {
						go func() { b, e := io.ReadAll(reader); done <- result{b, e} }()
					}
					if err := client.Close(); err != nil {
						t.Fatal(err)
					}
					select {
					case <-server.CloseChan():
					case <-time.After(time.Second):
						t.Fatal("no peer-initiated shutdown")
					}
					if err := server.Close(); err != nil {
						t.Fatal(err)
					}
					if disposition == "race" {
						select {
						case got := <-done:
							if got.err == nil {
								if !bytes.Equal(got.data, payload) {
									t.Fatalf("successful truncated EOF: %d", len(got.data))
								}
							} else if !errors.Is(got.err, ErrSessionShutdown) || !bytes.HasPrefix(payload, got.data) {
								t.Fatalf("racing drain: %d %v", len(got.data), got.err)
							}
						case <-time.After(time.Second):
							t.Fatal("reader did not join")
						}
					} else {
						_, err := reader.Read(make([]byte, 1))
						if disposition == "discard" && !errors.Is(err, ErrSessionShutdown) {
							t.Fatalf("discarded accepted buffer returned %v, want shutdown", err)
						}
						if disposition == "drain" && err != io.EOF {
							t.Fatalf("drained buffer returned %v, want EOF", err)
						}
					}
					if b, a := reader.ReceiveBufferStats(); b != 0 || a != 0 {
						t.Fatalf("retained terminal buffer=%d allocation=%d", b, a)
					}
					if stats := server.AggregateReceiveWindowStats(); !stats.Closed || stats.TotalUnits != 0 {
						t.Fatalf("retained session pool: %+v", stats)
					}
				})
			})
		}
	}
}

// Fully consumed DATA without peer FIN is still an incomplete stream, whereas
// a consumed final FIN remains a valid EOF after the physical session closes.
func TestAggregateSessionShutdownWithoutFINIsNotEOF(t *testing.T) {
	testConnTypes(t, func(t testing.TB, left, right io.ReadWriteCloser) {
		client, server := testClientServerConfig(t, left, right, smallAggregateTestConfig(), smallAggregateTestConfig())
		writer, err := client.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := server.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("incomplete body")
		if _, err := writer.Write(payload); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, payload) {
			t.Fatal(err)
		}
		if _, err := client.Ping(); err != nil {
			t.Fatal(err)
		}
		client.Close()
		select {
		case <-server.CloseChan():
		case <-time.After(time.Second):
			t.Fatal("no natural peer shutdown")
		}
		server.Close()
		if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, ErrSessionShutdown) {
			t.Fatalf("missing FIN: n=%d err=%v", n, err)
		}
		reader.Reset()
		server.Close()
		if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrConnectionReset) {
			t.Fatalf("explicit reset lost precedence: %v", err)
		}
	})
}

func TestAggregateSessionDisposalPreservesTerminalErrorPrecedence(t *testing.T) {
	for _, terminal := range []error{ErrConnectionReset, ErrReceivePressure} {
		session := newAggregateUnitSession(bytes.Repeat([]byte{1}, 5000))
		stream := newStream(session, 1, streamLocalClose)
		session.streams[1] = stream
		hdr := header(make([]byte, headerSize))
		hdr.encode(typeData, flagFIN, 1, 5000)
		if err := session.handleAggregateData(hdr); err != nil {
			t.Fatal(err)
		}
		stream.stateLock.Lock()
		if terminal == ErrConnectionReset {
			stream.state = streamReset
		} else {
			stream.pressureErr = terminal
		}
		stream.stateLock.Unlock()
		session.closeAggregatePool()
		stream.forceClose()
		session.closeAggregatePool()
		if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, terminal) {
			t.Fatalf("terminal error changed: %v want %v", err, terminal)
		}
		if b, a := stream.ReceiveBufferStats(); b != 0 || a != 0 {
			t.Fatal("terminal allocation retained")
		}
	}
}
