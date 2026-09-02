package server

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"

	"p2pstream/internal/tunnel"
)

const (
	agentTransportBenchmarkStatus          = http.StatusNoContent
	agentTransportBenchmarkChurnWindowSize = 128
	agentTransportBenchmarkStreamCapacity  = tunnel.MaxAggregateStreamWindowBytesLimit / tunnel.DefaultMaxStreamWindowSizeBytes
)

type agentTransportBenchmarkFixture struct {
	app           *App
	agent         *AgentConn
	fake          *fakeYamuxAgent
	target        publicRouteTargetConfig
	upstream      *httptest.Server
	protocolMajor *atomic.Int64
}

func BenchmarkAgentTransportWarmKeepAliveGET(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	if err := fixture.roundTrip(fixture.target); err != nil {
		b.Fatalf("warm agent transport: %v", err)
	}
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		if err := fixture.roundTrip(fixture.target); err != nil {
			b.Fatalf("warm keepalive round trip: %v", err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportOneShotGET(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	transport := fixture.app.agentTargetOneShotTransport(fixture.agent, fixture.target)
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		if err := fixture.roundTripWithTransport(transport, http.MethodGet, nil); err != nil {
			b.Fatalf("one-shot GET: %v", err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportWarmKeepAlivePOST1KiB(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	payload := bytes.Repeat([]byte("p"), 1024)
	if err := fixture.roundTripBody(fixture.target, http.MethodPost, payload); err != nil {
		b.Fatalf("warm agent POST transport: %v", err)
	}
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		if err := fixture.roundTripBody(fixture.target, http.MethodPost, payload); err != nil {
			b.Fatalf("warm keepalive POST: %v", err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportOneShotPOST1KiB(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	transport := fixture.app.agentTargetOneShotTransport(fixture.agent, fixture.target)
	payload := bytes.Repeat([]byte("p"), 1024)
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		if err := fixture.roundTripWithTransport(transport, http.MethodPost, payload); err != nil {
			b.Fatalf("one-shot POST: %v", err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportWarmHTTP2POST1KiB(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixtureWithOptions(b, agentTransportBenchmarkStreamCapacity, true)
	payload := bytes.Repeat([]byte("h"), 1024)
	if err := fixture.roundTripBody(fixture.target, http.MethodPost, payload); err != nil {
		b.Fatalf("warm agent HTTP/2 POST transport: %v", err)
	}
	if fixture.protocolMajor.Load() != 2 {
		b.Fatalf("upstream protocol major = %d, want HTTP/2", fixture.protocolMajor.Load())
	}
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		if err := fixture.roundTripBody(fixture.target, http.MethodPost, payload); err != nil {
			b.Fatalf("warm HTTP/2 POST: %v", err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportOneShotHTTP2POST1KiB(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixtureWithOptions(b, agentTransportBenchmarkStreamCapacity, true)
	transport := fixture.app.agentTargetOneShotTransport(fixture.agent, fixture.target)
	payload := bytes.Repeat([]byte("s"), 1024)
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		if err := fixture.roundTripWithTransport(transport, http.MethodPost, payload); err != nil {
			b.Fatalf("one-shot HTTP/2 POST: %v", err)
		}
	}
	b.StopTimer()
	if fixture.protocolMajor.Load() != 2 {
		b.Fatalf("upstream protocol major = %d, want HTTP/2", fixture.protocolMajor.Load())
	}
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportParallelHTTP2POST1KiB(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixtureWithOptions(b, agentTransportBenchmarkStreamCapacity, true)
	suppressInfoLogsForBenchmark(b)
	payload := bytes.Repeat([]byte("h"), 1024)
	if err := fixture.ownerRoundTrip(fixture.target, http.MethodPost, payload); err != nil {
		b.Fatalf("warm HTTP/2 owner transport: %v", err)
	}
	initialOpens := fixture.fake.openRequestCount()
	var failures atomic.Int64

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.StartTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := fixture.ownerRoundTrip(fixture.target, http.MethodPost, payload); err != nil {
				failures.Add(1)
			}
		}
	})
	b.StopTimer()
	if got := failures.Load(); got != 0 {
		b.Fatalf("parallel HTTP/2 POST failures = %d", got)
	}
	if fixture.protocolMajor.Load() != 2 {
		b.Fatalf("upstream protocol major = %d, want HTTP/2", fixture.protocolMajor.Load())
	}
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportColdNewTargetGET(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		target := fixture.targetWithID(int64(i) + 10_000)
		if err := fixture.roundTrip(target); err != nil {
			b.Fatalf("cold target round trip %d: %v", i, err)
		}
		fixture.app.AgentTransports.closeRouteTarget(target.ID)
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportWarmColdMix90To10(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	if err := fixture.roundTrip(fixture.target); err != nil {
		b.Fatalf("warm agent transport: %v", err)
	}
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		target := fixture.target
		cold := i%10 == 9
		if cold {
			target = fixture.targetWithID(int64(i) + 100_000)
		}
		if err := fixture.roundTrip(target); err != nil {
			b.Fatalf("mixed round trip %d: %v", i, err)
		}
		if cold {
			fixture.app.AgentTransports.closeRouteTarget(target.ID)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportParallelSameTarget(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	suppressInfoLogsForBenchmark(b)
	if err := fixture.roundTrip(fixture.target); err != nil {
		b.Fatalf("warm agent transport: %v", err)
	}
	initialOpens := fixture.fake.openRequestCount()
	var failures atomic.Int64

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := fixture.ownerRoundTrip(fixture.target, http.MethodGet, nil); err != nil {
				failures.Add(1)
			}
		}
	})
	b.StopTimer()
	if got := failures.Load(); got != 0 {
		b.Fatalf("parallel same-target failures = %d", got)
	}
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportParallelMixedGETPOST(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	suppressInfoLogsForBenchmark(b)
	if err := fixture.roundTrip(fixture.target); err != nil {
		b.Fatalf("warm agent transport: %v", err)
	}
	initialOpens := fixture.fake.openRequestCount()
	payload := bytes.Repeat([]byte("m"), 1024)
	var operations atomic.Int64
	var failures atomic.Int64

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			operation := operations.Add(1)
			method := http.MethodGet
			var body []byte
			if operation%10 == 0 {
				method, body = http.MethodPost, payload
			}
			if err := fixture.ownerRoundTrip(fixture.target, method, body); err != nil {
				failures.Add(1)
			}
		}
	})
	b.StopTimer()
	if got := failures.Load(); got != 0 {
		b.Fatalf("parallel mixed failures = %d", got)
	}
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportZipf128Targets(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	const targetCount = 128
	for index := 0; index < targetCount; index++ {
		if err := fixture.roundTrip(fixture.targetWithID(int64(3_000_000 + index))); err != nil {
			b.Fatalf("warm Zipf target %d: %v", index, err)
		}
	}
	initialOpens := fixture.fake.openRequestCount()
	random := rand.New(rand.NewSource(1))
	zipf := rand.NewZipf(random, 1.2, 1, targetCount-1)

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		target := fixture.targetWithID(int64(3_000_000 + zipf.Uint64()))
		if err := fixture.roundTrip(target); err != nil {
			b.Fatalf("Zipf target round trip: %v", err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
}

func BenchmarkAgentTransportZipf512OverPooledCapacity(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixtureWithOptions(b, tunnel.DefaultMaxConcurrentAgentRequests, false)
	suppressInfoLogsForBenchmark(b)
	const targetCount = 512
	for index := 0; index < targetCount; index++ {
		target := fixture.targetWithID(int64(4_000_000 + index))
		if err := fixture.ownerRoundTrip(target, http.MethodGet, nil); err != nil {
			b.Fatalf("warm over-capacity Zipf target %d: %v", index, err)
		}
	}
	initialOpens := fixture.fake.openRequestCount()
	random := rand.New(rand.NewSource(2))
	zipf := rand.NewZipf(random, 1.2, 1, targetCount-1)

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for range b.N {
		target := fixture.targetWithID(int64(4_000_000 + zipf.Uint64()))
		if err := fixture.ownerRoundTrip(target, http.MethodGet, nil); err != nil {
			b.Fatalf("over-capacity Zipf target round trip: %v", err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
	b.ReportMetric(float64(fixture.app.AgentTransports.len()), "pool_entries")
	if got, max := fixture.app.AgentTransports.len(), fixture.app.agentStreamCapacity.snapshot().Pooled.Capacity; got > max {
		b.Fatalf("retained pool entries = %d, max pooled capacity %d", got, max)
	}
}

func BenchmarkAgentTransportHighCardinalityTargetChurn(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixture(b)
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		target := fixture.targetWithID(int64(i) + 1_000_000)
		if err := fixture.roundTrip(target); err != nil {
			b.Fatalf("high-cardinality round trip %d: %v", i, err)
		}
		if i >= agentTransportBenchmarkChurnWindowSize {
			fixture.app.AgentTransports.closeRouteTarget(target.ID - agentTransportBenchmarkChurnWindowSize)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
	b.ReportMetric(float64(fixture.app.AgentTransports.len()), "pool_entries")
}

func BenchmarkAgentTransportProductionBoundaryChurn(b *testing.B) {
	b.StopTimer()
	fixture := newAgentTransportBenchmarkFixtureWithOptions(b, tunnel.DefaultMaxConcurrentAgentRequests, false)
	suppressInfoLogsForBenchmark(b)
	initialOpens := fixture.fake.openRequestCount()

	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		target := fixture.targetWithID(int64(i) + 2_000_000)
		if err := fixture.ownerRoundTrip(target, http.MethodGet, nil); err != nil {
			b.Fatalf("production-boundary churn round trip %d: %v", i, err)
		}
	}
	b.StopTimer()
	fixture.reportOpensPerOperation(b, initialOpens)
	b.ReportMetric(float64(fixture.app.AgentTransports.len()), "pool_entries")
	if got, max := fixture.app.AgentTransports.len(), fixture.app.agentStreamCapacity.snapshot().Pooled.Capacity; got > max {
		b.Fatalf("retained pool entries = %d, max pooled capacity %d", got, max)
	}
}

func newAgentTransportBenchmarkFixture(b *testing.B) *agentTransportBenchmarkFixture {
	return newAgentTransportBenchmarkFixtureWithOptions(b, agentTransportBenchmarkStreamCapacity, false)
}

func newAgentTransportBenchmarkFixtureWithOptions(b *testing.B, capacity int64, enableHTTP2 bool) *agentTransportBenchmarkFixture {
	b.Helper()
	protocolMajor := &atomic.Int64{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocolMajor.Store(int64(r.ProtoMajor))
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(agentTransportBenchmarkStatus)
	})
	var upstream *httptest.Server
	if enableHTTP2 {
		upstream = httptest.NewUnstartedServer(handler)
		upstream.EnableHTTP2 = true
		upstream.StartTLS()
	} else {
		upstream = httptest.NewServer(handler)
	}
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		upstream.Close()
		b.Fatalf("parse benchmark upstream URL: %v", err)
	}

	app := NewApp(nil, nil)
	// Churn benchmarks deliberately keep more idle agent streams than the
	// production default so they measure transport creation and eviction rather
	// than terminating at the known default-capacity ceiling. This higher test
	// capacity still respects the aggregate Yamux receive-window budget.
	app.agentStreamCapacity = mustNewDefaultAgentStreamCapacityManager(capacity)
	agent, fake := newAgentTransportBenchmarkFakeAgent(b, 7, "agent-transport-benchmark")
	if err := app.AgentHub.connect(agent); err != nil {
		fake.close()
		upstream.Close()
		b.Fatalf("connect benchmark agent: %v", err)
	}

	fixture := &agentTransportBenchmarkFixture{
		app:           app,
		agent:         agent,
		fake:          fake,
		upstream:      upstream,
		protocolMajor: protocolMajor,
		target: publicRouteTargetConfig{
			ID:                            70,
			Name:                          "agent-transport-benchmark",
			Enabled:                       true,
			TargetType:                    publicRouteTargetTypeProxy,
			URL:                           upstream.URL,
			Transport:                     publicRouteTargetTransportAgent,
			TLSSkipVerify:                 enableHTTP2,
			ParsedURL:                     origin,
			UpstreamResponseHeaderTimeout: 2 * time.Second,
		},
	}
	b.Cleanup(func() {
		fixture.app.AgentTransports.closeAll()
		fixture.app.AgentHub.disconnect(fixture.agent)
		fixture.fake.close()
		fixture.upstream.Close()

		done := make(chan struct{})
		go func() {
			fixture.fake.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			b.Errorf("timed out waiting for benchmark fake agent shutdown")
		}
	})
	return fixture
}

func newAgentTransportBenchmarkFakeAgent(b *testing.B, agentID int64, publicID string) (*AgentConn, *fakeYamuxAgent) {
	b.Helper()
	agentConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(agentConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		_ = agentConn.Close()
		_ = serverConn.Close()
		b.Fatalf("benchmark agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		_ = agentSession.Close()
		b.Fatalf("benchmark server yamux session: %v", err)
	}
	fake := &fakeYamuxAgent{
		serverSession: serverSession,
		agentSession:  agentSession,
		requests:      make(chan tunnel.OpenRequest, 1),
	}
	fake.wg.Add(1)
	go fake.acceptLoop()
	return &AgentConn{
		AgentID:     agentID,
		PublicID:    publicID,
		Name:        publicID,
		ConnectedAt: time.Now(),
		Done:        make(chan struct{}),
		Session:     serverSession,
	}, fake
}

func (f *agentTransportBenchmarkFixture) roundTrip(target publicRouteTargetConfig) error {
	return f.roundTripBody(target, http.MethodGet, nil)
}

func (f *agentTransportBenchmarkFixture) roundTripBody(target publicRouteTargetConfig, method string, payload []byte) error {
	return f.roundTripWithTransport(f.app.agentTargetTransport(f.agent, target), method, payload)
}

func (f *agentTransportBenchmarkFixture) roundTripWithTransport(transport http.RoundTripper, method string, payload []byte) error {
	req, err := http.NewRequest(method, f.upstream.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("agent transport returned a nil response")
	}
	if resp.Body != nil {
		_, copyErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if resp.StatusCode != agentTransportBenchmarkStatus {
		return fmt.Errorf("status = %d, want %d", resp.StatusCode, agentTransportBenchmarkStatus)
	}
	return nil
}

func (f *agentTransportBenchmarkFixture) ownerRoundTrip(target publicRouteTargetConfig, method string, payload []byte) error {
	req, err := http.NewRequest(method, f.upstream.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	transport := &publicAgentAttemptRoundTripper{
		app:        f.app,
		resolution: publicRouteResolution{Target: target},
		initial:    f.agent,
		requestID:  "agent-transport-benchmark",
		result:     &publicRetryAttemptResult{},
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("agent transport owner returned a nil response")
	}
	if resp.Body != nil {
		_, copyErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if resp.StatusCode != agentTransportBenchmarkStatus {
		return fmt.Errorf("status = %d, want %d", resp.StatusCode, agentTransportBenchmarkStatus)
	}
	return nil
}

func (f *agentTransportBenchmarkFixture) targetWithID(id int64) publicRouteTargetConfig {
	target := f.target
	target.ID = id
	return target
}

func (f *agentTransportBenchmarkFixture) reportOpensPerOperation(b *testing.B, initial int) {
	b.Helper()
	if b.N == 0 {
		return
	}
	opens := f.fake.openRequestCount() - initial
	b.ReportMetric(float64(opens)/float64(b.N), "opens/op")
}

func suppressInfoLogsForBenchmark(b *testing.B) {
	b.Helper()
	previousLogLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(previousLogLevel) })
}
