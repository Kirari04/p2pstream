//go:build linux

package agent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"runtime/metrics"
	"strings"

	"golang.org/x/sys/unix"
)

const externalQualificationObserverSplit = "split-v1"
const externalQualificationObserverHeapKey = "/memory/classes/heap/objects:bytes"

type externalQualificationRuntimePurpose string

const (
	externalQualificationPoll       externalQualificationRuntimePurpose = "poll"
	externalQualificationEventProbe externalQualificationRuntimePurpose = "event-probe"
	externalQualificationBoundary   externalQualificationRuntimePurpose = "boundary"
)

// The dependencies belong to this handler. No mutable package-global reader,
// cache, background refresher, retry or forced collection is introduced.
type externalQualificationObserver struct {
	mode      string
	configErr error
	readMem   func(*runtime.MemStats)
	readHeap  func() (uint64, error)
	clock     func() (int64, error)
}

type externalQualificationObserverReading struct {
	Schema          int                                 `json:"schema"`
	Mode            string                              `json:"mode"`
	Purpose         externalQualificationRuntimePurpose `json:"purpose"`
	Source          string                              `json:"source"`
	MemStatsPresent bool                                `json:"memstats_present"`
	HeapObjects     *uint64                             `json:"heap_objects_bytes,omitempty"`
	Start           int64                               `json:"start_monotonic_ns"`
	End             int64                               `json:"end_monotonic_ns"`
	Valid           bool                                `json:"valid"`
	Error           string                              `json:"error,omitempty"`
}

func externalQualificationObserverMode(raw string) (string, error) {
	if raw != "" && raw != externalQualificationObserverSplit {
		return "", errors.New("unsupported qualification observer mode")
	}
	return raw, nil
}
func externalQualificationObserverClock() (int64, error) {
	var value unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &value); err != nil {
		return 0, err
	}
	return value.Nano(), nil
}
func externalQualificationObserverHeap(read func([]metrics.Sample)) (uint64, error) {
	samples := [1]metrics.Sample{{Name: externalQualificationObserverHeapKey}}
	read(samples[:])
	if samples[0].Name != externalQualificationObserverHeapKey || samples[0].Value.Kind() != metrics.KindUint64 {
		return 0, errors.New("heap objects metric unavailable or wrong kind")
	}
	return samples[0].Value.Uint64(), nil
}
func newExternalQualificationObserver(mode string) *externalQualificationObserver {
	checked, err := externalQualificationObserverMode(mode)
	return &externalQualificationObserver{mode: checked, configErr: err, readMem: runtime.ReadMemStats, readHeap: func() (uint64, error) { return externalQualificationObserverHeap(metrics.Read) }, clock: externalQualificationObserverClock}
}

// Only the new read-only selector receives a new query policy. Legacy controls
// keep their existing admission rules, including census and pre-flush MemStats.
func (o *externalQualificationObserver) purpose(r *http.Request) (externalQualificationRuntimePurpose, bool, error) {
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
		return externalQualificationBoundary, false, nil
	}
	if len(r.URL.RawQuery) > 512 {
		return "", false, errors.New("observer query too long")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false, err
	}
	values := query["observation"]
	if o.mode != externalQualificationObserverSplit || r.Method != http.MethodGet || r.URL.Path != "/" || len(values) != 1 || values[0] != "status-v1" {
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
		return externalQualificationEventProbe, true, nil
	}
	return externalQualificationPoll, true, nil
}
func (o *externalQualificationObserver) reading(purpose externalQualificationRuntimePurpose, status bool, stats *runtime.MemStats) *externalQualificationObserverReading {
	if o.mode == "" {
		o.readMem(stats)
		return nil
	}
	r := &externalQualificationObserverReading{Schema: 1, Mode: o.mode, Purpose: purpose, Source: "runtime.ReadMemStats"}
	if status {
		r.Source = "runtime/metrics:" + externalQualificationObserverHeapKey
	}
	before, err := o.clock()
	r.Start = before
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if status {
		r.Source = "runtime/metrics:" + externalQualificationObserverHeapKey
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
func writeExternalQualificationObserverRuntime(w io.Writer, result externalQualificationAgentRuntime) error {
	if result.Observer == nil || result.Observer.MemStatsPresent {
		return json.NewEncoder(w).Encode(result)
	}
	return json.NewEncoder(w).Encode(struct {
		externalQualificationAgentRuntime
		TotalAlloc   *uint64 `json:"total_alloc_bytes,omitempty"`
		HeapInuse    *uint64 `json:"heap_inuse_bytes,omitempty"`
		HeapIdle     *uint64 `json:"heap_idle_bytes,omitempty"`
		HeapReleased *uint64 `json:"heap_released_bytes,omitempty"`
		HeapAlloc    *uint64 `json:"heap_alloc_bytes,omitempty"`
		HeapSys      *uint64 `json:"heap_sys_bytes,omitempty"`
		NumGC        *uint32 `json:"num_gc,omitempty"`
		PauseNS      *uint64 `json:"pause_total_ns,omitempty"`
	}{externalQualificationAgentRuntime: result})
}

func externalQualificationObserverHandler(observer *externalQualificationObserver, control func(http.ResponseWriter, *http.Request) bool, snapshot func(*externalQualificationAgentRuntime)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		purpose, status, err := observer.purpose(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if control(w, r) {
			return
		}
		var stats runtime.MemStats
		reading := observer.reading(purpose, status, &stats)
		result := externalQualificationAgentRuntime{Observer: reading, TotalAlloc: stats.TotalAlloc, HeapInuse: stats.HeapInuse, HeapIdle: stats.HeapIdle, HeapReleased: stats.HeapReleased, HeapAlloc: stats.HeapAlloc, HeapSys: stats.HeapSys, NumGC: stats.NumGC, PauseNS: stats.PauseTotalNs}
		snapshot(&result)
		_ = writeExternalQualificationObserverRuntime(w, result)
	})
}
