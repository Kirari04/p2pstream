//go:build linux

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/metrics"
	"strings"

	"golang.org/x/sys/unix"
)

const qualificationObserverSplit = "split-v1"
const qualificationObserverHeapKey = "/memory/classes/heap/objects:bytes"

type qualificationRuntimePurpose string

const (
	qualificationPoll       qualificationRuntimePurpose = "poll"
	qualificationEventProbe qualificationRuntimePurpose = "event-probe"
	qualificationBoundary   qualificationRuntimePurpose = "boundary"
)

// The dependencies belong to this handler. No mutable package-global reader,
// cache, background refresher, retry or forced collection is introduced.
type qualificationObserver struct {
	mode      string
	configErr error
	readMem   func(*runtime.MemStats)
	readHeap  func() (uint64, error)
	clock     func() (int64, error)
}

type qualificationObserverReading struct {
	Schema          int                         `json:"schema"`
	Mode            string                      `json:"mode"`
	Purpose         qualificationRuntimePurpose `json:"purpose"`
	Source          string                      `json:"source"`
	MemStatsPresent bool                        `json:"memstats_present"`
	HeapObjects     *uint64                     `json:"heap_objects_bytes,omitempty"`
	Start           int64                       `json:"start_monotonic_ns"`
	End             int64                       `json:"end_monotonic_ns"`
	Valid           bool                        `json:"valid"`
	Error           string                      `json:"error,omitempty"`
}

func qualificationObserverMode(raw string) (string, error) {
	if raw != "" && raw != qualificationObserverSplit {
		return "", errors.New("unsupported qualification observer mode")
	}
	return raw, nil
}
func qualificationObserverClock() (int64, error) {
	var value unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &value); err != nil {
		return 0, err
	}
	return value.Nano(), nil
}
func qualificationObserverHeap(read func([]metrics.Sample)) (uint64, error) {
	samples := [1]metrics.Sample{{Name: qualificationObserverHeapKey}}
	read(samples[:])
	if samples[0].Name != qualificationObserverHeapKey || samples[0].Value.Kind() != metrics.KindUint64 {
		return 0, errors.New("heap objects metric unavailable or wrong kind")
	}
	return samples[0].Value.Uint64(), nil
}
func newQualificationObserver(mode string) *qualificationObserver {
	checked, err := qualificationObserverMode(mode)
	return &qualificationObserver{mode: checked, configErr: err, readMem: runtime.ReadMemStats, readHeap: func() (uint64, error) { return qualificationObserverHeap(metrics.Read) }, clock: qualificationObserverClock}
}

// Only the new read-only selector receives a new query policy. Legacy controls
// keep their existing admission rules, including census and pre-flush MemStats.
func (o *qualificationObserver) purpose(r *http.Request) (qualificationRuntimePurpose, bool, error) {
	if o.configErr != nil {
		return "", false, o.configErr
	}
	selected := false
	// Discover only a bounded decoded key, without allocating an unbounded
	// values map or changing any legacy request whose selector is absent.
	for rest := r.URL.RawQuery; rest != ""; {
		var pair string
		pair, rest, _ = strings.Cut(rest, "&")
		key, _, _ := strings.Cut(pair, "=")
		if len(key) <= 3*len("observation") {
			decoded, decodeErr := url.QueryUnescape(key)
			if decodeErr == nil && decoded == "observation" {
				selected = true
				break
			}
		}
	}
	if !selected {
		return qualificationBoundary, false, nil
	}
	if len(r.URL.RawQuery) > 512 {
		return "", false, errors.New("observer query too long")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false, err
	}
	values := query["observation"]
	if o.mode != qualificationObserverSplit || r.Method != http.MethodGet || r.URL.Path != "/" || len(values) != 1 || values[0] != "status-v1" {
		return "", false, errors.New("unsupported status observation request")
	}
	for key, values := range query {
		if key != "observation" && key != "events" && key != "owners" {
			return "", false, errors.New("status observation cannot perform controls")
		}
		if len(values) != 1 || key != "observation" && values[0] != "1" {
			return "", false, errors.New("invalid or duplicate observation selector")
		}
	}
	if query.Has("events") {
		return qualificationEventProbe, true, nil
	}
	return qualificationPoll, true, nil
}
func (o *qualificationObserver) reading(purpose qualificationRuntimePurpose, status bool, stats *runtime.MemStats) *qualificationObserverReading {
	if o.mode == "" {
		o.readMem(stats)
		return nil
	}
	r := &qualificationObserverReading{Schema: 1, Mode: o.mode, Purpose: purpose, Source: "runtime.ReadMemStats"}
	if status {
		r.Source = "runtime/metrics:" + qualificationObserverHeapKey
	}
	before, err := o.clock()
	r.Start = before
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if status {
		r.Source = "runtime/metrics:" + qualificationObserverHeapKey
		value, readErr := o.readHeap()
		if readErr != nil {
			r.Error = readErr.Error()
		} else {
			r.HeapObjects = &value
		}
	} else {
		o.readMem(stats)
		r.MemStatsPresent = true
	}
	after, clockErr := o.clock()
	r.End = after
	if clockErr != nil {
		r.Error = clockErr.Error()
	} else if before <= 0 || after < before {
		r.Error = "invalid memory observation clock bracket"
	}
	r.Valid = r.Error == ""
	return r
}

// Shadow only the eight historical MemStats fields for status responses. The
// remainder is the live ordinary response, including owner/resource errors.
func writeQualificationObserverRuntime(w io.Writer, result qualificationRuntime) error {
	if result.Observer == nil || result.Observer.MemStatsPresent {
		return json.NewEncoder(w).Encode(result)
	}
	return json.NewEncoder(w).Encode(struct {
		qualificationRuntime
		TotalAlloc   *uint64 `json:"total_alloc_bytes,omitempty"`
		HeapInuse    *uint64 `json:"heap_inuse_bytes,omitempty"`
		HeapIdle     *uint64 `json:"heap_idle_bytes,omitempty"`
		HeapReleased *uint64 `json:"heap_released_bytes,omitempty"`
		HeapAlloc    *uint64 `json:"heap_alloc_bytes,omitempty"`
		HeapSys      *uint64 `json:"heap_sys_bytes,omitempty"`
		NumGC        *uint32 `json:"num_gc,omitempty"`
		PauseNS      *uint64 `json:"pause_total_ns,omitempty"`
	}{qualificationRuntime: result})
}

func decodeQualificationObserverResponse(body io.Reader, purpose qualificationRuntimePurpose) (qualificationRuntime, error) {
	if purpose != qualificationPoll && purpose != qualificationEventProbe && purpose != qualificationBoundary {
		return qualificationRuntime{}, errors.New("unknown observer purpose")
	}
	data, err := io.ReadAll(io.LimitReader(body, (32<<10)+1))
	if err != nil {
		return qualificationRuntime{}, err
	}
	if len(data) > 32<<10 {
		return qualificationRuntime{}, errors.New("observer response too large")
	}
	var result qualificationRuntime
	if err = json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	r := result.Observer
	if r == nil || r.Schema != 1 || r.Mode != qualificationObserverSplit || r.Purpose != purpose || !r.Valid || r.Error != "" || r.Start <= 0 || r.End < r.Start {
		return result, errors.New("observer capability, purpose or sample invalid")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		return result, err
	}
	boundary := purpose == qualificationBoundary
	for _, key := range []string{"total_alloc_bytes", "heap_inuse_bytes", "heap_idle_bytes", "heap_released_bytes", "heap_alloc_bytes", "heap_sys_bytes", "num_gc", "pause_total_ns"} {
		raw, present := fields[key]
		if present != boundary || present && string(raw) == "null" {
			return result, fmt.Errorf("observer memory presence mismatch: %s", key)
		}
	}
	if boundary {
		if !r.MemStatsPresent || r.Source != "runtime.ReadMemStats" || r.HeapObjects != nil {
			return result, errors.New("boundary memory source mismatch")
		}
	} else if r.MemStatsPresent || r.Source != "runtime/metrics:"+qualificationObserverHeapKey || r.HeapObjects == nil {
		return result, errors.New("status heap metric missing or source mismatch")
	}
	return result, nil
}

func requireQualificationObserverMode(t interface {
	Helper()
	Fatal(...any)
}) {
	t.Helper()
	if _, err := qualificationObserverMode(os.Getenv("P2PSTREAM_QUALIFICATION_OBSERVER_MODE")); err != nil {
		t.Fatal(err)
	}
}

type qualificationObserverBoundarySnapshot struct {
	Reading    *qualificationObserverReading `json:"reading"`
	TotalAlloc uint64                        `json:"total_alloc_bytes"`
	HeapAlloc  uint64                        `json:"heap_alloc_bytes"`
	NumGC      uint32                        `json:"num_gc"`
	PauseNS    uint64                        `json:"pause_total_ns"`
}
type qualificationObserverSummary struct {
	Schema          int                                    `json:"schema"`
	Mode            string                                 `json:"mode"`
	HeapSource      string                                 `json:"sampled_heap_source"`
	PollAttempts    uint64                                 `json:"poll_attempts"`
	PollSuccesses   uint64                                 `json:"poll_successes"`
	PollErrors      uint64                                 `json:"poll_errors"`
	FirstPollStart  int64                                  `json:"first_poll_start_monotonic_ns"`
	LastPollEnd     int64                                  `json:"last_poll_end_monotonic_ns"`
	PeakHeapObjects *uint64                                `json:"sampled_peak_heap_objects_bytes,omitempty"`
	Initial         *qualificationObserverBoundarySnapshot `json:"initial_boundary,omitempty"`
	Final           *qualificationObserverBoundarySnapshot `json:"final_boundary,omitempty"`
	Drain           *qualificationObserverBoundarySnapshot `json:"pre_drain_memory_boundary,omitempty"`
	BoundaryValid   bool                                   `json:"boundary_valid"`
	TotalAllocDelta *uint64                                `json:"total_alloc_delta,omitempty"`
	GCDelta         *uint32                                `json:"gc_delta,omitempty"`
	PauseDelta      *uint64                                `json:"pause_delta_ns,omitempty"`
	Valid           bool                                   `json:"valid"`
	Error           string                                 `json:"error,omitempty"`
}

func qualificationObserverBoundary(stats qualificationRuntime, ok bool) *qualificationObserverBoundarySnapshot {
	r := stats.Observer
	if !ok || r == nil || !r.Valid || r.Schema != 1 || r.Mode != qualificationObserverSplit || r.Error != "" || !r.MemStatsPresent || r.Purpose != qualificationBoundary || r.Source != "runtime.ReadMemStats" {
		return nil
	}
	return &qualificationObserverBoundarySnapshot{Reading: r, TotalAlloc: stats.TotalAlloc, HeapAlloc: stats.HeapAlloc, NumGC: stats.NumGC, PauseNS: stats.PauseNS}
}
func (s *qualificationSampler) observerSummary() *qualificationObserverSummary {
	if s.observerMode == "" {
		return nil
	}
	r := s.observerState
	r.Schema = 1
	r.Mode = s.observerMode
	r.HeapSource = "runtime/metrics:" + qualificationObserverHeapKey
	if r.Initial != nil && r.Final != nil && r.Final.Reading.Start >= r.Initial.Reading.End && r.Final.TotalAlloc >= r.Initial.TotalAlloc && r.Final.NumGC >= r.Initial.NumGC && r.Final.PauseNS >= r.Initial.PauseNS {
		alloc, gc, pause := r.Final.TotalAlloc-r.Initial.TotalAlloc, r.Final.NumGC-r.Initial.NumGC, r.Final.PauseNS-r.Initial.PauseNS
		r.BoundaryValid = true
		r.TotalAllocDelta = &alloc
		r.GCDelta = &gc
		r.PauseDelta = &pause
	}
	r.Valid = r.Mode == qualificationObserverSplit && r.BoundaryValid && r.PollAttempts > 0 && r.PollAttempts == r.PollSuccesses && r.PollErrors == 0 && r.PeakHeapObjects != nil && r.FirstPollStart >= r.Initial.Reading.End && r.LastPollEnd <= r.Final.Reading.Start
	if !r.Valid {
		r.Error = "missing, failed or regressing observer sample/boundary"
	}
	return &r
}
func (s *qualificationSampler) observePoll(stats qualificationRuntime, ok bool) {
	if s.observerMode == "" {
		return
	}
	s.observerState.PollAttempts++
	r := stats.Observer
	if !ok || r == nil || !r.Valid || r.MemStatsPresent || r.HeapObjects == nil {
		s.observerState.PollErrors++
		return
	}
	s.observerState.PollSuccesses++
	if s.observerState.FirstPollStart == 0 {
		s.observerState.FirstPollStart = r.Start
	}
	s.observerState.LastPollEnd = r.End
	if s.observerState.PeakHeapObjects == nil || *r.HeapObjects > *s.observerState.PeakHeapObjects {
		value := *r.HeapObjects
		s.observerState.PeakHeapObjects = &value
	}
}

// Schema 2 records presence, rather than emitting zero MemStats for a poll or
// unsuccessful admin read. The binary request record and elapsed value stay
// unchanged. These local brackets are not a cross-clock physical error bound.
type qualificationLatencyMemStats struct {
	NumGC                          uint32
	PauseNS, HeapAlloc, TotalAlloc uint64
}
type qualificationLatencyObserverObservation struct {
	Purpose     qualificationRuntimePurpose   `json:"purpose"`
	Source      string                        `json:"source"`
	MemStats    *qualificationLatencyMemStats `json:"memstats,omitempty"`
	HeapObjects *uint64                       `json:"heap_objects_bytes,omitempty"`
	Reading     *qualificationObserverReading `json:"reading,omitempty"`
}

func (o qualificationLatencyObservation) MarshalJSON() ([]byte, error) {
	if o.observer == nil {
		type legacy qualificationLatencyObservation
		return json.Marshal(legacy(o))
	}
	return json.Marshal(struct {
		Kind          string `json:"kind"`
		Start, End    int64
		OK            bool
		Purpose       qualificationRuntimePurpose   `json:"purpose"`
		Source        string                        `json:"source"`
		MemoryPresent bool                          `json:"memory_present"`
		MemStats      *qualificationLatencyMemStats `json:"memstats,omitempty"`
		HeapObjects   *uint64                       `json:"heap_objects_bytes,omitempty"`
		Reading       *qualificationObserverReading `json:"reading,omitempty"`
	}{o.Kind, o.Start, o.End, o.OK, o.observer.Purpose, o.observer.Source, o.observer.MemStats != nil || o.observer.HeapObjects != nil, o.observer.MemStats, o.observer.HeapObjects, o.observer.Reading})
}
