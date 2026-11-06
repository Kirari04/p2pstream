//go:build linux

package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const (
	qualificationLatencyJobs           = 263660
	qualificationLatencyObservations   = 4096
	qualificationLatencyTraceCap       = 64 << 20
	qualificationLatencyRecordBytes    = 96
	qualificationLatencyTraceTolerance = 100 * time.Millisecond
	qualificationLatencyPrefix         = "P2PSTREAM_QUALIFICATION_LATENCY_"
)

// The dispatcher owns Scheduled/Ready, the worker owns the remaining ordinary
// fields. GotConn uses atomics because httptrace permits concurrent callbacks.
// No record is serialized until workers and the sampler have joined.
type qualificationLatencyRecord struct {
	Scheduled, Ready, Worker, Do, Headers, Body, Complete, Bookkeeping int64
	GotConn                                                            atomic.Int64
	Callbacks                                                          atomic.Uint32
	Reused                                                             atomic.Uint32
	Status                                                             int32
	Outcome                                                            uint32 // low byte: 1 success, 2 construction, 3 Do, 4 body; bits 8/9/10: read/Close/validation failure
	callbackMu                                                         sync.Mutex
	callbackClosed                                                     bool
	Elapsed                                                            int64 // EXACT value of the existing time.Since(started), not recomputed
}

type qualificationLatencyObservation struct {
	observer                       *qualificationLatencyObserverObservation
	Kind                           string `json:"kind"`
	Start, End                     int64
	OK                             bool
	NumGC                          uint32
	PauseNS, HeapAlloc, TotalAlloc uint64
}

type qualificationLatencyAnchor struct {
	MonoBefore, GoRelative, MonoAfter int64
}

type qualificationLatencyDiagnostic struct {
	role, dir          string
	observerMode       string
	phase              string
	scheduleOrigin     qualificationLatencyAnchor
	epoch              time.Time
	now                func() time.Time
	records            []qualificationLatencyRecord
	mu                 sync.Mutex
	observations       []qualificationLatencyObservation
	overflow           bool
	sealed             bool
	late               atomic.Uint64
	trace              *qualificationLatencyTrace
	loadStart, loadEnd int64
	plannedStart       int64
	joined             bool
	finishOnce         sync.Once
	finishErr          error
}

// Only the two Linux qualification subprocess roles use this opt-in. Neither
// production handlers nor ordinary qualification output gain a new field.
func qualificationLatencyFromEnv(get func(string) string, role string) (*qualificationLatencyDiagnostic, error) {
	observerMode, observerErr := qualificationObserverMode(get("P2PSTREAM_QUALIFICATION_OBSERVER_MODE"))
	if observerErr != nil {
		return nil, observerErr
	}
	enabled := get(qualificationLatencyPrefix + "DIAGNOSTIC")
	dir, start := get(qualificationLatencyPrefix+"DIR"), get(qualificationLatencyPrefix+"TRACE_START_NS")
	traceID := get(qualificationLatencyPrefix + "TRACE_ID")
	phase := get(qualificationLatencyPrefix + "PHASE")
	if enabled == "" {
		if dir != "" || start != "" || traceID != "" || phase != "" {
			return nil, errors.New("latency fields require explicit opt-in")
		}
		return nil, nil
	}
	if enabled != "1" || (role != "proxy" && role != "load") {
		return nil, errors.New("unsupported latency diagnostic role/mode")
	}
	if phase != "" && phase != "onset" {
		return nil, errors.New("unsupported latency diagnostic phase")
	}
	if len(traceID) != 32 {
		return nil, errors.New("latency trace ID must be 32 lowercase hex characters")
	}
	for _, c := range traceID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return nil, errors.New("invalid latency trace ID")
		}
	}
	if get("P2PSTREAM_QUALIFICATION_PROFILE_DIR") != "" || get("P2PSTREAM_QUALIFICATION_PROFILE_MODE") != "" || get("P2PSTREAM_QUALIFICATION_MEMORY_CENSUS") != "" || (get("P2PSTREAM_QUALIFICATION_BALLAST_MIB") != "" && get("P2PSTREAM_QUALIFICATION_BALLAST_MIB") != "0") {
		return nil, errors.New("latency diagnostic forbids profiles, census and ballast")
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("latency output must be an absolute clean fresh directory")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil || parent != filepath.Dir(dir) {
		return nil, errors.New("latency output parent must be canonical and existing")
	}
	if role == "proxy" && start != "" {
		return nil, errors.New("endpoint trace must be armed through owned admin")
	}
	if role == "load" {
		if get("P2PSTREAM_QUALIFICATION_SCENARIO") != "matched-h1-offered-20s" || get("P2PSTREAM_QUALIFICATION_PROTOCOL") != "h1" || get("P2PSTREAM_QUALIFICATION_CONCURRENCY") != "128" || get("P2PSTREAM_QUALIFICATION_REQUESTS") != "263660" || get("P2PSTREAM_QUALIFICATION_OFFERED_RPS") != "13183" || get("P2PSTREAM_QUALIFICATION_MATCHED_TLS") != "1" || get("P2PSTREAM_QUALIFICATION_DURATION_SECONDS") != "0" || get("P2PSTREAM_QUALIFICATION_WARMUP_SECONDS") != "0" || get("P2PSTREAM_QUALIFICATION_H2_PREWARM") != "" || get("P2PSTREAM_QUALIFICATION_AGENT_PROCESSES") != "" || get("P2PSTREAM_QUALIFICATION_NETWORK_TIMEOUT_SECONDS") != "" {
			return nil, errors.New("latency diagnostic requires the exact fixed matched H1/c128 offered cell")
		}
		n, err := strconv.ParseInt(start, 10, 64)
		if err != nil || n <= 0 || n > int64(^uint64(0)>>1)-int64(2*time.Second) || strconv.FormatInt(n, 10) != start {
			return nil, errors.New("invalid latency trace absolute start")
		}
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	d := &qualificationLatencyDiagnostic{role: role, dir: dir, phase: phase, observerMode: observerMode, epoch: time.Now(), now: time.Now, observations: make([]qualificationLatencyObservation, 0, qualificationLatencyObservations)}
	if role == "load" {
		d.records = make([]qualificationLatencyRecord, qualificationLatencyJobs)
	}
	d.trace = newQualificationLatencyTrace(filepath.Join(dir, "runtime.trace"), qualificationLatencyTraceCap)
	d.trace.role = role
	d.trace.id = traceID
	if role == "load" {
		d.plannedStart, _ = strconv.ParseInt(start, 10, 64)
	}
	return d, nil
}

func (d *qualificationLatencyDiagnostic) armDriver() error {
	if d == nil {
		return nil
	}
	if d.role != "load" {
		return errors.New("only driver owns environment trace window")
	}
	return d.trace.arm(d.plannedStart, d.plannedStart+int64(2*time.Second))
}

// Onset alone waits after ordinary setup/event flush and immediately before
// sampler.start. One wait plus a clock recheck is fail-closed: no polling,
// backdating, rearming or second sleep can move a missed fixed window.
func qualificationLatencyOnsetGate(phase string, start int64, mono func() (int64, error), sleep func(time.Duration)) error {
	if phase == "" {
		return nil
	}
	if phase != "onset" || start <= 0 || start > int64(^uint64(0)>>1)-int64(2*time.Second) {
		return errors.New("invalid onset gate")
	}
	now, err := mono()
	if err != nil {
		return err
	}
	if now <= 0 {
		return errors.New("invalid onset monotonic clock")
	}
	gate, latest := start+int64(250*time.Millisecond), start+int64(750*time.Millisecond)
	if now < gate {
		sleep(time.Duration(gate - now))
		now, err = mono()
		if err != nil {
			return err
		}
	}
	if now < gate || now > latest {
		return errors.New("missed fixed onset gate")
	}
	return nil
}

func (d *qualificationLatencyDiagnostic) beforeSampler() error {
	if d == nil {
		return nil
	}
	return qualificationLatencyOnsetGate(d.phase, d.plannedStart, qualificationLatencyMono, time.Sleep)
}

func (d *qualificationLatencyDiagnostic) recordScheduleOrigin(before int64, started time.Time, after int64) {
	if d != nil && d.phase == "onset" {
		d.scheduleOrigin = qualificationLatencyAnchor{before, started.Sub(d.epoch).Nanoseconds(), after}
	}
}

// This uses the log-anchor call brackets. Cross-role validation applies the
// same inequalities to both traces only after proving a shared clock domain.
func qualificationLatencyOnsetCoverage(origin qualificationLatencyAnchor, firstScheduled int64, result qualificationLatencyTraceResult) error {
	if !result.Armed || !result.Complete || qualificationLatencyTraceIntervals(result) != nil {
		return errors.New("incomplete onset trace")
	}
	if origin.MonoBefore <= 0 || origin.MonoAfter < origin.MonoBefore || origin.GoRelative <= 0 || firstScheduled != origin.GoRelative ||
		origin.MonoBefore < result.PlannedStart+int64(250*time.Millisecond) || origin.MonoAfter > result.PlannedStart+int64(750*time.Millisecond) ||
		origin.MonoAfter > int64(^uint64(0)>>1)-int64(time.Second) {
		return errors.New("invalid fixed onset schedule origin")
	}
	if result.StartLogAfter > origin.MonoBefore || result.StopLogBefore < origin.MonoAfter+int64(time.Second) {
		return errors.New("onset trace does not cover first scheduled second")
	}
	return nil
}

func (d *qualificationLatencyDiagnostic) stamp() int64 {
	if d == nil {
		return 0
	}
	return d.now().Sub(d.epoch).Nanoseconds()
}
func (d *qualificationLatencyDiagnostic) anchor() (qualificationLatencyAnchor, error) {
	a, e := qualificationLatencyMono()
	if e != nil {
		return qualificationLatencyAnchor{}, e
	}
	g := d.stamp()
	b, e := qualificationLatencyMono()
	return qualificationLatencyAnchor{a, g, b}, e
}
func qualificationLatencyMono() (int64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, err
	}
	return ts.Nano(), nil
}

func (d *qualificationLatencyDiagnostic) dispatch(i int, scheduled time.Time) *qualificationLatencyRecord {
	if d == nil {
		return nil
	}
	r := &d.records[i]
	r.Scheduled = scheduled.Sub(d.epoch).Nanoseconds()
	r.Ready = d.stamp()
	return r
}
func (d *qualificationLatencyDiagnostic) worker(r *qualificationLatencyRecord) {
	if d != nil {
		r.Worker = d.stamp()
	}
}
func (d *qualificationLatencyDiagnostic) wrapClientTrace(clientTrace *httptrace.ClientTrace, r *qualificationLatencyRecord) {
	// Called only in the opt-in branch: the default callback retains its original
	// captures and behavior, including connection accounting.
	previous := clientTrace.GotConn
	clientTrace.GotConn = func(info httptrace.GotConnInfo) { d.gotConn(r, info); previous(info) }
}
func (d *qualificationLatencyDiagnostic) gotConn(r *qualificationLatencyRecord, info httptrace.GotConnInfo) {
	if d == nil {
		return
	}
	r.callbackMu.Lock()
	defer r.callbackMu.Unlock()
	if r.callbackClosed {
		d.late.Add(1)
		return
	}
	if r.Callbacks.Add(1) == 1 {
		r.GotConn.Store(d.stamp())
		if info.Reused {
			r.Reused.Store(1)
		}
	}
}
func (d *qualificationLatencyDiagnostic) returned(r *qualificationLatencyRecord) {
	if d != nil {
		r.Headers = d.stamp()
		r.callbackMu.Lock()
		r.callbackClosed = true
		r.callbackMu.Unlock()
	}
}
func (d *qualificationLatencyDiagnostic) complete(r *qualificationLatencyRecord, elapsed time.Duration, status int, outcome uint32) {
	if d == nil {
		return
	}
	r.Elapsed = int64(elapsed)
	r.Complete = r.Scheduled + r.Elapsed
	r.Status = int32(status)
	r.Outcome = outcome
}
func (d *qualificationLatencyDiagnostic) failed(r *qualificationLatencyRecord, started time.Time, outcome uint32) {
	if d == nil {
		return
	}
	d.complete(r, d.now().Sub(started), 0, outcome)
	r.Bookkeeping = d.stamp()
}
func (d *qualificationLatencyDiagnostic) bodyFailed(r *qualificationLatencyRecord, readErr, closeErr error, valid bool) {
	if d == nil {
		return
	}
	r.Outcome = 4
	if readErr != nil {
		r.Outcome |= 1 << 8
	}
	if closeErr != nil {
		r.Outcome |= 1 << 9
	}
	if !valid {
		r.Outcome |= 1 << 10
	}
	r.Bookkeeping = d.stamp()
}
func (d *qualificationLatencyDiagnostic) observe(kind string, start int64, stats qualificationRuntime, ok bool) {
	if d != nil {
		d.observeAt(kind, start, d.stamp(), stats, ok)
	}
}
func (d *qualificationLatencyDiagnostic) observeAt(kind string, start, end int64, stats qualificationRuntime, ok bool) {
	if d == nil {
		return
	}
	observation := qualificationLatencyObservation{Kind: kind, Start: start, End: end, OK: ok, NumGC: stats.NumGC, PauseNS: stats.PauseNS, HeapAlloc: stats.HeapAlloc, TotalAlloc: stats.TotalAlloc}
	if d.observerMode == qualificationObserverSplit {
		observation.observer = &qualificationLatencyObserverObservation{Purpose: qualificationPoll, Source: "none"}
		if kind == "endpoint_read_mem_stats" {
			observation.observer.Purpose = qualificationBoundary
			observation.observer.Source = "runtime.ReadMemStats"
			observation.observer.MemStats = &qualificationLatencyMemStats{stats.NumGC, stats.PauseNS, stats.HeapAlloc, stats.TotalAlloc}
		}
	}
	d.appendObservation(observation)
}
func (d *qualificationLatencyDiagnostic) observeRuntime(start int64, stats qualificationRuntime, ok bool, purpose qualificationRuntimePurpose) {
	if d == nil {
		return
	}
	if d.observerMode == "" {
		d.observe("driver_admin", start, stats, ok)
		return
	}
	observer := &qualificationLatencyObserverObservation{Purpose: purpose, Source: "none"}
	if ok && stats.Observer != nil {
		observer.Source = stats.Observer.Source
		observer.Reading = stats.Observer
		if stats.Observer.MemStatsPresent {
			observer.MemStats = &qualificationLatencyMemStats{stats.NumGC, stats.PauseNS, stats.HeapAlloc, stats.TotalAlloc}
		}
		if stats.Observer.HeapObjects != nil {
			value := *stats.Observer.HeapObjects
			observer.HeapObjects = &value
		}
	}
	d.appendObservation(qualificationLatencyObservation{Kind: "driver_admin", Start: start, End: d.stamp(), OK: ok, observer: observer})
}
func (d *qualificationLatencyDiagnostic) appendObservation(observation qualificationLatencyObservation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sealed {
		d.late.Add(1)
		return
	}
	if len(d.observations) == cap(d.observations) {
		d.overflow = true
		return
	}
	d.observations = append(d.observations, observation)
}
func (d *qualificationLatencyDiagnostic) memStats(start, end int64, s *runtime.MemStats) {
	if d != nil {
		d.observeAt("endpoint_read_mem_stats", start, end, qualificationRuntime{NumGC: s.NumGC, PauseNS: s.PauseTotalNs, HeapAlloc: s.HeapAlloc, TotalAlloc: s.TotalAlloc}, true)
	}
}

// Arm is reachable only on the qualification admin handler, never the public
// listener. Body, method, path, query, address and exact interval are bounded.
func (d *qualificationLatencyDiagnostic) admin(w http.ResponseWriter, r *http.Request) bool {
	if d == nil || r.URL.Path != "/latency-trace-arm" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || err != nil || !net.ParseIP(host).IsLoopback() || d.role != "proxy" {
		http.Error(w, "invalid owned trace arm", 400)
		return true
	}
	var request struct {
		StartNS int64 `json:"start_ns"`
		EndNS   int64 `json:"end_ns"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&request)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("extra trace arm data")
		}
	}
	if err == nil {
		err = d.trace.arm(request.StartNS, request.EndNS)
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"start_ns": request.StartNS, "end_ns": request.EndNS})
	return true
}

// Every component is per-request; adding their quantiles would be invalid.
func qualificationLatencyComponents(r *qualificationLatencyRecord) ([6]int64, error) {
	c := [6]int64{r.Ready - r.Scheduled, r.Worker - r.Ready, r.Do - r.Worker, r.Headers - r.Do, r.Body - r.Headers, r.Complete - r.Body}
	var sum int64
	for _, v := range c {
		if v < 0 {
			return c, errors.New("negative latency component")
		}
		if v > int64(^uint64(0)>>1)-sum {
			return c, errors.New("latency component sum overflow")
		}
		sum += v
	}
	if sum != r.Elapsed || r.Bookkeeping < r.Complete || r.Outcome != 1 || r.Status != 200 || r.Callbacks.Load() != 1 || r.GotConn.Load() < r.Do || r.GotConn.Load() > r.Headers {
		return c, errors.New("incomplete/nonconserving latency record")
	}
	return c, nil
}

func (d *qualificationLatencyDiagnostic) finish() error {
	if d == nil {
		return nil
	}
	d.finishOnce.Do(func() { d.finishErr = d.writeFinal() })
	return d.finishErr
}
func (d *qualificationLatencyDiagnostic) writeFinal() error {
	// Call after sampler/worker join (driver), or admin Shutdown (endpoint). Trace
	// Stop joins runtime's writer internally; our timer goroutine is also joined.
	traceResult := d.trace.close()
	anchor, anchorErr := d.anchor()
	d.mu.Lock()
	d.sealed = true
	observations := d.observations
	overflow := d.overflow
	d.mu.Unlock()
	complete := d.joined && !overflow && d.late.Load() == 0 && anchorErr == nil && traceResult.Complete
	invalid := 0
	if d.role == "load" {
		for i := range d.records {
			if _, err := qualificationLatencyComponents(&d.records[i]); err != nil {
				invalid++
			}
		}
		if d.phase == "onset" {
			firstScheduled := int64(-1)
			if len(d.records) > 0 {
				firstScheduled = d.records[0].Scheduled
			}
			complete = complete && invalid == 0 && qualificationLatencyOnsetCoverage(d.scheduleOrigin, firstScheduled, traceResult) == nil && traceResult.StopAfter <= d.loadEnd
		} else {
			complete = complete && invalid == 0 && traceResult.StartBefore >= d.loadStart && traceResult.StopAfter <= d.loadEnd
		}
	}
	recordsHash := ""
	recordsBytes := int64(0)
	var recordErr error
	if d.role == "load" {
		recordsHash, recordsBytes, recordErr = d.writeRecords()
		if recordErr != nil {
			complete = false
		}
	}
	receipt := struct {
		RecordError        string                     `json:"record_error"`
		Version            int                        `json:"version"`
		Role               string                     `json:"role"`
		Complete           bool                       `json:"complete"`
		Joined             bool                       `json:"joined"`
		InvalidRecords     int                        `json:"invalid_records"`
		Overflow           bool                       `json:"observation_overflow"`
		Late               uint64                     `json:"late_callbacks"`
		Records            int                        `json:"record_count"`
		RecordBytes        int                        `json:"record_size"`
		Bytes              int64                      `json:"file_bytes"`
		SHA                string                     `json:"sha256"`
		Anchor             qualificationLatencyAnchor `json:"anchor"`
		AnchorError        string                     `json:"anchor_error"`
		LoadStart, LoadEnd int64
		Trace              qualificationLatencyTraceResult   `json:"trace"`
		Observations       []qualificationLatencyObservation `json:"observations"`
		Phase              string                            `json:"phase,omitempty"`
		ScheduleOrigin     *qualificationLatencyAnchor       `json:"schedule_origin,omitempty"`
		ObserverMode       string                            `json:"observer_mode,omitempty"`
	}{"", 1, d.role, complete, d.joined, invalid, overflow, d.late.Load(), len(d.records), qualificationLatencyRecordBytes, recordsBytes, recordsHash, anchor, "", d.loadStart, d.loadEnd, traceResult, observations, d.phase, nil, d.observerMode}
	if d.observerMode == qualificationObserverSplit {
		receipt.Version = 2
	}
	if d.phase == "onset" && d.role == "load" {
		receipt.ScheduleOrigin = &d.scheduleOrigin
	}
	if anchorErr != nil {
		receipt.AnchorError = anchorErr.Error()
	}
	if recordErr != nil {
		receipt.RecordError = recordErr.Error()
	}
	f, err := os.OpenFile(filepath.Join(d.dir, "receipt.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(receipt)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !complete {
		return errors.New("latency diagnostic incomplete; raw evidence retained")
	}
	return nil
}

func (d *qualificationLatencyDiagnostic) writeRecords() (string, int64, error) {
	f, err := os.OpenFile(filepath.Join(d.dir, "requests.bin"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	write := func(p []byte) (int, error) {
		n, err := f.Write(p)
		_, _ = h.Write(p[:n])
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		return n, err
	}
	header := make([]byte, 16)
	copy(header, "P2PLAT01")
	binary.LittleEndian.PutUint32(header[8:], uint32(len(d.records)))
	binary.LittleEndian.PutUint32(header[12:], qualificationLatencyRecordBytes)
	n, err := write(header)
	written := int64(n)
	var raw [qualificationLatencyRecordBytes]byte
	for i := range d.records {
		if err != nil {
			break
		}
		r := &d.records[i]
		values := [10]int64{r.Scheduled, r.Ready, r.Worker, r.Do, r.GotConn.Load(), r.Headers, r.Body, r.Complete, r.Bookkeeping, r.Elapsed}
		for j, v := range values {
			binary.LittleEndian.PutUint64(raw[j*8:], uint64(v))
		}
		binary.LittleEndian.PutUint32(raw[80:], r.Callbacks.Load())
		binary.LittleEndian.PutUint32(raw[84:], r.Reused.Load())
		binary.LittleEndian.PutUint32(raw[88:], uint32(r.Status))
		binary.LittleEndian.PutUint32(raw[92:], r.Outcome)
		n, err = write(raw[:])
		written += int64(n)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), written, err
}

type qualificationLatencyTraceResult struct {
	Role, TraceID                                                            string
	Armed, Complete                                                          bool
	PlannedStart, PlannedEnd, StartBefore, StartAfter, StopBefore, StopAfter int64
	StartLogBefore, StartLogAfter, StopLogBefore, StopLogAfter               int64
	Bytes                                                                    int64
	SHA256, Error                                                            string
}

// Completeness only: the full Start/Stop call brackets must fit the fixed
// deadlines plus the prospectively approved tolerance; raw traces survive a
// breach and are never rescheduled or promoted to performance evidence.
func qualificationLatencyTraceIntervals(r qualificationLatencyTraceResult) error {
	tolerance := int64(qualificationLatencyTraceTolerance)
	if r.PlannedStart <= 0 || r.PlannedEnd-r.PlannedStart != int64(2*time.Second) || r.PlannedEnd < r.PlannedStart || r.PlannedEnd > int64(^uint64(0)>>1)-tolerance {
		return errors.New("invalid planned trace interval")
	}
	if r.StartBefore < r.PlannedStart || r.StartAfter < r.StartBefore || r.StartAfter > r.PlannedStart+tolerance {
		return errors.New("trace Start bracket missed 100ms tolerance")
	}
	if r.StopBefore < r.PlannedEnd || r.StopAfter < r.StopBefore || r.StopAfter > r.PlannedEnd+tolerance {
		return errors.New("trace Stop bracket missed 100ms tolerance")
	}
	if r.StartLogBefore < r.StartAfter || r.StartLogAfter < r.StartLogBefore || r.StartLogAfter > r.StopBefore || r.StopLogBefore < r.StopBefore || r.StopLogAfter < r.StopLogBefore || r.StopLogAfter > r.StopAfter {
		return errors.New("trace anchor brackets out of order")
	}
	return nil
}

type qualificationLatencyTrace struct {
	mu         sync.Mutex
	path       string
	role, id   string
	cap        int64
	armed      bool
	cancel     chan struct{}
	done       chan struct{}
	started    chan struct{}
	cancelOnce sync.Once
	result     qualificationLatencyTraceResult
}

func newQualificationLatencyTrace(path string, cap int64) *qualificationLatencyTrace {
	return &qualificationLatencyTrace{path: path, role: "contract", id: "00000000000000000000000000000000", cap: cap, cancel: make(chan struct{}), done: make(chan struct{}), started: make(chan struct{})}
}
func (t *qualificationLatencyTrace) arm(start, end int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now, err := qualificationLatencyMono()
	if err != nil {
		return err
	}
	if t.armed || start <= now || start-now > int64(30*time.Second) || end-start != int64(2*time.Second) || end < start {
		return errors.New("trace already armed or invalid fixed window")
	}
	select {
	case <-t.cancel:
		return errors.New("trace owner already closed")
	default:
	}
	t.armed = true
	t.result = qualificationLatencyTraceResult{Role: t.role, TraceID: t.id, Armed: true, PlannedStart: start, PlannedEnd: end}
	go t.run(start, end)
	return nil
}
func (t *qualificationLatencyTrace) close() qualificationLatencyTraceResult {
	t.cancelOnce.Do(func() { close(t.cancel) })
	t.mu.Lock()
	armed := t.armed
	t.mu.Unlock()
	if armed {
		<-t.done
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.result
}
func (t *qualificationLatencyTrace) run(start, end int64) {
	defer close(t.done)
	result := qualificationLatencyTraceResult{Role: t.role, TraceID: t.id, Armed: true, PlannedStart: start, PlannedEnd: end}
	defer func() { t.mu.Lock(); t.result = result; t.mu.Unlock() }()
	fail := func(err error) {
		if err != nil && result.Error == "" {
			result.Error = err.Error()
		}
	}
	wait := func(deadline int64) bool {
		now, err := qualificationLatencyMono()
		if err != nil {
			fail(err)
			return false
		}
		timer := time.NewTimer(time.Duration(max(int64(0), deadline-now)))
		defer timer.Stop()
		select {
		case <-timer.C:
			return true
		case <-t.cancel:
			fail(errors.New("trace owner canceled before window end"))
			return false
		}
	}
	if !wait(start) {
		return
	}
	f, err := os.OpenFile(t.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fail(err)
		return
	}
	bounded := &qualificationLatencyTraceWriter{file: f, cap: t.cap, hash: sha256.New()}
	result.StartBefore, err = qualificationLatencyMono()
	fail(err)
	if result.StartBefore >= end || err != nil {
		fail(errors.New("missed trace window"))
		_ = f.Close()
		return
	}
	if err = trace.Start(bounded); err != nil {
		fail(err)
		_ = f.Close()
		return
	}
	result.StartAfter, err = qualificationLatencyMono()
	fail(err)
	if result.StartAfter >= end {
		fail(errors.New("trace Start exceeded fixed window"))
	}
	result.StartLogBefore, err = qualificationLatencyMono()
	fail(err)
	trace.Log(context.Background(), "qualification-latency-anchor", fmt.Sprintf("role=%s trace_id=%s seq=1 before_mono_ns=%d", t.role, t.id, result.StartLogBefore))
	result.StartLogAfter, err = qualificationLatencyMono()
	fail(err)
	close(t.started)
	ended := wait(end)
	result.StopBefore, err = qualificationLatencyMono()
	fail(err)
	result.StopLogBefore, err = qualificationLatencyMono()
	fail(err)
	trace.Log(context.Background(), "qualification-latency-anchor", fmt.Sprintf("role=%s trace_id=%s seq=2 before_mono_ns=%d", t.role, t.id, result.StopLogBefore))
	result.StopLogAfter, err = qualificationLatencyMono()
	fail(err)
	trace.Stop()
	result.StopAfter, err = qualificationLatencyMono()
	fail(err)
	fail(f.Close())
	result.Bytes = bounded.n
	result.SHA256 = hex.EncodeToString(bounded.hash.Sum(nil))
	fail(bounded.err)
	fail(qualificationLatencyTraceIntervals(result))
	result.Complete = ended && result.Error == "" && result.Bytes > 0 && result.StartBefore >= start && result.StartAfter < end && result.StopBefore >= end
}

type qualificationLatencyTraceWriter struct {
	file   io.Writer
	cap, n int64
	hash   interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	err error
}

func (w *qualificationLatencyTraceWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	remaining := w.cap - w.n
	data := p
	if int64(len(data)) > remaining {
		data = data[:remaining]
	}
	n, err := w.file.Write(data)
	w.n += int64(n)
	_, _ = w.hash.Write(data[:n])
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil && len(data) != len(p) {
		err = errors.New("trace byte cap exceeded")
	}
	if err != nil {
		w.err = err
	}
	return n, err
}
