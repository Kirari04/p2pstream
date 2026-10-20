//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExternalQualificationObserverSeparation(t *testing.T) {
	var mem, heap, controls, clock atomic.Int64
	o := newExternalQualificationObserver(externalQualificationObserverSplit)
	o.readMem = func(s *runtime.MemStats) { mem.Add(1); s.TotalAlloc = 10 }
	o.readHeap = func() (uint64, error) { heap.Add(1); return 17, nil }
	o.clock = func() (int64, error) { return clock.Add(1), nil }
	h := externalQualificationObserverHandler(o, func(http.ResponseWriter, *http.Request) bool { controls.Add(1); return false }, func(s *externalQualificationAgentRuntime) {
		s.BallastBytes = 123
		s.Capacity = &externalQualificationAgentCapacity{InUse: 5, Maximum: 13, SampleError: "fixture transient", LastGoodSampleUnixNano: 37, PressureReason: "fixture pressure", MemorySource: "fixture source", MemoryUsedBytes: 89}
		s.TunnelSessions = []externalQualificationAgentTunnelSession{{Lane: 1, Aggregate: true, SessionIdentity: "fixture", ReceiveLimitBytes: 97, ReceiveAvailableBytes: 53}}
	})
	var baseline map[string]any
	for i, target := range []string{"/", "/?observation=status-v1", "/?observation=status-v1&events=1"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 200 {
			t.Fatalf("status=%d %s", w.Code, w.Body.String())
		}
		var fields map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"total_alloc_bytes", "heap_inuse_bytes", "heap_idle_bytes", "heap_released_bytes", "heap_alloc_bytes", "heap_sys_bytes", "num_gc", "pause_total_ns"} {
			_, present := fields[key]
			if present != (i == 0) {
				t.Fatalf("%s presence=%v", key, present)
			}
			delete(fields, key)
		}
		delete(fields, "observer")
		if i == 0 {
			baseline = fields
		} else if !reflect.DeepEqual(fields, baseline) {
			t.Fatalf("live resource fields changed: %v / %v", fields, baseline)
		}
	}
	if mem.Load() != 1 || heap.Load() != 2 || controls.Load() != 3 {
		t.Fatal("reader separation failed")
	}
	for _, target := range []string{"/?observation=status-v1&ballast_mib=1", "/?observation=status-v1&growth_snapshot=drain", "/?observation=status-v1&credit_snapshot=1", "/?observation=status-v1&memory_census=1", "/retirement?observation=status-v1", "/?observation=status-v1&bad=%ZZ", "/?observation=status-v1&observ%61tion=status-v1", "/?observation=bad", "/?observation=status-v1&x=" + strings.Repeat("a", 512)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 400 {
			t.Fatalf("accepted %s", target)
		}
	}
	if mem.Load() != 1 || heap.Load() != 2 || controls.Load() != 3 {
		t.Fatal("invalid selector reached control/reader")
	}
	o.readHeap = func() (uint64, error) { return 0, errors.New("metric unavailable") }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/?observation=status-v1", nil))
	var result externalQualificationAgentRuntime
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Observer.Valid || result.Observer.HeapObjects != nil || result.Observer.MemStatsPresent || result.Observer.Error == "" {
		t.Fatalf("invalid metric became zero: %+v", result.Observer)
	}
	if _, err := externalQualificationObserverHeap(func([]metrics.Sample) {}); err == nil {
		t.Fatal("KindBad accepted")
	}
	if _, err := externalQualificationObserverHeap(metrics.Read); err != nil {
		t.Fatal(err)
	}
	legacy := newExternalQualificationObserver("")
	legacy.readMem = o.readMem
	legacyHandler := externalQualificationObserverHandler(legacy, func(http.ResponseWriter, *http.Request) bool { return false }, func(*externalQualificationAgentRuntime) {})
	w = httptest.NewRecorder()
	legacyHandler.ServeHTTP(w, httptest.NewRequest("GET", "/?ignored=%ZZ", nil))
	var fields map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &fields)
	if w.Code != 200 || fields["total_alloc_bytes"] != float64(10) {
		t.Fatalf("legacy=%s", w.Body.String())
	}
	if _, exists := fields["observer"]; exists {
		t.Fatal("legacy observer field")
	}
}
func TestExternalQualificationObserverConcurrentShutdown(t *testing.T) {
	var mem, heap, clock atomic.Int64
	o := newExternalQualificationObserver(externalQualificationObserverSplit)
	o.readMem = func(*runtime.MemStats) { mem.Add(1) }
	o.readHeap = func() (uint64, error) { heap.Add(1); return 0, nil }
	o.clock = func() (int64, error) { return clock.Add(1), nil }
	srv := httptest.NewServer(externalQualificationObserverHandler(o, func(http.ResponseWriter, *http.Request) bool { return false }, func(*externalQualificationAgentRuntime) {}))
	t.Cleanup(srv.Close)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target := srv.URL
			if i%2 == 1 {
				target += "/?observation=status-v1"
			}
			resp, err := srv.Client().Get(target)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			var result externalQualificationAgentRuntime
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Error(err)
			} else if !result.Observer.Valid {
				t.Error("invalid observation")
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
		t.Fatal("reader counts")
	}
}
