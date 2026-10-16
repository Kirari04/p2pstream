//go:build linux && p2pstream_transport_ceiling && p2pstream_http_stage

package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type httpStageFastPathBody struct {
	reader         *bytes.Reader
	writerToCalled atomic.Bool
	closed         atomic.Bool
	closeErr       error
}

func (b *httpStageFastPathBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *httpStageFastPathBody) Close() error {
	b.closed.Store(true)
	return b.closeErr
}

func TestHTTPStageResponsePumpReportsCloseErrorAfterJoin(t *testing.T) {
	want := errors.New("close failed")
	body := &httpStageFastPathBody{reader: bytes.NewReader([]byte("payload")), closeErr: want}
	var released atomic.Bool
	_, _, _, err := ConsumeHTTPStageResponse(context.Background(), body, func() { released.Store(true) })
	if !errors.Is(err, want) {
		t.Fatalf("close error = %v, want %v", err, want)
	}
	if !body.closed.Load() || !released.Load() {
		t.Fatalf("close-error lifecycle closed=%v released=%v", body.closed.Load(), released.Load())
	}
}
func (b *httpStageFastPathBody) WriteTo(io.Writer) (int64, error) {
	b.writerToCalled.Store(true)
	return 0, errors.New("WriterTo fast path must not be used")
}

func TestHTTPStageResponsePumpKeepsBoundedRelay(t *testing.T) {
	payload := bytes.Repeat([]byte("http-stage"), 8193)
	body := &httpStageFastPathBody{reader: bytes.NewReader(payload)}
	var releases atomic.Int32
	n, digest, prefix, err := ConsumeHTTPStageResponse(context.Background(), body, func() { releases.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) || digest == "" || !bytes.Equal(prefix, payload[:len(prefix)]) {
		t.Fatalf("unexpected response result n=%d digest=%q prefix=%d", n, digest, len(prefix))
	}
	if body.writerToCalled.Load() {
		t.Fatal("response pump used body WriterTo")
	}
	if !body.closed.Load() || releases.Load() != 1 {
		t.Fatalf("body/release lifecycle closed=%v releases=%d", body.closed.Load(), releases.Load())
	}
}

type httpStageBlockingBody struct {
	closeCalled   chan struct{}
	allowReadExit chan struct{}
	once          sync.Once
	allowOnce     sync.Once
}

func (b *httpStageBlockingBody) allowReadToExit() {
	b.allowOnce.Do(func() { close(b.allowReadExit) })
}

func (b *httpStageBlockingBody) Read([]byte) (int, error) {
	<-b.allowReadExit
	return 0, context.Canceled
}
func (b *httpStageBlockingBody) Close() error {
	b.once.Do(func() { close(b.closeCalled) })
	return nil
}

func TestHTTPStageResponseCancellationJoinsPumpBeforeRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &httpStageBlockingBody{closeCalled: make(chan struct{}), allowReadExit: make(chan struct{})}
	var released atomic.Bool
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _, _, err := ConsumeHTTPStageResponse(ctx, body, func() {
			released.Store(true)
		})
		done <- err
	}()
	t.Cleanup(func() {
		body.allowReadToExit()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("cancelled response pump leaked after test cleanup")
		}
	})
	cancel()
	select {
	case <-body.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close response body")
	}
	if released.Load() {
		t.Fatal("working owner released before pump read exited")
	}
	body.allowReadToExit()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled response pump returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled response pump did not join")
	}
	if !released.Load() {
		t.Fatal("working owner was not released after joined cancellation")
	}
	if err := body.Close(); err != nil {
		t.Fatalf("idempotent body close: %v", err)
	}
}

func TestHTTPStageEndpointRequiresOrderedResults(t *testing.T) {
	endpoint, err := NewHTTPStageEndpoint(
		"server",
		"0123456789abcdef0123456789abcdef01234567",
		NewTransportCeilingLedger(TransportCeilingPoolLimitBytes),
		NewTransportCeilingLedger(TransportCeilingWorkingLimitBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.RecordResult(HTTPStagePhaseResult{PhaseIndex: 1}); err == nil {
		t.Fatal("out-of-order HTTP stage result accepted")
	}
}

func TestHTTPStageConfigurationPinsNativeTransportBoundaries(t *testing.T) {
	server := httpStageConfiguration("server")
	agent := httpStageConfiguration("agent")
	for role, configuration := range map[string]HTTPStageConfiguration{"server": server, "agent": agent} {
		if configuration.FixedCapacityStreams != HTTPStageFixedCapacity ||
			configuration.HTTPIdleTimeoutNanoseconds != int64(HTTPStageIdleTimeout) ||
			configuration.ResponseHeaderTimeoutNanoseconds != int64(HTTPStageResponseTimeout) ||
			configuration.MaxIdleConnections != HTTPStagePooledCapacity ||
			configuration.MaxIdleConnectionsPerHost != HTTPStagePooledCapacity ||
			configuration.MaxConnectionsPerHost != 0 || configuration.DisableKeepAlives ||
			configuration.ResponsePumpBufferBytes != HTTPStageResponsePumpBytes ||
			configuration.FinalConsumerReadBytes != HTTPStageConsumerReadBytes ||
			configuration.OriginWriteBytes != HTTPStageOriginWriteBytes || configuration.OriginFlush ||
			!configuration.UploadOriginIOCopySHA256 || !configuration.ConditioningClosesWholePool {
			t.Fatalf("%s HTTP stage configuration = %+v", role, configuration)
		}
		if !configuration.TLSYamux.YamuxEnabled || configuration.TLSYamux.DataIncomingHardBound != HTTPStageFixedCapacity && role == "agent" {
			t.Fatalf("%s TLS/Yamux configuration = %+v", role, configuration.TLSYamux)
		}
	}
	if server.TLSYamux.DataIncomingHardBound != -1 || agent.TLSYamux.DataIncomingHardBound != HTTPStageFixedCapacity {
		t.Fatalf("role bounds server=%d agent=%d", server.TLSYamux.DataIncomingHardBound, agent.TLSYamux.DataIncomingHardBound)
	}
}
