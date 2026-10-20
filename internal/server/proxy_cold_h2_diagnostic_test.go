//go:build linux

package server

// Opt-in client instrumentation for a finite, separately labeled cold-H2
// diagnostic. It does not change production listeners or original cold gates.
import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const coldH2DiagnosticConnectionLimit = 8192
const coldH2DiagnosticBucketCount = 121 // 120 x 250 ms, then [30s, infinity).
const coldH2DiagnosticBucketWidth = 250 * time.Millisecond

type coldH2DiagnosticCounter struct {
	ConnectStart atomic.Int64
	ConnectDone  atomic.Int64
	ConnectError atomic.Int64
	TLSStart     atomic.Int64
	TLSDone      atomic.Int64
	TLSError     atomic.Int64
	TLSH2        atomic.Int64
	GotConn      atomic.Int64
	Reused       atomic.Int64
	WasIdle      atomic.Int64
}

type coldH2DiagnosticCounts struct {
	ConnectStart int64 `json:"tcp_connect_start"`
	ConnectDone  int64 `json:"tcp_connect_done"`
	ConnectError int64 `json:"tcp_connect_error"`
	TLSStart     int64 `json:"tls_start"`
	TLSDone      int64 `json:"tls_done"`
	TLSError     int64 `json:"tls_error"`
	TLSH2        int64 `json:"tls_alpn_h2"`
	GotConn      int64 `json:"got_conn"`
	Reused       int64 `json:"got_conn_reused"`
	WasIdle      int64 `json:"got_conn_was_idle"`
}

func (c *coldH2DiagnosticCounter) snapshot() coldH2DiagnosticCounts {
	return coldH2DiagnosticCounts{c.ConnectStart.Load(), c.ConnectDone.Load(), c.ConnectError.Load(), c.TLSStart.Load(), c.TLSDone.Load(), c.TLSError.Load(), c.TLSH2.Load(), c.GotConn.Load(), c.Reused.Load(), c.WasIdle.Load()}
}

type coldH2DiagnosticConnection struct {
	ID     int          `json:"id"`
	Uses   atomic.Int64 `json:"-"`
	Reused atomic.Int64 `json:"-"`
	Active atomic.Int64 `json:"-"`
	Peak   atomic.Int64 `json:"-"`
	H2     atomic.Bool  `json:"-"`
}

type coldH2DiagnosticConnectionResult struct {
	ID          int   `json:"id"`
	Uses        int64 `json:"got_conn"`
	Reused      int64 `json:"reused"`
	Outstanding int64 `json:"outstanding_to_client_do_return"`
	Peak        int64 `json:"peak_concurrent_to_client_do_return"`
	H2          bool  `json:"alpn_h2"`
}

type coldH2Diagnostic struct {
	observationMu      sync.RWMutex
	closed             bool // guarded by observationMu; post-observation callbacks are ignored.
	cap                int
	started            time.Time
	resultPath         string
	buckets            [coldH2DiagnosticBucketCount]coldH2DiagnosticCounter
	connectActive      atomic.Int64
	connectPeak        atomic.Int64
	tlsActive          atomic.Int64
	tlsPeak            atomic.Int64
	requestActive      atomic.Int64
	requestPeak        atomic.Int64
	connectionOverflow atomic.Int64
	mu                 sync.Mutex
	connections        map[net.Conn]*coldH2DiagnosticConnection // Hard bound, never per-request storage.
}

func qualificationConfigureColdH2Diagnostic(t *testing.T, transport *http.Transport, scenario, protocol string, concurrency, requests int) *coldH2Diagnostic {
	t.Helper()
	raw := os.Getenv("P2PSTREAM_COLD_H2_DIAGNOSTIC_MAX_CONNS")
	if raw == "" {
		return nil
	}
	cap, err := strconv.Atoi(raw)
	if err != nil || (cap != 64 && cap != 3072) {
		t.Fatal("cold-H2 diagnostic cap must be exactly 64 or 3072")
	}
	// The existing runner's recovery and lifecycle rows use unchanged clients.
	if scenario == "recovery" || scenario == "lifecycle-repopulate" || os.Getenv("P2PSTREAM_QUALIFICATION_PATH") != "agent-external" {
		return nil
	}
	if scenario != "capacity-3072-h2" || protocol != "h2" || concurrency != 3072 || requests != 3072 || os.Getenv("P2PSTREAM_QUALIFICATION_CAPACITY_ESTABLISHED_H2") == "1" || os.Getenv("P2PSTREAM_QUALIFICATION_H2_PREWARM") == "1" || !coldH2DiagnosticZeroEnv("P2PSTREAM_QUALIFICATION_DURATION_SECONDS") || !coldH2DiagnosticZeroEnv("P2PSTREAM_QUALIFICATION_WARMUP_SECONDS") || !coldH2DiagnosticZeroEnv("P2PSTREAM_QUALIFICATION_OFFERED_RPS") {
		t.Fatal("cold-H2 diagnostic requires exactly 3072 cold finite remote-agent H2 requests without warmup")
	}
	if runtime.Version() != "go1.26.6" {
		t.Fatalf("recheck source-derived HTTP/2 limits before using toolchain %s", runtime.Version())
	}
	transport.MaxConnsPerHost = cap
	d := &coldH2Diagnostic{cap: cap, started: time.Now(), resultPath: requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_RESULT"), connections: make(map[net.Conn]*coldH2DiagnosticConnection)}
	return d
}

func coldH2DiagnosticZeroEnv(key string) bool {
	value := os.Getenv(key)
	return value == "" || value == "0"
}

func coldH2DiagnosticPeak(peak *atomic.Int64, current int64) {
	for previous := peak.Load(); current > previous; previous = peak.Load() {
		if peak.CompareAndSwap(previous, current) {
			return
		}
	}
}

func (d *coldH2Diagnostic) observe(update func()) {
	d.observationMu.RLock()
	defer d.observationMu.RUnlock()
	if !d.closed {
		update()
	}
}

func (d *coldH2Diagnostic) freezeObservation() {
	d.observationMu.Lock()
	d.closed = true
	d.observationMu.Unlock()
}

func (d *coldH2Diagnostic) bucket() *coldH2DiagnosticCounter {
	index := min(max(int(time.Since(d.started)/coldH2DiagnosticBucketWidth), 0), coldH2DiagnosticBucketCount-1)
	return &d.buckets[index]
}

func (d *coldH2Diagnostic) connection(info httptrace.GotConnInfo) *coldH2DiagnosticConnection {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.connections[info.Conn]; c != nil {
		return c
	}
	if len(d.connections) == coldH2DiagnosticConnectionLimit {
		d.connectionOverflow.Add(1)
		return nil
	}
	c := &coldH2DiagnosticConnection{ID: len(d.connections) + 1}
	d.connections[info.Conn] = c
	return c
}

func (d *coldH2Diagnostic) traceRequest(req *http.Request) (*http.Request, func()) {
	if d == nil {
		return req, func() {}
	}
	// Transport retries can invoke GotConn more than once. Hold only the current
	// assignment; the interval ends at client.Do return, not body completion.
	var assignmentMu sync.Mutex
	var assigned *coldH2DiagnosticConnection
	release := func() {
		if assigned != nil {
			assigned.Active.Add(-1)
			d.requestActive.Add(-1)
			assigned = nil
		}
	}
	done := func() { d.observe(func() { assignmentMu.Lock(); defer assignmentMu.Unlock(); release() }) }
	trace := &httptrace.ClientTrace{
		ConnectStart: func(_, _ string) {
			d.observe(func() {
				d.bucket().ConnectStart.Add(1)
				coldH2DiagnosticPeak(&d.connectPeak, d.connectActive.Add(1))
			})
		},
		ConnectDone: func(_, _ string, err error) {
			d.observe(func() {
				d.bucket().ConnectDone.Add(1)
				if err != nil {
					d.bucket().ConnectError.Add(1)
				}
				d.connectActive.Add(-1)
			})
		},
		TLSHandshakeStart: func() {
			d.observe(func() {
				d.bucket().TLSStart.Add(1)
				coldH2DiagnosticPeak(&d.tlsPeak, d.tlsActive.Add(1))
			})
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			d.observe(func() {
				d.bucket().TLSDone.Add(1)
				if err != nil {
					d.bucket().TLSError.Add(1)
				} else if state.NegotiatedProtocol == "h2" {
					d.bucket().TLSH2.Add(1)
				}
				d.tlsActive.Add(-1)
			})
		},
		GotConn: func(info httptrace.GotConnInfo) {
			d.observe(func() {
				d.bucket().GotConn.Add(1)
				if info.Reused {
					d.bucket().Reused.Add(1)
				}
				if info.WasIdle {
					d.bucket().WasIdle.Add(1)
				}
				c := d.connection(info)
				assignmentMu.Lock()
				defer assignmentMu.Unlock()
				release()
				if c == nil {
					return
				}
				assigned = c
				c.Uses.Add(1)
				if info.Reused {
					c.Reused.Add(1)
				}
				if conn, ok := info.Conn.(*tls.Conn); ok && conn.ConnectionState().NegotiatedProtocol == "h2" {
					c.H2.Store(true)
				}
				coldH2DiagnosticPeak(&c.Peak, c.Active.Add(1))
				coldH2DiagnosticPeak(&d.requestPeak, d.requestActive.Add(1))
			})
		},
	}
	// WithClientTrace composes the existing qualification trace, preserving its
	// original protocol and distinct request-used connection observations.
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace)), done
}

func (d *coldH2Diagnostic) finish(t *testing.T) {
	if d == nil {
		return
	}
	// client.Do does not join surplus dials. Observe one fixed tail interval
	// after CloseIdleConnections; report any remaining activity explicitly.
	tailStart := time.Now()
	deadline := tailStart.Add(time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	d.freezeObservation()
	d.mu.Lock()
	connections := make([]coldH2DiagnosticConnectionResult, 0, len(d.connections))
	for _, c := range d.connections {
		connections = append(connections, coldH2DiagnosticConnectionResult{c.ID, c.Uses.Load(), c.Reused.Load(), c.Active.Load(), c.Peak.Load(), c.H2.Load()})
	}
	d.mu.Unlock()
	sort.Slice(connections, func(i, j int) bool { return connections[i].ID < connections[j].ID })
	buckets := make([]coldH2DiagnosticCounts, len(d.buckets))
	var totals coldH2DiagnosticCounts
	for i := range d.buckets {
		c := d.buckets[i].snapshot()
		buckets[i] = c
		totals.ConnectStart += c.ConnectStart
		totals.ConnectDone += c.ConnectDone
		totals.ConnectError += c.ConnectError
		totals.TLSStart += c.TLSStart
		totals.TLSDone += c.TLSDone
		totals.TLSError += c.TLSError
		totals.TLSH2 += c.TLSH2
		totals.GotConn += c.GotConn
		totals.Reused += c.Reused
		totals.WasIdle += c.WasIdle
	}
	result := map[string]any{
		"schema_version": 1, "kind": "changed-client-cold-h2-diagnostic", "original_cold_gate": false, "qualifies_4096": false,
		"max_conns_per_host": d.cap, "concurrency": 3072, "toolchain": runtime.Version(),
		"timeline_origin":          "diagnostic configuration before measured workload; no prewarm",
		"timeline_bucket_width_ms": coldH2DiagnosticBucketWidth.Milliseconds(), "timeline_last_bucket_overflow": true, "timeline": buckets, "totals": totals,
		"tcp_connect_scope":     "httptrace ConnectStart/Done: TCP attempts, not net/http dial queue entries; no custom DialContext",
		"connect_peak_inflight": d.connectPeak.Load(), "connect_outstanding_at_snapshot": d.connectActive.Load(),
		"tls_peak_inflight": d.tlsPeak.Load(), "tls_outstanding_at_snapshot": d.tlsActive.Load(),
		"observation_tail_ms": time.Since(tailStart).Milliseconds(), "observation_tail_target_ms": 1000,
		"observed_trace_activity_drained": d.connectActive.Load() == 0 && d.tlsActive.Load() == 0,
		"post_observation_activity":       "unknown; callbacks after coherent fixed-tail snapshot are ignored",
		"all_transport_dials_joined":      "unknown: httptrace cannot observe queued or not-yet-started transport dials",
		"connection_storage_limit":        coldH2DiagnosticConnectionLimit, "connection_storage_overflow_callbacks": d.connectionOverflow.Load(),
		"connections": connections, "request_assignments_peak_to_client_do_return": d.requestPeak.Load(), "request_assignments_outstanding": d.requestActive.Load(),
		"connection_usage_scope": "GotConn assignment until client.Do returns headers/error; retries counted; not actual wire-active streams or negotiated capacity",
		"stream_capacity": map[string]any{
			"wire_settings_observed": false, "reason": "stock net/http httptrace exposes ALPN but no peer SETTINGS_MAX_CONCURRENT_STREAMS",
			"source_toolchain": "go1.26.6 src/net/http/h2_bundle.go", "client_initial_before_settings": 100,
			"client_default_if_setting_absent": 1000, "server_default_advertised_limit": 250,
			"source_values_are_negotiation_evidence": false, "cap_times_initial_assumption": d.cap * 100,
			"required_connections_at_initial_assumption": (3072 + 99) / 100,
			"capacity_interpretation":                    "64x100=6400 exceeds 3072 only as source arithmetic; origin simultaneous occupancy and unchanged owner/payload/event contracts are required empirical evidence",
		},
	}
	var row qualificationResult
	data, err := os.ReadFile(d.resultPath)
	if err == nil {
		err = json.Unmarshal(data, &row)
	}
	if err != nil {
		result["qualification_result_read_error"] = err.Error()
	} else {
		result["qualification_result"] = d.resultPath
		result["owner_peak_reservations"] = row.OwnerPeakReservations
		result["owner_peak_bytes"] = row.OwnerPeakBytes
		result["owner_peak_fds"] = row.OwnerPeakFDs
		result["admission_rejections"] = row.AdmissionRejections
		result["opening_wait_observation"] = "session_opening_limit/queue admission-attempt counters in qualification result; no wait-duration instrumentation"
	}
	if err := writeQualificationJSON(d.resultPath+".cold-h2.json", result); err != nil {
		t.Error(fmt.Errorf("write cold-H2 diagnostic: %w", err))
	}
}

func TestColdH2DiagnosticAbsentPreservesTransport(t *testing.T) {
	t.Setenv("P2PSTREAM_COLD_H2_DIAGNOSTIC_MAX_CONNS", "")
	transport := &http.Transport{MaxConnsPerHost: 3072}
	diagnostic := qualificationConfigureColdH2Diagnostic(t, transport, "anything", "h1", 1, 1)
	if diagnostic != nil || transport.MaxConnsPerHost != 3072 {
		t.Fatal("absent opt-in changed the transport")
	}
	diagnostic.finish(t) // The common fixture defers this even when disabled.
	req, err := http.NewRequest(http.MethodGet, "https://public.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	traced, done := diagnostic.traceRequest(req)
	done()
	if traced != req {
		t.Fatal("disabled instrumentation changed request")
	}
}

func TestColdH2DiagnosticPreservesH2TraceAndBoundsConnectionStorage(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	d := &coldH2Diagnostic{started: time.Now(), connections: make(map[net.Conn]*coldH2DiagnosticConnection)}
	client := server.Client()
	var originalTrace atomic.Int64
	for range 2 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { originalTrace.Add(1) }}))
		req, done := d.traceRequest(req)
		resp, err := client.Do(req)
		done()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.ProtoMajor != 2 {
			t.Fatalf("HTTP/%d", resp.ProtoMajor)
		}
	}
	if originalTrace.Load() != 2 || len(d.connections) != 1 || d.connectActive.Load() != 0 || d.tlsActive.Load() != 0 || d.requestActive.Load() != 0 {
		t.Fatalf("trace/connection balance: original=%d conns=%d connect=%d tls=%d requests=%d", originalTrace.Load(), len(d.connections), d.connectActive.Load(), d.tlsActive.Load(), d.requestActive.Load())
	}
	for _, c := range d.connections {
		if c.Uses.Load() != 2 || c.Reused.Load() != 1 || c.Peak.Load() != 1 || !c.H2.Load() {
			t.Fatal("unexpected connection use observations")
		}
	}
	// Fill with bounded, comparable fake identities. No sockets are opened.
	for len(d.connections) < coldH2DiagnosticConnectionLimit {
		d.connections[&coldH2DiagnosticFakeConn{}] = &coldH2DiagnosticConnection{}
	}
	if d.connection(httptrace.GotConnInfo{Conn: &coldH2DiagnosticFakeConn{}}) != nil || d.connectionOverflow.Load() != 1 || len(d.connections) != coldH2DiagnosticConnectionLimit {
		t.Fatal("connection observation bound not enforced")
	}
}

type coldH2DiagnosticFakeConn struct {
	net.Conn
	identity byte
}

func TestColdH2DiagnosticTimelineOverflow(t *testing.T) {
	d := &coldH2Diagnostic{started: time.Now().Add(-time.Minute)}
	d.bucket().GotConn.Add(1)
	if d.buckets[coldH2DiagnosticBucketCount-1].GotConn.Load() != 1 {
		t.Fatal("late event did not enter bounded overflow bucket")
	}
}

func TestColdH2DiagnosticConcurrentSnapshotIsCoherent(t *testing.T) {
	d := &coldH2Diagnostic{started: time.Now(), connections: make(map[net.Conn]*coldH2DiagnosticConnection)}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 200 {
				d.observe(func() {
					bucket := d.bucket()
					bucket.ConnectStart.Add(1)
					bucket.ConnectDone.Add(1)
					_ = d.connection(httptrace.GotConnInfo{Conn: &coldH2DiagnosticFakeConn{}})
				})
			}
		}()
	}
	close(start)
	runtime.Gosched()
	d.freezeObservation()
	before := len(d.connections)
	var starts, dones int64
	for i := range d.buckets {
		c := d.buckets[i].snapshot()
		starts += c.ConnectStart
		dones += c.ConnectDone
	}
	workers.Wait()
	if starts != dones || len(d.connections) != before || before > coldH2DiagnosticConnectionLimit {
		t.Fatal("coherent snapshot changed after freeze")
	}
	d.observe(func() { t.Error("late callback admitted after observation closed") })
	var after int64
	for i := range d.buckets {
		after += d.buckets[i].ConnectStart.Load()
	}
	if after != starts {
		t.Fatal("late counter update changed frozen snapshot")
	}
}

func TestColdH2DiagnosticCapAndRecoveryScope(t *testing.T) {
	if runtime.Version() != "go1.26.6" {
		t.Skipf("only the version-pinned diagnostic configuration test requires go1.26.6; running %s", runtime.Version())
	}
	t.Setenv("P2PSTREAM_COLD_H2_DIAGNOSTIC_MAX_CONNS", "64")
	t.Setenv("P2PSTREAM_QUALIFICATION_PATH", "agent-external")
	t.Setenv("P2PSTREAM_QUALIFICATION_CAPACITY_ESTABLISHED_H2", "0")
	t.Setenv("P2PSTREAM_QUALIFICATION_H2_PREWARM", "0")
	t.Setenv("P2PSTREAM_QUALIFICATION_DURATION_SECONDS", "0")
	t.Setenv("P2PSTREAM_QUALIFICATION_WARMUP_SECONDS", "0")
	t.Setenv("P2PSTREAM_QUALIFICATION_OFFERED_RPS", "0")
	t.Setenv("P2PSTREAM_QUALIFICATION_RESULT", t.TempDir()+"/result.json")
	transport := &http.Transport{MaxConnsPerHost: 3072}
	d := qualificationConfigureColdH2Diagnostic(t, transport, "capacity-3072-h2", "h2", 3072, 3072)
	if d == nil || transport.MaxConnsPerHost != 64 {
		t.Fatal("target diagnostic cap missing")
	}
	transport.MaxConnsPerHost = 3072
	if qualificationConfigureColdH2Diagnostic(t, transport, "recovery", "h2", 64, 512) != nil || transport.MaxConnsPerHost != 3072 {
		t.Fatal("recovery client changed")
	}
	t.Setenv("P2PSTREAM_QUALIFICATION_PATH", "direct-control")
	if qualificationConfigureColdH2Diagnostic(t, transport, "capacity-3072-h2", "h2", 3072, 3072) != nil || transport.MaxConnsPerHost != 3072 {
		t.Fatal("standard HAProxy control client changed")
	}
}
