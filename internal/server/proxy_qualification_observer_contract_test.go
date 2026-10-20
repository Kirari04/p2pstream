//go:build linux

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2pstream/internal/db"
)

func observerContractReader(mode string) (*qualificationObserver, *atomic.Int64, *atomic.Int64) {
	var mem, heap, clock atomic.Int64
	o := newQualificationObserver(mode)
	o.readMem = func(s *runtime.MemStats) {
		mem.Add(1)
		*s = runtime.MemStats{TotalAlloc: 101, HeapAlloc: 11, HeapInuse: 12, HeapIdle: 13, HeapReleased: 14, HeapSys: 15, NumGC: 2, PauseTotalNs: 17}
	}
	o.readHeap = func() (uint64, error) { heap.Add(1); return 0, nil } // Zero is a valid measured value, not absence.
	o.clock = func() (int64, error) { return clock.Add(1), nil }
	return o, &mem, &heap
}
func observerContractResponse(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}
func TestQualificationObserverQueriesAndPresence(t *testing.T) {
	for _, test := range []struct {
		name, mode, target, method string
		status                     int
		mem, heap                  int64
		purpose                    qualificationRuntimePurpose
	}{
		{"legacy", "", "/?ignored=%ZZ", "GET", 200, 1, 0, qualificationBoundary},
		{"legacy escaped unrelated", "", "/?unrelated=%ZZ&events=0", "GET", 200, 1, 0, qualificationBoundary},
		{"boundary", qualificationObserverSplit, "/", "GET", 200, 1, 0, qualificationBoundary},
		{"poll", qualificationObserverSplit, "/?observation=status-v1", "GET", 200, 0, 1, qualificationPoll},
		{"event probe", qualificationObserverSplit, "/?observation=status-v1&events=1&owners=1", "GET", 200, 0, 1, qualificationEventProbe},
		{"not opted in", "", "/?observation=status-v1", "GET", 400, 0, 0, ""},
		{"duplicate", qualificationObserverSplit, "/?observation=status-v1&observ%61tion=status-v1", "GET", 400, 0, 0, ""},
		{"unknown", qualificationObserverSplit, "/?observation=other", "GET", 400, 0, 0, ""},
		{"malformed", qualificationObserverSplit, "/?observation=status-v1&ignored=%ZZ", "GET", 400, 0, 0, ""},
		{"semicolon", qualificationObserverSplit, "/?observation=status-v1&events=1;flush=1", "GET", 400, 0, 0, ""},
		{"duplicate event", qualificationObserverSplit, "/?observation=status-v1&events=1&events=1", "GET", 400, 0, 0, ""},
		{"bad event", qualificationObserverSplit, "/?observation=status-v1&events=0", "GET", 400, 0, 0, ""},
		{"unknown pair", qualificationObserverSplit, "/?observation=status-v1&x=1", "GET", 400, 0, 0, ""},
		{"mutation", qualificationObserverSplit, "/?observation=status-v1&close_idle_agent_transports=1", "GET", 400, 0, 0, ""},
		{"drain", qualificationObserverSplit, "/?observation=status-v1&flush=1", "GET", 400, 0, 0, ""},
		{"census", qualificationObserverSplit, "/?observation=status-v1&memory_census=1", "GET", 400, 0, 0, ""},
		{"retirement", qualificationObserverSplit, "/retirement?observation=status-v1", "GET", 400, 0, 0, ""},
		{"post", qualificationObserverSplit, "/?observation=status-v1", "POST", 400, 0, 0, ""},
		{"bound", qualificationObserverSplit, "/?observation=status-v1&x=" + strings.Repeat("a", 512), "GET", 400, 0, 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, mem, heap := observerContractReader(test.mode)
			w := observerContractResponse(t, qualificationRuntimeHandlerWithObserver(nil, nil, nil, o), test.method, test.target)
			if w.Code != test.status || mem.Load() != test.mem || heap.Load() != test.heap {
				t.Fatalf("status=%d mem=%d heap=%d body=%s", w.Code, mem.Load(), heap.Load(), w.Body.String())
			}
			if w.Code != 200 {
				return
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			_, memoryPresent := fields["total_alloc_bytes"]
			if memoryPresent != (test.mem == 1) {
				t.Fatalf("memory presence=%v", memoryPresent)
			}
			if test.mode == "" {
				if _, present := fields["observer"]; present {
					t.Fatal("legacy wire changed")
				}
				return
			}
			stats, err := decodeQualificationObserverResponse(bytes.NewReader(w.Body.Bytes()), test.purpose)
			if err != nil {
				t.Fatal(err)
			}
			if test.heap == 1 && (stats.Observer.HeapObjects == nil || *stats.Observer.HeapObjects != 0) {
				t.Fatal("measured zero lost")
			}
		})
	}
}
func TestQualificationObserverMetricAndCapabilityFailures(t *testing.T) {
	if _, err := qualificationObserverHeap(func([]metrics.Sample) {}); err == nil {
		t.Fatal("KindBad accepted")
	}
	if _, err := qualificationObserverHeap(func(s []metrics.Sample) { s[0].Name = "wrong" }); err == nil {
		t.Fatal("changed key accepted")
	}
	if _, err := qualificationObserverHeap(func(s []metrics.Sample) {
		s[0].Name = "/cpu/classes/gc/mark/assist:cpu-seconds"
		metrics.Read(s)
		if s[0].Value.Kind() != metrics.KindFloat64 {
			t.Fatal("fixture metric is not float64")
		}
		s[0].Name = qualificationObserverHeapKey
	}); err == nil {
		t.Fatal("KindFloat64 accepted")
	}
	if _, err := qualificationObserverHeap(metrics.Read); err != nil {
		t.Fatal(err)
	} // Pinned runtime capability, no value/cost threshold.
	for _, failure := range []string{"heap", "clock-before", "clock-after", "regressing-clock"} {
		t.Run(failure, func(t *testing.T) {
			o, mem, _ := observerContractReader(qualificationObserverSplit)
			if failure == "heap" {
				o.readHeap = func() (uint64, error) { return 0, errors.New("fixture unavailable") }
			}
			n := 0
			if failure != "heap" {
				o.clock = func() (int64, error) {
					n++
					if failure == "clock-before" || failure == "clock-after" && n == 2 {
						return 0, errors.New("fixture clock")
					}
					if failure == "regressing-clock" {
						return int64(3 - n), nil
					}
					return int64(n), nil
				}
			}
			w := observerContractResponse(t, qualificationRuntimeHandlerWithObserver(nil, nil, nil, o), "GET", "/?observation=status-v1")
			if mem.Load() != 0 {
				t.Fatal("status invoked MemStats")
			}
			if _, err := decodeQualificationObserverResponse(w.Body, qualificationPoll); err == nil {
				t.Fatal("invalid sample accepted")
			}
		})
	}
	o, _, _ := observerContractReader(qualificationObserverSplit)
	w := observerContractResponse(t, qualificationRuntimeHandlerWithObserver(nil, nil, nil, o), "GET", "/?observation=status-v1")
	if _, err := decodeQualificationObserverResponse(bytes.NewReader(w.Body.Bytes()), qualificationBoundary); err == nil {
		t.Fatal("poll accepted as boundary")
	}
	if _, err := decodeQualificationObserverResponse(strings.NewReader(strings.Repeat("x", (32<<10)+1)), qualificationPoll); err == nil {
		t.Fatal("over-limit accepted")
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	body["total_alloc_bytes"] = 0
	data, _ := json.Marshal(body)
	if _, err := decodeQualificationObserverResponse(bytes.NewReader(data), qualificationPoll); err == nil {
		t.Fatal("manufactured memory field accepted")
	}
	legacy, _, _ := observerContractReader("")
	w = observerContractResponse(t, qualificationRuntimeHandlerWithObserver(nil, nil, nil, legacy), "GET", "/")
	if _, err := decodeQualificationObserverResponse(w.Body, qualificationBoundary); err == nil {
		t.Fatal("missing capability accepted")
	}
}
func TestQualificationObserverLiveEventsOwnersAndFlushOrder(t *testing.T) {
	app, _ := qualificationMemoryCensusTestApp(t)
	o, mem, heap := observerContractReader(qualificationObserverSplit)
	h := qualificationRuntimeHandlerWithObserver(app, nil, nil, o)
	w := observerContractResponse(t, h, "GET", "/?observation=status-v1&events=1&owners=1")
	got, err := decodeQualificationObserverResponse(w.Body, qualificationEventProbe)
	if err != nil || !got.EventStats || !got.OwnerStats || got.PersistedEvents != 0 || mem.Load() != 0 || heap.Load() != 1 {
		t.Fatalf("status=%+v err=%v", got, err)
	}
	// NewApp constructs this recorder but does not start it. Queue one already
	// normalized event directly so no ticker can persist it before the handler's
	// memory boundary. This is test-owned setup of the existing bounded queue;
	// the real handler/FlushObservabilityRecorder/SQLite transaction do the drain.
	recorder := app.observabilityRecorder
	if recorder == nil || len(recorder.events) != 0 || recorder.pendingEvents.Load() != 0 {
		t.Fatal("expected fresh empty recorder")
	}
	event := db.InsertProxyRequestEventAtParams{OccurredAt: time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC), StatusCode: 201, DurationMs: 7, Method: "GET", Host: "observer.test", PathPrefix: "/queued-before-boundary", ResponseBytes: 19}
	recorder.events <- event
	o.readMem = func(s *runtime.MemStats) {
		mem.Add(1)
		s.TotalAlloc = 303
		var persisted int64
		if err := app.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM proxy_request_events`).Scan(&persisted); err != nil {
			t.Error(err)
		}
		if len(recorder.events) != 1 || recorder.pendingEvents.Load() != 0 || persisted != 0 {
			t.Errorf("MemStats must precede actual drain: queued=%d pending=%d persisted=%d", len(recorder.events), recorder.pendingEvents.Load(), persisted)
		}
	}
	w = observerContractResponse(t, h, "GET", "/?flush=1")
	got, err = decodeQualificationObserverResponse(w.Body, qualificationBoundary)
	if err != nil || got.TotalAlloc != 303 || !got.EventStats || got.PersistedEvents != 1 || got.PendingEvents != 0 || got.QueuedEvents != 0 || got.DroppedEvents != 0 || mem.Load() != 1 {
		t.Fatalf("drain=%+v err=%v", got, err)
	}
	if len(recorder.events) != 0 || recorder.pendingEvents.Load() != 0 {
		t.Fatal("actual recorder did not drain")
	}
	var status, duration, responseBytes, rollup int64
	var method, host, path string
	if err := app.DB.QueryRowContext(t.Context(), `SELECT status_code,duration_ms,method,host,path_prefix,response_bytes FROM proxy_request_events`).Scan(&status, &duration, &method, &host, &path, &responseBytes); err != nil {
		t.Fatal(err)
	}
	if status != event.StatusCode || duration != event.DurationMs || method != event.Method || host != event.Host || path != event.PathPrefix || responseBytes != event.ResponseBytes {
		t.Fatal("drained event differs")
	}
	if err := app.DB.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(requests),0) FROM proxy_request_tuple_rollup_minutes`).Scan(&rollup); err != nil || rollup != 1 {
		t.Fatalf("real flush rollup=%d err=%v", rollup, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/?observation=status-v1&events=1&owners=1", nil).WithContext(ctx))
	got, err = decodeQualificationObserverResponse(w.Body, qualificationEventProbe)
	if err != nil || got.EventStats || got.EventStatsError == "" || !got.OwnerStats || mem.Load() != 1 {
		t.Fatalf("event query cancellation lost: %+v / %v", got, err)
	}
}
func TestQualificationObserverSamplerJoinAndAccounting(t *testing.T) {
	t.Setenv("P2PSTREAM_QUALIFICATION_OBSERVER_MODE", qualificationObserverSplit)
	o, _, _ := observerContractReader(qualificationObserverSplit)
	var mu sync.Mutex
	order := []string{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var first sync.Once
	var allocations atomic.Uint64
	o.readMem = func(s *runtime.MemStats) {
		s.TotalAlloc = allocations.Add(10)
		s.NumGC = uint32(s.TotalAlloc / 10)
		s.PauseTotalNs = s.TotalAlloc * 2
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		purpose, status, err := o.purpose(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		label := string(purpose)
		if r.URL.Query().Has("flush") {
			label = "drain"
		}
		mu.Lock()
		order = append(order, label)
		mu.Unlock()
		if status {
			first.Do(func() { close(entered); <-release })
		}
		var m runtime.MemStats
		reading := o.reading(purpose, status, &m)
		_ = writeQualificationObserverRuntime(w, qualificationRuntime{Observer: reading, TotalAlloc: m.TotalAlloc, NumGC: m.NumGC, PauseNS: m.PauseTotalNs, EventStats: !status, PersistedEvents: 5, OwnerStats: true, OwnerBytes: map[string]int64{"fixture": 29}, AgentBallastBytes: 31})
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	s := newQualificationSampler("", server.URL, true)
	s.start()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sample did not enter")
	}
	s.signalStop()
	stopped := s.samplingStopped
	joined := make(chan struct{})
	go func() { s.finishSampling(); close(joined) }()
	close(release)
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("sample did not join")
	}
	s.flushEventRecorder()
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 4 || order[0] != "boundary" || order[len(order)-2] != "boundary" || order[len(order)-1] != "drain" {
		t.Fatalf("order=%v", order)
	}
	for _, label := range order[1 : len(order)-2] {
		if label != "poll" {
			t.Fatalf("order=%v", order)
		}
	}
	summary := s.observerSummary()
	if !summary.Valid || !summary.BoundaryValid || summary.Drain == nil || *summary.TotalAllocDelta != 10 || *summary.GCDelta != 1 || *summary.PauseDelta != 20 || summary.PollAttempts != uint64(len(order)-3) || summary.PollSuccesses != summary.PollAttempts || *summary.PeakHeapObjects != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if s.peakHeap != 0 || s.ownerPeakBytes["fixture"] != 29 || s.initialEvents.PersistedEvents != 5 || s.afterDrainEvents.PersistedEvents != 5 || !s.samplingStopped.Equal(stopped) {
		t.Fatal("resource/events/CPU boundary changed")
	}
	s.observerState.Final.TotalAlloc = 0
	if s.observerSummary().BoundaryValid || s.observerSummary().Valid || s.observerSummary().TotalAllocDelta != nil {
		t.Fatal("regressing boundary accepted")
	}
	s.observerState.Final = nil
	if s.observerSummary().Valid {
		t.Fatal("missing boundary accepted")
	}
}
func TestQualificationObserverConcurrentAdminAndShutdown(t *testing.T) {
	o, mem, heap := observerContractReader(qualificationObserverSplit)
	srv := httptest.NewServer(qualificationRuntimeHandlerWithObserver(nil, nil, nil, o))
	t.Cleanup(srv.Close)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := srv.URL + "/?observation=status-v1"
			purpose := qualificationPoll
			if i%2 == 0 {
				url = srv.URL
				purpose = qualificationBoundary
			}
			resp, err := srv.Client().Get(url)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			if _, err := decodeQualificationObserverResponse(resp.Body, purpose); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// A completed request may have reused another connection while its own
	// dial finished unused. Close the client pool before graceful shutdown:
	// net/http otherwise keeps a fresh StateNew connection for over 5 seconds.
	// This does not interrupt active handlers or relax the shutdown deadline.
	srv.Client().CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if mem.Load() != 6 || heap.Load() != 6 {
		t.Fatalf("mem=%d heap=%d", mem.Load(), heap.Load())
	}
}
func TestQualificationObserverShutdownJoinsActiveRead(t *testing.T) {
	o, _, _ := observerContractReader(qualificationObserverSplit)
	entered, released := make(chan struct{}), make(chan struct{})
	o.readHeap = func() (uint64, error) { close(entered); <-released; return 7, nil }
	srv := httptest.NewServer(qualificationRuntimeHandlerWithObserver(nil, nil, nil, o))
	defer srv.Close()
	defer func() {
		select {
		case <-released:
		default:
			close(released)
		}
	}()
	responseDone := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Get(srv.URL + "/?observation=status-v1")
		if err == nil {
			_, err = decodeQualificationObserverResponse(resp.Body, qualificationPoll)
			resp.Body.Close()
		}
		responseDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- srv.Config.Shutdown(ctx) }()
	// An active handler cannot finish until its per-handler reader returns.
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before active reader: %v", err)
	default:
	}
	close(released)
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func TestQualificationObserverDiagnosticPresence(t *testing.T) {
	d := &qualificationLatencyDiagnostic{observerMode: qualificationObserverSplit, epoch: time.Now().Add(-time.Second), now: time.Now, observations: make([]qualificationLatencyObservation, 0, 8)}
	d.observeAt("driver_sample", 1, 2, qualificationRuntime{}, true)
	d.observeRuntime(3, qualificationRuntime{}, false, qualificationPoll)
	heap := uint64(0)
	d.observeRuntime(4, qualificationRuntime{Observer: &qualificationObserverReading{Source: "runtime/metrics:" + qualificationObserverHeapKey, HeapObjects: &heap}}, true, qualificationPoll)
	d.memStats(5, 6, &runtime.MemStats{NumGC: 2, PauseTotalNs: 3, HeapAlloc: 4, TotalAlloc: 5})
	for i, observation := range d.observations {
		data, err := json.Marshal(observation)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		_ = json.Unmarshal(data, &fields)
		for _, key := range []string{"NumGC", "PauseNS", "HeapAlloc", "TotalAlloc"} {
			if _, ok := fields[key]; ok {
				t.Fatal("schema2 manufactured legacy memory")
			}
		}
		if fields["memory_present"] != (i >= 2) {
			t.Fatalf("observation=%s", data)
		}
	}
	legacy := qualificationLatencyObservation{Kind: "driver_sample", Start: 1, End: 2, OK: true}
	data, _ := json.Marshal(legacy)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	if len(fields) != 8 || fields["NumGC"] != float64(0) {
		t.Fatalf("legacy JSON=%s", data)
	}
}

func TestQualificationObserverDiagnosticReceiptVersion(t *testing.T) {
	d, _ := qualificationLatencyFake(t, 0)
	d.role = "proxy"
	d.observerMode = qualificationObserverSplit
	d.memStats(1, 2, &runtime.MemStats{TotalAlloc: 7})
	if d.finish() == nil {
		t.Fatal("unarmed trace accepted")
	}
	data, err := os.ReadFile(filepath.Join(d.dir, "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err = json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["version"] != float64(2) || receipt["observer_mode"] != qualificationObserverSplit || receipt["complete"] != false {
		t.Fatalf("receipt=%s", data)
	}
	if len(receipt["observations"].([]any)) != 1 {
		t.Fatal("partial evidence lost")
	}
	if _, err := qualificationLatencyFromEnv(func(key string) string {
		if key == "P2PSTREAM_QUALIFICATION_OBSERVER_MODE" {
			return "unknown"
		}
		return ""
	}, "load"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
