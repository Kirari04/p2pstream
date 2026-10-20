//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func qualificationLatencyFake(t *testing.T, n int) (*qualificationLatencyDiagnostic, *atomic.Int64) {
	t.Helper()
	var clock atomic.Int64
	clock.Store(1)
	epoch := time.Unix(0, 0)
	d := &qualificationLatencyDiagnostic{role: "load", dir: t.TempDir(), epoch: epoch, now: func() time.Time { return epoch.Add(time.Duration(clock.Load())) }, records: make([]qualificationLatencyRecord, n), observations: make([]qualificationLatencyObservation, 0, qualificationLatencyObservations)}
	d.trace = newQualificationLatencyTrace(filepath.Join(d.dir, "runtime.trace"), qualificationLatencyTraceCap)
	return d, &clock
}

func TestQualificationLatencyOptInBounds(t *testing.T) {
	empty := func(string) string { return "" }
	d, err := qualificationLatencyFromEnv(empty, "load")
	if d != nil || err != nil {
		t.Fatal(d, err)
	}
	// Nil hooks must not touch supplied records, files, clocks or admin output.
	var nilD *qualificationLatencyDiagnostic
	nilD.worker(nil)
	nilD.gotConn(nil, httptrace.GotConnInfo{})
	nilD.returned(nil)
	nilD.complete(nil, 0, 0, 0)
	nilD.failed(nil, time.Time{}, 0)
	nilD.observe("", 0, qualificationRuntime{}, false)
	if nilD.stamp() != 0 || nilD.finish() != nil || nilD.dispatch(999, time.Time{}) != nil {
		t.Fatal("default is not inert")
	}
	w := httptest.NewRecorder()
	if nilD.admin(w, httptest.NewRequest("POST", "http://localhost/latency-trace-arm", nil)) || w.Body.Len() != 0 {
		t.Fatal("default admin changed")
	}
	if int64(unsafe.Sizeof(qualificationLatencyRecord{}))*qualificationLatencyJobs > 32<<20 {
		t.Fatal("request preallocation exceeds 32 MiB")
	}
	for _, tc := range []struct{ name, key, value string }{
		{"partial", "DIR", "/tmp/unused"}, {"trace_id", "TRACE_ID", "UPPER"}, {"mode", "DIAGNOSTIC", "true"}, {"negative_start", "TRACE_START_NS", "-1"},
		{"overflow_start", "TRACE_START_NS", "9223372036854775808"}, {"zero_start", "TRACE_START_NS", "0"},
		{"count", "REQUESTS", "263661"}, {"concurrency", "CONCURRENCY", "16"}, {"scenario", "SCENARIO", "matched-h2-offered-20s"},
		{"rate", "OFFERED_RPS", "13183.0"}, {"warmup", "WARMUP_SECONDS", "1"}, {"duration", "DURATION_SECONDS", "20"},
		{"profile", "PROFILE_MODE", "cpu"}, {"census", "MEMORY_CENSUS", "1"}, {"ballast", "BALLAST_MIB", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, clockErr := qualificationLatencyMono()
			if clockErr != nil {
				t.Fatal(clockErr)
			}
			values := map[string]string{"TRACE_ID": "0123456789abcdef0123456789abcdef", "DIAGNOSTIC": "1", "DIR": filepath.Join(t.TempDir(), "fresh"), "TRACE_START_NS": strconv.FormatInt(now+int64(10*time.Second), 10), "SCENARIO": "matched-h1-offered-20s", "PROTOCOL": "h1", "CONCURRENCY": "128", "REQUESTS": "263660", "OFFERED_RPS": "13183", "MATCHED_TLS": "1", "DURATION_SECONDS": "0", "WARMUP_SECONDS": "0", "BALLAST_MIB": "0"}
			values[tc.key] = tc.value
			if tc.name == "partial" {
				values["DIAGNOSTIC"] = ""
			}
			get := func(k string) string {
				for _, prefix := range []string{qualificationLatencyPrefix, "P2PSTREAM_QUALIFICATION_"} {
					if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
						return values[k[len(prefix):]]
					}
				}
				return ""
			}
			d, err := qualificationLatencyFromEnv(get, "load")
			if err == nil {
				if d != nil {
					_ = d.finish()
				}
				t.Fatal("invalid mode accepted")
			}
		})
	}
}

func TestQualificationLatencyComponentsAndFailures(t *testing.T) {
	d, c := qualificationLatencyFake(t, 1)
	c.Store(110)
	r := d.dispatch(0, d.epoch.Add(100))
	c.Store(130)
	d.worker(r)
	r.Do = 140
	c.Store(150)
	d.gotConn(r, httptrace.GotConnInfo{Reused: true})
	c.Store(170)
	d.returned(r)
	r.Body = 190
	d.complete(r, 120, 200, 1)
	r.Bookkeeping = 230
	got, err := qualificationLatencyComponents(r)
	want := [6]int64{10, 20, 10, 30, 20, 30}
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	if r.GotConn.Load()-r.Do != 10 || r.Elapsed != 120 || r.Reused.Load() != 1 {
		t.Fatal("connection/original elapsed lost")
	}
	d.gotConn(r, httptrace.GotConnInfo{})
	if d.late.Load() != 1 || r.Callbacks.Load() != 1 {
		t.Fatal("late callback not isolated")
	}
	for _, outcome := range []uint32{2, 3, 4} {
		t.Run(strconv.Itoa(int(outcome)), func(t *testing.T) {
			d, c := qualificationLatencyFake(t, 1)
			c.Store(110)
			r := d.dispatch(0, d.epoch.Add(100))
			d.worker(r)
			c.Store(180)
			d.failed(r, d.epoch.Add(100), outcome)
			if r.Outcome != outcome || r.Elapsed != 80 || r.Complete != 180 || r.Bookkeeping != 180 {
				t.Fatal("failure did not retain terminal timestamp")
			}
			if _, err := qualificationLatencyComponents(r); err == nil {
				t.Fatal("failed record accepted")
			}
		})
	}
	for bit := 0; bit < 3; bit++ {
		d, c := qualificationLatencyFake(t, 1)
		r := &d.records[0]
		r.Elapsed = 120
		c.Store(230)
		var readErr, closeErr error
		valid := true
		if bit == 0 {
			readErr = io.ErrUnexpectedEOF
		}
		if bit == 1 {
			closeErr = io.ErrClosedPipe
		}
		if bit == 2 {
			valid = false
		}
		d.bodyFailed(r, readErr, closeErr, valid)
		if r.Outcome != 4|uint32(1<<(8+bit)) || r.Elapsed != 120 || r.Bookkeeping != 230 {
			t.Fatal("body/Close/validation failure reason lost")
		}
	}
	r.Callbacks.Store(2)
	if _, err := qualificationLatencyComponents(r); err == nil {
		t.Fatal("repeated callback accepted")
	}
	r.Callbacks.Store(1)
	r.Elapsed++
	if _, err := qualificationLatencyComponents(r); err == nil {
		t.Fatal("nonconserving elapsed accepted")
	}
	r.Elapsed--
	r.Body = r.Headers - 1
	if _, err := qualificationLatencyComponents(r); err == nil {
		t.Fatal("reversed clock accepted")
	}
}

type qualificationLatencyBarrierTransport struct {
	ctx                                  context.Context
	conn, headers, body, close           <-chan struct{}
	entered, connected, reading, closing chan struct{}
	response                             *qualificationLatencyBarrierBody
}

func (b *qualificationLatencyBarrierTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	close(b.entered)
	if err := qualificationLatencyWait(b.ctx, b.conn); err != nil {
		return nil, err
	}
	httptrace.ContextClientTrace(req.Context()).GotConn(httptrace.GotConnInfo{Reused: true})
	close(b.connected)
	if err := qualificationLatencyWait(b.ctx, b.headers); err != nil {
		return nil, err
	}
	b.response = &qualificationLatencyBarrierBody{ctx: b.ctx, body: b.body, close: b.close, reading: b.reading, closing: b.closing}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: b.response, Request: req}, nil
}

type qualificationLatencyBarrierBody struct {
	ctx              context.Context
	body, close      <-chan struct{}
	reading, closing chan struct{}
	read             bool
}

func (b *qualificationLatencyBarrierBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	close(b.reading)
	if err := qualificationLatencyWait(b.ctx, b.body); err != nil {
		return 0, err
	}
	b.read = true
	return copy(p, make([]byte, 256)), io.EOF
}
func (b *qualificationLatencyBarrierBody) Close() error {
	close(b.closing)
	return qualificationLatencyWait(b.ctx, b.close)
}
func qualificationLatencyWait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestQualificationLatencyControlledStages(t *testing.T) {
	// No sleeps or presumed absence. Each acknowledgement is a real blocked
	// worker/transport/body boundary; only the coordinator advances the clock.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	d, clock := qualificationLatencyFake(t, 1)
	workerGate, doGate, connGate, headersGate, bodyGate, closeGate, finishGate := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	workerAck, completeAck, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	tr := &qualificationLatencyBarrierTransport{ctx: ctx, conn: connGate, headers: headersGate, body: bodyGate, close: closeGate, entered: make(chan struct{}), connected: make(chan struct{}), reading: make(chan struct{}), closing: make(chan struct{})}
	jobs := make(chan *qualificationLatencyRecord)
	fail := make(chan error, 1)
	go func() {
		defer close(done)
		if err := qualificationLatencyWait(ctx, workerGate); err != nil {
			fail <- err
			return
		}
		var r *qualificationLatencyRecord
		select {
		case r = <-jobs:
		case <-ctx.Done():
			fail <- ctx.Err()
			return
		}
		d.worker(r)
		close(workerAck)
		if err := qualificationLatencyWait(ctx, doGate); err != nil {
			fail <- err
			return
		}
		req, _ := http.NewRequest("GET", "http://fixture.invalid/direct/small", nil)
		req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { d.gotConn(r, i) }}))
		r.Do = d.stamp()
		resp, err := (&http.Client{Transport: tr}).Do(req)
		d.returned(r)
		if err != nil {
			fail <- err
			return
		}
		payload, err := io.ReadAll(resp.Body)
		r.Body = d.stamp()
		if err != nil || len(payload) != 256 || !bytes.Equal(payload, make([]byte, 256)) {
			fail <- errors.New("body contract")
			return
		}
		if err := resp.Body.Close(); err != nil {
			fail <- err
			return
		}
		d.complete(r, d.now().Sub(d.epoch.Add(time.Duration(r.Scheduled))), 200, 1)
		close(completeAck)
		if err := qualificationLatencyWait(ctx, finishGate); err != nil {
			fail <- err
			return
		}
		r.Bookkeeping = d.stamp()
		fail <- nil
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("controlled worker did not join")
		}
	}()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		if err := qualificationLatencyWait(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	clock.Store(110)
	r := d.dispatch(0, d.epoch.Add(100))
	clock.Store(130)
	close(workerGate)
	select {
	case jobs <- r:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	wait(workerAck)
	clock.Store(140)
	close(doGate)
	wait(tr.entered)
	clock.Store(150)
	close(connGate)
	wait(tr.connected)
	clock.Store(170)
	close(headersGate)
	wait(tr.reading)
	clock.Store(190)
	close(bodyGate)
	wait(tr.closing)
	clock.Store(220)
	close(closeGate)
	wait(completeAck)
	clock.Store(230)
	close(finishGate)
	wait(done)
	if err := <-fail; err != nil {
		t.Fatal(err)
	}
	parts, err := qualificationLatencyComponents(r)
	if err != nil || parts != [6]int64{10, 20, 10, 30, 20, 30} || r.Elapsed != 120 || r.Bookkeeping-r.Complete != 10 {
		t.Fatal(parts, err, r.Elapsed)
	}
}

func TestQualificationLatencyObservationAndBinaryBounds(t *testing.T) {
	d, c := qualificationLatencyFake(t, 1)
	c.Store(10)
	for i := 0; i < qualificationLatencyObservations; i++ {
		d.observe("driver_admin", 5, qualificationRuntime{NumGC: 7, PauseNS: 11, HeapAlloc: 13, TotalAlloc: 17}, i%2 == 0)
	}
	d.observe("driver_admin", 5, qualificationRuntime{}, false)
	r := &d.records[0]
	r.Scheduled = 1
	r.Ready = 2
	r.Worker = 3
	r.Do = 4
	r.GotConn.Store(5)
	r.Headers = 6
	r.Body = 7
	r.Complete = 8
	r.Bookkeeping = 9
	r.Elapsed = 7
	r.Callbacks.Store(1)
	r.Reused.Store(1)
	r.Status = 200
	r.Outcome = 1
	if !d.overflow || len(d.observations) != qualificationLatencyObservations || d.observations[0].NumGC != 7 || d.observations[0].PauseNS != 11 || d.observations[1].OK {
		t.Fatal("observation cap/value mismatch")
	}
	if err := d.finish(); err == nil {
		t.Fatal("unarmed/unjoined/overflow diagnostic accepted")
	}
	raw, err := os.ReadFile(filepath.Join(d.dir, "requests.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 16+qualificationLatencyRecordBytes || string(raw[:8]) != "P2PLAT01" || binary.LittleEndian.Uint32(raw[8:]) != 1 || binary.LittleEndian.Uint32(raw[12:]) != qualificationLatencyRecordBytes {
		t.Fatal("binary format")
	}
	for i, want := range []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 7} {
		if binary.LittleEndian.Uint64(raw[16+i*8:]) != want {
			t.Fatal("request field order/encoding")
		}
	}
	for i, want := range []uint32{1, 1, 200, 1} {
		if binary.LittleEndian.Uint32(raw[16+80+i*4:]) != want {
			t.Fatal("request flags encoding")
		}
	}
	var receipt struct {
		Complete     bool
		Records      int    `json:"record_count"`
		Bytes        int64  `json:"file_bytes"`
		SHA          string `json:"sha256"`
		Observations []qualificationLatencyObservation
	}
	encoded, err := os.ReadFile(filepath.Join(d.dir, "receipt.json"))
	if err != nil || json.Unmarshal(encoded, &receipt) != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if receipt.Complete || receipt.Records != 1 || receipt.Bytes != int64(len(raw)) || receipt.SHA != hex.EncodeToString(hash[:]) || len(receipt.Observations) != qualificationLatencyObservations {
		t.Fatal("receipt/count/hash mismatch")
	}
	// Idempotence cannot overwrite exclusive evidence files or conceal failure.
	if d.finish() == nil {
		t.Fatal("second finish hid failure")
	}
}

func TestQualificationLatencyTraceOwner(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		startDelta, endDelta int64
	}{{"past", -1, int64(2*time.Second) - 1}, {"far", int64(31 * time.Second), int64(33 * time.Second)}, {"duration", int64(time.Second), int64(4 * time.Second)}, {"overflow", int64(^uint64(0) >> 1), -1}} {
		t.Run(tc.name, func(t *testing.T) {
			now, err := qualificationLatencyMono()
			if err != nil {
				t.Fatal(err)
			}
			tr := newQualificationLatencyTrace(filepath.Join(t.TempDir(), "trace"), 128)
			if tr.arm(now+tc.startDelta, now+tc.endDelta) == nil {
				tr.close()
				t.Fatal("invalid window accepted")
			}
			tr.close()
		})
	}
	now, err := qualificationLatencyMono()
	if err != nil {
		t.Fatal(err)
	}
	tr := newQualificationLatencyTrace(filepath.Join(t.TempDir(), "trace"), 128)
	if err := tr.arm(now+int64(time.Second), now+int64(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if tr.arm(now+int64(time.Second), now+int64(3*time.Second)) == nil {
		t.Fatal("second arm accepted")
	}
	result := tr.close()
	if result.Complete || result.Error == "" {
		t.Fatal("early cleanup accepted")
	}
	if !reflect.DeepEqual(result, tr.close()) {
		t.Fatal("trace close not idempotent")
	}
	var output bytes.Buffer
	w := &qualificationLatencyTraceWriter{file: &output, cap: 3, hash: sha256.New()}
	n, err := w.Write([]byte("abcde"))
	if n != 3 || err == nil || w.n != 3 || output.String() != "abc" {
		t.Fatal("trace cap failed")
	}
	if n, err := w.Write([]byte("z")); n != 0 || err == nil || output.String() != "abc" {
		t.Fatal("trace continued after cap")
	}
}

func TestQualificationLatencyOwnedAdminBoundary(t *testing.T) {
	d, _ := qualificationLatencyFake(t, 0)
	d.role = "proxy"
	for _, tc := range []struct{ name, method, address, body string }{{"remote", "POST", "192.0.2.1:99", "{}"}, {"method", "GET", "127.0.0.1:99", "{}"}, {"extra", "POST", "127.0.0.1:99", "{\"x\":1}"}, {"large", "POST", "127.0.0.1:99", string(make([]byte, 257))}, {"past", "POST", "127.0.0.1:99", "{\"start_ns\":1,\"end_ns\":2000000001}"}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://localhost/latency-trace-arm", bytes.NewBufferString(tc.body))
			req.RemoteAddr = tc.address
			w := httptest.NewRecorder()
			if !d.admin(w, req) || w.Code != 400 || d.trace.armed {
				t.Fatal("invalid admin arm accepted")
			}
		})
	}
	now, err := qualificationLatencyMono()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"start_ns":` + strconv.FormatInt(now+int64(time.Second), 10) + `,"end_ns":` + strconv.FormatInt(now+int64(3*time.Second), 10) + `}`)
	req := httptest.NewRequest("POST", "http://localhost/latency-trace-arm", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:99"
	w := httptest.NewRecorder()
	if !d.admin(w, req) || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if d.trace.close().Complete {
		t.Fatal("canceled trace unexpectedly complete")
	}
}

func TestQualificationLatencyTraceLifecycle(t *testing.T) {
	// A root-owned follow-up must parse this raw trace with pinned Go1.26.6
	// cmd/trace -d=parsed and check the two anchors and ReadMemStats STW reason/
	// stack. Magic/hash checks here are transport evidence, NOT a parse claim.
	dir := os.Getenv("P2PSTREAM_QUALIFICATION_LATENCY_CONTRACT_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			t.Fatal("contract directory must be absolute clean")
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	tr := newQualificationLatencyTrace(filepath.Join(dir, "runtime.trace"), qualificationLatencyTraceCap)
	defer tr.close()
	now, err := qualificationLatencyMono()
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.arm(now+int64(50*time.Millisecond), now+int64(2050*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.started:
	case <-tr.done:
		t.Fatal("trace failed before start")
	case <-time.After(3 * time.Second):
		t.Fatal("trace start did not complete")
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats) // deliberate STW contract only, never a new workload poll
	select {
	case <-tr.done:
	case <-time.After(4 * time.Second):
		t.Fatal("trace timer failed to retire")
	}
	result := tr.close()
	if !result.Complete || result.Error != "" || result.Bytes <= 16 || result.Bytes > qualificationLatencyTraceCap || result.StartBefore > result.StartAfter || result.StartAfter > result.StartLogBefore || result.StartLogBefore > result.StartLogAfter || result.StopBefore > result.StopLogBefore || result.StopLogBefore > result.StopLogAfter || result.StopLogAfter > result.StopAfter {
		t.Fatalf("trace lifecycle %+v", result)
	}
	f, err := os.Open(tr.path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var magic [16]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || !bytes.HasPrefix(magic[:], []byte("go 1.")) {
		t.Fatal("trace header", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n != result.Bytes || hex.EncodeToString(h.Sum(nil)) != result.SHA256 {
		t.Fatal("trace hash/size", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trace-receipt.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("LATENCY_TRACE_CONTRACT bytes=%d sha256=%s anchors=2 parse_required=true trace=%s receipt=%s", result.Bytes, result.SHA256, tr.path, filepath.Join(dir, "trace-receipt.json"))
}

func TestQualificationLatencySamplerAndEndpointObservation(t *testing.T) {
	d, clock := qualificationLatencyFake(t, 0)
	d.role = "proxy"
	handler := qualificationRuntimeHandlerWithLatency(nil, nil, d)
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 200 || len(d.observations) != 1 || d.observations[0].Kind != "endpoint_read_mem_stats" || !d.observations[0].OK {
		t.Fatal("existing MemStats call not observed")
	}
	var ordinary, observed map[string]any
	plain := httptest.NewRecorder()
	qualificationRuntimeHandler(nil).ServeHTTP(plain, req)
	if json.Unmarshal(plain.Body.Bytes(), &ordinary) != nil || json.Unmarshal(response.Body.Bytes(), &observed) != nil || len(ordinary) != len(observed) {
		t.Fatal("admin schema changed")
	}
	for k := range ordinary {
		if _, ok := observed[k]; !ok {
			t.Fatalf("admin field removed: %s", k)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clock.Store(20)
		if r.URL.Query().Get("events") != "1" {
			t.Error("sampler query changed")
		}
		_, _ = io.WriteString(w, `{"num_gc":7,"pause_total_ns":11,"heap_alloc_bytes":13,"total_alloc_bytes":17}`)
	}))
	defer server.Close()
	sampler := newQualificationSampler("", server.URL, false)
	sampler.latency = d
	before := len(d.observations)
	stats, ok := sampler.readRuntime(qualificationBoundary)
	if !ok || len(d.observations) != before+1 {
		t.Fatal("sampler observation missing")
	}
	item := d.observations[before]
	if item.Kind != "driver_admin" || !item.OK || item.Start != 1 || item.End != 20 || item.NumGC != 7 || item.PauseNS != 11 || item.HeapAlloc != 13 || item.TotalAlloc != 17 || item.NumGC != stats.NumGC || item.TotalAlloc != stats.TotalAlloc {
		t.Fatal("sampler boundary/value mismatch", item, stats)
	}
	server.Close()
	_, ok = sampler.readRuntime(qualificationBoundary)
	if ok || d.observations[len(d.observations)-1].OK {
		t.Fatal("failed sampler call hidden")
	}
}

func TestQualificationLatencyValidConfiguration(t *testing.T) {
	for _, role := range []string{"proxy", "load"} {
		t.Run(role, func(t *testing.T) {
			now, err := qualificationLatencyMono()
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"TRACE_ID": "0123456789abcdef0123456789abcdef", "DIAGNOSTIC": "1", "DIR": filepath.Join(t.TempDir(), "fresh"), "SCENARIO": "matched-h1-offered-20s", "PROTOCOL": "h1", "CONCURRENCY": "128", "REQUESTS": "263660", "OFFERED_RPS": "13183", "MATCHED_TLS": "1", "DURATION_SECONDS": "0", "WARMUP_SECONDS": "0", "BALLAST_MIB": "0"}
			if role == "load" {
				values["TRACE_START_NS"] = strconv.FormatInt(now+int64(10*time.Second), 10)
			}
			get := func(k string) string {
				for _, prefix := range []string{qualificationLatencyPrefix, "P2PSTREAM_QUALIFICATION_"} {
					if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
						return values[k[len(prefix):]]
					}
				}
				return ""
			}
			d, err := qualificationLatencyFromEnv(get, role)
			if err != nil || d == nil {
				t.Fatal(err)
			}
			if role == "load" {
				if d.trace.armed {
					t.Fatal("trace armed before TLS validation boundary")
				}
				if err := d.armDriver(); err != nil {
					t.Fatal(err)
				}
			}
			result := d.trace.close()
			if result.Complete {
				t.Fatal("unexecuted trace marked complete")
			}
			expected := 0
			if role == "load" {
				expected = qualificationLatencyJobs
			}
			if len(d.records) != expected || cap(d.observations) != qualificationLatencyObservations || d.trace.role != role || d.trace.id != values["TRACE_ID"] {
				t.Fatal("fixed config identity/count changed")
			}
			if _, err := qualificationLatencyFromEnv(get, role); err == nil {
				t.Fatal("existing output directory reused")
			}
		})
	}
}

func TestQualificationLatencyTraceIntervalTolerance(t *testing.T) {
	start, end := int64(time.Second), int64(3*time.Second)
	tolerance := int64(qualificationLatencyTraceTolerance)
	good := qualificationLatencyTraceResult{PlannedStart: start, PlannedEnd: end, StartBefore: start, StartAfter: start + tolerance, StartLogBefore: start + tolerance, StartLogAfter: start + tolerance + 1, StopBefore: end, StopLogBefore: end, StopLogAfter: end + tolerance, StopAfter: end + tolerance}
	if err := qualificationLatencyTraceIntervals(good); err != nil {
		t.Fatal("exact tolerance boundary rejected", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*qualificationLatencyTraceResult)
	}{
		{"late_start", func(r *qualificationLatencyTraceResult) { r.StartAfter++ }},
		{"late_stop", func(r *qualificationLatencyTraceResult) { r.StopAfter++ }},
		{"early_start", func(r *qualificationLatencyTraceResult) { r.StartBefore-- }},
		{"early_stop", func(r *qualificationLatencyTraceResult) { r.StopBefore-- }},
		{"reversed_start", func(r *qualificationLatencyTraceResult) { r.StartAfter = r.StartBefore - 1 }},
		{"reversed_stop", func(r *qualificationLatencyTraceResult) { r.StopAfter = r.StopBefore - 1 }},
		{"anchor_outside", func(r *qualificationLatencyTraceResult) { r.StopLogAfter++ }},
		{"overflow", func(r *qualificationLatencyTraceResult) {
			r.PlannedEnd = int64(^uint64(0) >> 1)
			r.PlannedStart = r.PlannedEnd - int64(2*time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := good
			tc.mutate(&bad)
			if qualificationLatencyTraceIntervals(bad) == nil {
				t.Fatal("invalid interval accepted")
			}
		})
	}
}

func TestQualificationLatencyOnsetPhase(t *testing.T) {
	for _, tc := range []struct {
		name, role, phase string
		disabled, invalid bool
	}{
		{name: "load_onset", role: "load", phase: "onset"},
		{name: "proxy_onset", role: "proxy", phase: "onset"},
		{name: "default_middle", role: "load"},
		{name: "phase_requires_opt_in", role: "load", phase: "onset", disabled: true, invalid: true},
		{name: "unknown_phase", role: "load", phase: "Onset", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{"DIAGNOSTIC": "1", "PHASE": tc.phase, "DIR": filepath.Join(t.TempDir(), "fresh"), "TRACE_ID": "0123456789abcdef0123456789abcdef", "TRACE_START_NS": "10000000000", "SCENARIO": "matched-h1-offered-20s", "PROTOCOL": "h1", "CONCURRENCY": "128", "REQUESTS": "263660", "OFFERED_RPS": "13183", "MATCHED_TLS": "1", "DURATION_SECONDS": "0", "WARMUP_SECONDS": "0", "BALLAST_MIB": "0"}
			if tc.role == "proxy" {
				values["TRACE_START_NS"] = ""
			}
			if tc.disabled {
				values["DIAGNOSTIC"] = ""
			}
			get := func(key string) string {
				for _, prefix := range []string{qualificationLatencyPrefix, "P2PSTREAM_QUALIFICATION_"} {
					if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
						return values[key[len(prefix):]]
					}
				}
				return ""
			}
			d, err := qualificationLatencyFromEnv(get, tc.role)
			if tc.invalid {
				if err == nil || d != nil {
					t.Fatal("invalid phase accepted", d, err)
				}
				return
			}
			if err != nil || d == nil || d.phase != tc.phase {
				t.Fatal("phase not retained", d, err)
			}
			d.trace.close()
		})
	}
}

func TestQualificationLatencyOnsetGate(t *testing.T) {
	const start = int64(20 * time.Second)
	var nilD *qualificationLatencyDiagnostic
	if nilD.beforeSampler() != nil {
		t.Fatal("nil default changed")
	}
	if err := qualificationLatencyOnsetGate("", 0, func() (int64, error) { t.Fatal("default read clock"); return 0, nil }, func(time.Duration) { t.Fatal("default waited") }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                        string
		now, oversleep, planned     int64
		clockErrorAt, reads, sleeps int
		phase                       string
		invalid                     bool
	}{
		{name: "before_gate", now: start - int64(time.Second), planned: start, phase: "onset", reads: 2, sleeps: 1},
		{name: "at_gate", now: start + int64(250*time.Millisecond), planned: start, phase: "onset", reads: 1},
		{name: "at_latest", now: start + int64(750*time.Millisecond), planned: start, phase: "onset", reads: 1},
		{name: "late_setup", now: start + int64(750*time.Millisecond) + 1, planned: start, phase: "onset", reads: 1, invalid: true},
		{name: "sleep_overshoots", now: start, planned: start, phase: "onset", oversleep: int64(500*time.Millisecond) + 1, reads: 2, sleeps: 1, invalid: true},
		{name: "early_wake", now: start, planned: start, phase: "onset", oversleep: -1, reads: 2, sleeps: 1, invalid: true},
		{name: "clock_error", now: start, planned: start, phase: "onset", clockErrorAt: 1, reads: 1, invalid: true},
		{name: "clock_error_after_wait", now: start, planned: start, phase: "onset", clockErrorAt: 2, reads: 2, sleeps: 1, invalid: true},
		{name: "invalid_clock", now: 0, planned: start, phase: "onset", reads: 1, invalid: true},
		{name: "invalid_start", now: start, phase: "onset", invalid: true},
		{name: "invalid_phase", now: start, planned: start, phase: "middle", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, reads, sleeps := tc.now, 0, 0
			mono := func() (int64, error) {
				reads++
				if reads == tc.clockErrorAt {
					return 0, errors.New("fake clock failure")
				}
				return now, nil
			}
			sleep := func(wait time.Duration) {
				sleeps++
				if wait != time.Duration(start+int64(250*time.Millisecond)-now) {
					t.Fatal("wait moved fixed gate", wait)
				}
				now += int64(wait) + tc.oversleep
			}
			err := qualificationLatencyOnsetGate(tc.phase, tc.planned, mono, sleep)
			if (err != nil) != tc.invalid || reads != tc.reads || sleeps != tc.sleeps {
				t.Fatal("gate contract", err, reads, sleeps)
			}
		})
	}
}

func TestQualificationLatencyOnsetCoverage(t *testing.T) {
	const start = int64(20 * time.Second)
	for _, name := range []string{"at_gate", "at_latest", "before_gate", "after_latest", "reversed_origin", "wrong_first_schedule", "partial_capture", "late_start_anchor", "early_stop_anchor", "invalid_fixed_interval"} {
		t.Run(name, func(t *testing.T) {
			origin := qualificationLatencyAnchor{start + int64(250*time.Millisecond), 17, start + int64(250*time.Millisecond)}
			first := int64(17)
			tr := qualificationLatencyTraceResult{Armed: true, Complete: true, PlannedStart: start, PlannedEnd: start + int64(2*time.Second), StartBefore: start, StartAfter: start + int64(time.Millisecond), StartLogBefore: start + int64(time.Millisecond), StartLogAfter: start + int64(2*time.Millisecond), StopBefore: start + int64(2*time.Second), StopLogBefore: start + int64(2*time.Second) + int64(time.Millisecond), StopLogAfter: start + int64(2*time.Second) + int64(2*time.Millisecond), StopAfter: start + int64(2*time.Second) + int64(3*time.Millisecond)}
			switch name {
			case "at_latest":
				origin.MonoBefore, origin.MonoAfter = start+int64(750*time.Millisecond), start+int64(750*time.Millisecond)
			case "before_gate":
				origin.MonoBefore--
			case "after_latest":
				origin.MonoAfter = start + int64(750*time.Millisecond) + 1
			case "reversed_origin":
				origin.MonoAfter--
			case "wrong_first_schedule":
				first++
			case "partial_capture":
				tr.Complete = false
			case "late_start_anchor":
				tr.StartLogAfter = origin.MonoBefore + 1
			case "early_stop_anchor":
				tr.StopLogBefore = origin.MonoAfter + int64(time.Second) - 1
			case "invalid_fixed_interval":
				tr.PlannedEnd++
			}
			err := qualificationLatencyOnsetCoverage(origin, first, tr)
			valid := name == "at_gate" || name == "at_latest"
			if (err == nil) != valid {
				t.Fatal("coverage contract", name, err)
			}
		})
	}
}

func TestQualificationLatencyOnsetOriginAndIncompleteReceipt(t *testing.T) {
	for _, phase := range []string{"onset", ""} {
		name := phase
		if name == "" {
			name = "default_middle"
		}
		t.Run(name, func(t *testing.T) {
			d, clock := qualificationLatencyFake(t, 1)
			d.phase = phase
			started := d.epoch.Add(1)
			d.recordScheduleOrigin(int64(20*time.Second+250*time.Millisecond), started, int64(20*time.Second+250*time.Millisecond)+1)
			clock.Store(2)
			r := d.dispatch(0, started)
			r.Worker, r.Do, r.Headers, r.Body, r.Bookkeeping = 3, 4, 6, 7, 9
			r.GotConn.Store(5)
			r.Callbacks.Store(1)
			d.complete(r, 7, 200, 1)
			if phase == "onset" && (d.scheduleOrigin.GoRelative != r.Scheduled || d.scheduleOrigin.MonoAfter <= d.scheduleOrigin.MonoBefore) {
				t.Fatal("direct origin not retained")
			}
			if phase == "" && d.scheduleOrigin != (qualificationLatencyAnchor{}) {
				t.Fatal("default captured onset origin")
			}
			if _, err := qualificationLatencyComponents(r); err != nil || r.Elapsed != 7 {
				t.Fatal("original elapsed/components changed", err)
			}
			d.joined = true
			clock.Store(30)
			if d.finish() == nil {
				t.Fatal("partial trace accepted")
			}
			raw, err := os.ReadFile(filepath.Join(d.dir, "requests.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != 16+qualificationLatencyRecordBytes || binary.LittleEndian.Uint64(raw[16:]) != 1 || binary.LittleEndian.Uint64(raw[16+72:]) != 7 {
				t.Fatal("partial request evidence or original elapsed lost")
			}
			encoded, err := os.ReadFile(filepath.Join(d.dir, "receipt.json"))
			if err != nil {
				t.Fatal(err)
			}
			var receipt map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &receipt); err != nil {
				t.Fatal(err)
			}
			if string(receipt["complete"]) != "false" {
				t.Fatal("partial capture reported complete")
			}
			_, hasPhase := receipt["phase"]
			_, hasOrigin := receipt["schedule_origin"]
			if hasPhase != (phase == "onset") || hasOrigin != (phase == "onset") {
				t.Fatal("phase/origin receipt contract")
			}
		})
	}
}
