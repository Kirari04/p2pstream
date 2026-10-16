//go:build linux

package server

// This file backs scripts/qualify-proxy-performance.sh. It deliberately lives
// in the server package so the finite qualification process can exercise the
// production public listener and routing stack without adding a benchmark API
// to the shipped binary.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type qualificationReady struct {
	URL      string `json:"url"`
	AdminURL string `json:"admin_url,omitempty"`
	PID      int    `json:"pid"`
}

const qualificationBarrierMaxHold = 3 * time.Second

type qualificationRuntime struct {
	HeapAlloc                        uint64            `json:"heap_alloc_bytes"`
	HeapSys                          uint64            `json:"heap_sys_bytes"`
	NumGC                            uint32            `json:"num_gc"`
	PauseNS                          uint64            `json:"pause_total_ns"`
	EventStats                       bool              `json:"event_stats,omitempty"`
	PersistedEvents                  int64             `json:"persisted_events,omitempty"`
	DroppedEvents                    int64             `json:"dropped_events,omitempty"`
	PendingEvents                    int64             `json:"pending_events,omitempty"`
	QueuedEvents                     int               `json:"queued_events,omitempty"`
	EventStatsError                  string            `json:"event_stats_error,omitempty"`
	OwnerStats                       bool              `json:"owner_stats,omitempty"`
	OwnerBytes                       map[string]int64  `json:"owner_bytes,omitempty"`
	OwnerFDs                         map[string]int64  `json:"owner_fds,omitempty"`
	OwnerReservations                map[string]int64  `json:"owner_reservations,omitempty"`
	AdmissionMisses                  map[string]uint64 `json:"admission_misses,omitempty"`
	PublicConnectionLimitRejected    uint64            `json:"public_connection_limit_rejected,omitempty"`
	PublicConnectionResourceRejected uint64            `json:"public_connection_resource_rejected,omitempty"`
	PublicClientRequestRejected      uint64            `json:"public_client_request_rejected,omitempty"`
	AdaptiveAdmissionLimit           int               `json:"adaptive_admission_limit,omitempty"`
	MemoryHeadroomBytes              int64             `json:"memory_headroom_bytes,omitempty"`
	ResourcePressureReason           string            `json:"resource_pressure_reason,omitempty"`
}

type qualificationResult struct {
	Label                         string            `json:"label"`
	Path                          string            `json:"path"`
	Scenario                      string            `json:"scenario"`
	Protocol                      string            `json:"protocol"`
	Concurrency                   int               `json:"concurrency"`
	OfferedRPS                    float64           `json:"offered_requests_per_second,omitempty"`
	WarmupSeconds                 float64           `json:"warmup_seconds,omitempty"`
	WarmupRequests                int               `json:"warmup_requests,omitempty"`
	DurationSeconds               float64           `json:"configured_duration_seconds,omitempty"`
	Attempted                     int               `json:"attempted"`
	UsefulCompleted               int64             `json:"useful_completed"`
	StatusClasses                 map[string]int    `json:"status_classes"`
	StatusCodes                   map[string]int    `json:"status_codes"`
	Errors                        int64             `json:"errors"`
	WallSeconds                   float64           `json:"wall_seconds"`
	UsefulRPS                     float64           `json:"useful_requests_per_second"`
	P50Milliseconds               float64           `json:"p50_ms"`
	P95Milliseconds               float64           `json:"p95_ms"`
	P99Milliseconds               float64           `json:"p99_ms"`
	ResponseBytes                 int64             `json:"response_bytes"`
	PeakRSSBytes                  int64             `json:"peak_rss_bytes,omitempty"`
	FinalRSSBytes                 int64             `json:"final_rss_bytes,omitempty"`
	PeakFDs                       int               `json:"peak_fds,omitempty"`
	CPUSeconds                    float64           `json:"cpu_seconds,omitempty"`
	PeakHeapAlloc                 uint64            `json:"peak_heap_alloc_bytes,omitempty"`
	CgroupMemoryPeak              int64             `json:"cgroup_memory_peak_bytes,omitempty"`
	CgroupMemoryFinal             int64             `json:"cgroup_memory_final_bytes,omitempty"`
	CgroupMemoryMax               int64             `json:"cgroup_memory_max_bytes,omitempty"`
	GCCycles                      uint32            `json:"gc_cycles,omitempty"`
	GCPauseSeconds                float64           `json:"gc_pause_seconds,omitempty"`
	ObservedHTTPMajor             int               `json:"observed_http_major,omitempty"`
	ObservedOriginHTTPMajor       int               `json:"observed_origin_http_major,omitempty"`
	OriginPeakInflight            int64             `json:"origin_peak_inflight,omitempty"`
	InitialRSSBytes               int64             `json:"initial_rss_bytes,omitempty"`
	InitialHeapAlloc              uint64            `json:"initial_heap_alloc_bytes,omitempty"`
	InitialCgroupBytes            int64             `json:"initial_cgroup_memory_bytes,omitempty"`
	PeakSocketFDs                 int               `json:"peak_socket_fds,omitempty"`
	FinalSocketFDs                int               `json:"final_socket_fds,omitempty"`
	PeakCgroupSock                int64             `json:"peak_cgroup_sock_bytes,omitempty"`
	FinalCgroupSock               int64             `json:"final_cgroup_sock_bytes,omitempty"`
	ProxyCPUSet                   string            `json:"proxy_cpu_set,omitempty"`
	LoadCPUSet                    string            `json:"load_cpu_set,omitempty"`
	ResourceEnvelope              string            `json:"resource_envelope,omitempty"`
	ClientConnections             int64             `json:"client_tcp_connections,omitempty"`
	NewOriginConnections          int64             `json:"new_origin_tcp_connections,omitempty"`
	BallastBytes                  int64             `json:"server_ballast_bytes,omitempty"`
	EventStatsCaptured            bool              `json:"event_stats_captured,omitempty"`
	PersistedAfterLoad            int64             `json:"persisted_events_after_load,omitempty"`
	PersistedAfterDrain           int64             `json:"persisted_events_after_drain,omitempty"`
	PersistedEventDelta           int64             `json:"persisted_event_delta,omitempty"`
	DroppedEvents                 int64             `json:"dropped_events,omitempty"`
	DroppedEventDelta             int64             `json:"dropped_event_delta,omitempty"`
	EventQueueAfterLoad           int64             `json:"event_queue_after_load,omitempty"`
	EventQueueAfterDrain          int64             `json:"event_queue_after_drain,omitempty"`
	EventDrainSeconds             float64           `json:"event_drain_seconds,omitempty"`
	EventDrainCPUSeconds          float64           `json:"event_drain_cpu_seconds,omitempty"`
	EventDrainError               string            `json:"event_drain_error,omitempty"`
	EventStatsError               string            `json:"event_stats_error,omitempty"`
	OwnerPeakBytes                map[string]int64  `json:"owner_peak_bytes,omitempty"`
	OwnerPeakFDs                  map[string]int64  `json:"owner_peak_fds,omitempty"`
	OwnerPeakReservations         map[string]int64  `json:"owner_peak_reservations,omitempty"`
	AdmissionRejections           map[string]uint64 `json:"admission_rejections,omitempty"`
	MinimumAdaptiveAdmissionLimit int               `json:"minimum_adaptive_admission_limit,omitempty"`
	MinimumMemoryHeadroomBytes    int64             `json:"minimum_memory_headroom_bytes,omitempty"`
	ResourcePressureReasons       []string          `json:"resource_pressure_reasons,omitempty"`
	BarrierMaxHoldSeconds         float64           `json:"barrier_max_hold_seconds,omitempty"`
}

// TestProxyQualificationProcess is a bounded subprocess entry point. The
// orchestration script supplies a stop file and always removes the process.
func TestProxyQualificationProcess(t *testing.T) {
	role := os.Getenv("P2PSTREAM_QUALIFICATION_ROLE")
	if role == "" {
		t.Skip("qualification subprocess only")
	}
	switch role {
	case "origin":
		runQualificationOrigin(t)
	case "proxy":
		runQualificationProxy(t)
	case "stdlib":
		runQualificationStdlibProxy(t)
	default:
		t.Fatalf("unknown qualification role %q", role)
	}
}

func runQualificationStdlibProxy(t *testing.T) {
	origin, err := url.Parse(requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_ORIGIN_URL"))
	if err != nil {
		t.Fatal(err)
	}
	maxIdle := qualificationEnvInt(t, "P2PSTREAM_QUALIFICATION_STDLIB_MAX_IDLE")
	shards := qualificationOptionalEnvInt("P2PSTREAM_QUALIFICATION_STDLIB_SHARDS")
	if shards == 0 {
		shards = 1
	}
	if shards < 1 || maxIdle < shards || maxIdle%shards != 0 {
		t.Fatalf("invalid standard-library transport shards=%d total idle=%d", shards, maxIdle)
	}
	certPath := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_CERT")
	keyPath := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_KEY")
	proxies := make([]*httputil.ReverseProxy, 0, shards)
	transports := make([]*http.Transport, 0, shards)
	perShardIdle := maxIdle / shards
	bufferPool := newReverseProxyBufferPool()
	for range shards {
		transport := &http.Transport{MaxIdleConns: perShardIdle, MaxIdleConnsPerHost: perShardIdle, MaxConnsPerHost: 0, ForceAttemptHTTP2: false}
		transports = append(transports, transport)
		proxy := httputil.NewSingleHostReverseProxy(origin)
		proxy.Transport = transport
		if os.Getenv("P2PSTREAM_QUALIFICATION_STDLIB_POOL") == "1" {
			proxy.BufferPool = bufferPool
		}
		proxies = append(proxies, proxy)
	}
	defer func() {
		for _, transport := range transports {
			transport.CloseIdleConnections()
		}
	}()
	var next atomic.Uint64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := (next.Add(1) - 1) % uint64(len(proxies))
		proxies[index].ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	configurePublicHTTPServer(srv, 0)
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Server{Handler: qualificationRuntimeHandler(nil)}
	go func() { _ = admin.Serve(adminListener) }()
	writeQualificationReady(t, qualificationReady{URL: "https://" + ln.Addr().String(), AdminURL: "http://" + adminListener.Addr().String(), PID: os.Getpid()})
	go func() { _ = srv.ServeTLS(ln, certPath, keyPath) }()
	waitQualificationStop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	_ = admin.Shutdown(ctx)
}

func qualificationRuntimeHandler(app *App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		result := qualificationRuntime{HeapAlloc: stats.HeapAlloc, HeapSys: stats.HeapSys, NumGC: stats.NumGC, PauseNS: stats.PauseTotalNs}
		if app != nil && r.URL.Query().Get("flush") == "1" {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if err := app.FlushObservabilityRecorder(ctx); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if app != nil && (r.URL.Query().Get("events") == "1" || r.URL.Query().Get("flush") == "1") {
			result.EventStats = true
			if recorder := app.observabilityRecorder; recorder != nil {
				result.DroppedEvents = recorder.droppedEvents.Load()
				result.PendingEvents = recorder.pendingEvents.Load()
				result.QueuedEvents = len(recorder.events)
			}
			ctx, cancel := context.WithTimeout(r.Context(), 250*time.Millisecond)
			defer cancel()
			if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&result.PersistedEvents); err != nil {
				result.EventStats = false
				result.EventStatsError = err.Error()
			}
		}
		if app != nil && r.URL.Query().Get("owners") == "1" {
			snapshot := app.agentStreamCapacity.snapshot()
			result.OwnerStats = true
			result.OwnerBytes = qualificationOwnerValues(snapshot, "AdaptiveExternalBytesByOwner")
			result.OwnerFDs = qualificationOwnerValues(snapshot, "AdaptiveExternalFDsByOwner")
			result.OwnerReservations = qualificationOwnerValues(snapshot, "AdaptiveExternalReservationsByOwner")
			result.AdmissionMisses = snapshot.AdmissionMissesByConstraint
			result.PublicConnectionLimitRejected = app.publicConnectionLimitRejected.Load()
			result.PublicConnectionResourceRejected = app.publicConnectionResourceReject.Load()
			result.PublicClientRequestRejected = app.publicClientRequestRejected.Load()
			result.AdaptiveAdmissionLimit = snapshot.AdaptiveAdmissionLimit
			result.MemoryHeadroomBytes = snapshot.MemoryHeadroomToHardBytes
			result.ResourcePressureReason = snapshot.ResourcePressureReason
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}

func qualificationOwnerValues(snapshot any, fieldName string) map[string]int64 {
	field := reflect.ValueOf(snapshot).FieldByName(fieldName)
	if !field.IsValid() || (field.Kind() != reflect.Array && field.Kind() != reflect.Slice) {
		return nil
	}
	names := [...]string{"other", "public_connection", "public_handshake", "public_request", "public_response", "direct_connection", "replay_buffer", "receive_window"}
	result := make(map[string]int64, min(field.Len(), len(names)))
	for index := 0; index < field.Len() && index < len(names); index++ {
		result[names[index]] = field.Index(index).Int()
	}
	return result
}

func runQualificationOrigin(t *testing.T) {
	originTLS := os.Getenv("P2PSTREAM_QUALIFICATION_ORIGIN_TLS") == "1"
	originHTTP1 := os.Getenv("P2PSTREAM_QUALIFICATION_ORIGIN_HTTP1") == "1"
	data := bytes.Repeat([]byte("p2pstream-qualification-payload-"), 1<<15)
	type barrierState struct{ active, peak atomic.Int64 }
	var barriers sync.Map
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Qualification-Origin-Connection", r.RemoteAddr)
		w.Header().Set("X-Qualification-Origin-HTTP-Major", strconv.Itoa(r.ProtoMajor))
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload"):
			n, err := io.Copy(io.Discard, r.Body)
			if err != nil {
				http.Error(w, "body read failed", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintf(w, "%d", n)
		case strings.HasSuffix(r.URL.Path, "/slow"):
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data)
		case strings.HasSuffix(r.URL.Path, "/delay"):
			token := r.URL.Query().Get("token")
			stateValue, _ := barriers.LoadOrStore(token, &barrierState{})
			state := stateValue.(*barrierState)
			current := state.active.Add(1)
			defer state.active.Add(-1)
			for {
				previous := state.peak.Load()
				if current <= previous || state.peak.CompareAndSwap(previous, current) {
					break
				}
			}
			barrier, _ := strconv.Atoi(r.URL.Query().Get("barrier"))
			deadline := time.Now().Add(qualificationBarrierMaxHold)
			for barrier > 0 && state.peak.Load() < int64(barrier) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
			if ms < 0 || ms > 5000 {
				http.Error(w, "invalid delay", http.StatusBadRequest)
				return
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)
			w.Header().Set("X-Qualification-Peak-Inflight", strconv.FormatInt(state.peak.Load(), 10))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Length", "256")
			_, _ = w.Write(make([]byte, 256))
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	if originTLS && originHTTP1 {
		srv.TLSConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	}
	scheme := "http"
	certPath, keyPath := "", ""
	if originTLS {
		scheme = "https"
		certPath = requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_CERT")
		keyPath = requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_KEY")
	}
	writeQualificationReady(t, qualificationReady{URL: scheme + "://" + ln.Addr().String(), PID: os.Getpid()})
	go func() {
		if originTLS {
			_ = srv.ServeTLS(ln, certPath, keyPath)
			return
		}
		_ = srv.Serve(ln)
	}()
	waitQualificationStop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Error(err)
	}
}

func runQualificationProxy(t *testing.T) {
	originURL := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_ORIGIN_URL")
	certPath := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_CERT")
	keyPath := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_KEY")
	f := newRealAgentProxyFixture(t, 4, 0, 0)
	ballastMiB, _ := strconv.Atoi(os.Getenv("P2PSTREAM_QUALIFICATION_BALLAST_MIB"))
	if ballastMiB < 0 || ballastMiB > 2048 {
		t.Fatalf("invalid ballast MiB %d", ballastMiB)
	}
	ballast := make([]byte, ballastMiB<<20)
	for offset := 0; offset < len(ballast); offset += os.Getpagesize() {
		ballast[offset] = 1
	}
	stopProfiles := startQualificationProfiles(t, os.Getenv("P2PSTREAM_QUALIFICATION_PROFILE_DIR"), os.Getenv("P2PSTREAM_QUALIFICATION_PROFILE_MODE"))
	defer stopProfiles()
	parsed, err := url.Parse(originURL)
	if err != nil {
		t.Fatal(err)
	}
	direct := publicRouteTargetConfig{ID: 801, RouteID: 701, Name: "qualification-direct", Enabled: true, Position: 0, Weight: 100,
		TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportDirect, URL: originURL, ParsedURL: parsed,
		TLSSkipVerify: parsed.Scheme == "https", UpstreamResponseHeaderTimeout: 10 * time.Second}
	agent := direct
	agent.ID, agent.RouteID, agent.Name, agent.Transport = 802, 702, "qualification-agent", publicRouteTargetTransportAgent
	agent.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"qualification": "true"}}
	listener := publicListenerConfig{ID: 1, Name: "qualification", Protocol: publicListenerProtocolHTTPS, BindAddress: "127.0.0.1", Enabled: true}
	snap := &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: listener},
		RoutesByListener: map[int64][]publicRouteConfig{1: {
			{ID: 701, ListenerID: 1, Enabled: true, HostPattern: "public.test", PathPrefix: "/direct", PathSecurityMode: publicRoutePathSecurityModeStrict, Targets: []publicRouteTargetConfig{direct}},
			{ID: 702, ListenerID: 1, Enabled: true, HostPattern: "public.test", PathPrefix: "/agent", PathSecurityMode: publicRoutePathSecurityModeStrict, Targets: []publicRouteTargetConfig{agent}},
		}},
		RouteTargets:    map[int64]publicRouteTargetConfig{801: direct, 802: agent},
		Agents:          map[int64]publicAgentConfig{f.agent.AgentID: {ID: f.agent.AgentID, PublicID: f.agent.PublicID, Enabled: true, Labels: map[string]string{"qualification": "true"}}},
		CertsByListener: map[int64][]publicTLSCertificateConfig{1: {{ID: 1, ListenerID: 1, HostnamePattern: "public.test", CertPath: certPath, KeyPath: keyPath, Enabled: true, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusReady}}},
	}
	setPublicSnapshotForTest(t, f.app, snap)
	f.app.TargetHealth.reconcile(f.app, snap, false)
	if _, err := f.app.startPublicListenerFromSnapshot(listener, snap); err != nil {
		t.Fatal(err)
	}
	f.app.proxyMu.Lock()
	address := f.app.publicListenerState[1].BoundAddress
	f.app.proxyMu.Unlock()
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Server{Handler: qualificationRuntimeHandler(f.app)}
	go func() { _ = admin.Serve(adminListener) }()
	writeQualificationReady(t, qualificationReady{URL: "https://" + address, AdminURL: "http://" + adminListener.Addr().String(), PID: os.Getpid()})
	waitQualificationStop(t)
	runtime.KeepAlive(ballast)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = f.app.stopPublicListenerRuntime(ctx, 1)
	_ = admin.Shutdown(ctx)
}

func startQualificationProfiles(t *testing.T, dir, mode string) func() {
	t.Helper()
	if dir == "" {
		return func() {}
	}
	if mode != "cpu" && mode != "contention" {
		t.Fatalf("invalid qualification profile mode %q", mode)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var cpu *os.File
	if mode == "cpu" {
		var err error
		cpu, err = os.Create(filepath.Join(dir, "cpu.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(cpu); err != nil {
			_ = cpu.Close()
			t.Fatal(err)
		}
	}
	previousMutex := 0
	if mode == "contention" {
		previousMutex = runtime.SetMutexProfileFraction(100)
		runtime.SetBlockProfileRate(1_000_000)
	}
	return func() {
		if cpu != nil {
			pprof.StopCPUProfile()
			_ = cpu.Close()
		}
		runtime.GC()
		names := []string{"heap", "allocs"}
		if mode == "contention" {
			names = append(names, "mutex", "block")
		}
		for _, name := range names {
			file, createErr := os.Create(filepath.Join(dir, name+".pprof"))
			if createErr != nil {
				t.Errorf("create %s profile: %v", name, createErr)
				continue
			}
			profile := pprof.Lookup(name)
			if profile == nil {
				t.Errorf("profile %s unavailable", name)
			} else if writeErr := profile.WriteTo(file, 0); writeErr != nil {
				t.Errorf("write %s profile: %v", name, writeErr)
			}
			_ = file.Close()
		}
		if mode == "contention" {
			runtime.SetMutexProfileFraction(previousMutex)
			runtime.SetBlockProfileRate(0)
		}
	}
}

func TestProxyQualificationLoad(t *testing.T) {
	if os.Getenv("P2PSTREAM_QUALIFICATION_ROLE") != "load" {
		t.Skip("qualification load subprocess only")
	}
	target := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_URL")
	scenario := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_SCENARIO")
	concurrency := qualificationEnvInt(t, "P2PSTREAM_QUALIFICATION_CONCURRENCY")
	requests := qualificationEnvInt(t, "P2PSTREAM_QUALIFICATION_REQUESTS")
	offeredRPS, _ := strconv.ParseFloat(os.Getenv("P2PSTREAM_QUALIFICATION_OFFERED_RPS"), 64)
	durationSeconds, _ := strconv.ParseFloat(os.Getenv("P2PSTREAM_QUALIFICATION_DURATION_SECONDS"), 64)
	warmupSeconds, _ := strconv.ParseFloat(os.Getenv("P2PSTREAM_QUALIFICATION_WARMUP_SECONDS"), 64)
	protocol := os.Getenv("P2PSTREAM_QUALIFICATION_PROTOCOL")
	if concurrency < 1 || requests < 1 || concurrency > requests {
		t.Fatalf("invalid concurrency=%d requests=%d", concurrency, requests)
	}
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, // fixture certificate
		MaxIdleConns:        concurrency,
		MaxIdleConnsPerHost: concurrency,
		MaxConnsPerHost:     concurrency,
		ForceAttemptHTTP2:   protocol == "h2",
		DisableKeepAlives:   strings.HasPrefix(scenario, "h1-churn"),
	}
	if protocol == "h1" {
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	if scenario == "tls-idle" {
		runQualificationTLSIdle(t, target, concurrency, requests)
		return
	}
	var useful, errorsSeen, bytesSeen atomic.Int64
	var observedMajor atomic.Int64
	var observedOriginMajor atomic.Int64
	var originPeak atomic.Int64
	var clientConnections atomic.Int64
	var newOriginConnections atomic.Int64
	var seenConnections sync.Map
	var seenOriginConnections sync.Map
	classes := make(map[string]int)
	statusCodes := make(map[string]int)
	latencies := make([]time.Duration, 0, requests)
	var resultMu sync.Mutex
	var inflight atomic.Int64
	type loadJob struct{ scheduled time.Time }
	jobs := make(chan loadJob)
	startGate := make(chan struct{})
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-startGate
			for job := range jobs {
				started := job.scheduled
				if started.IsZero() {
					started = time.Now()
				}
				method := http.MethodGet
				var body io.Reader
				requestURL := target
				if strings.HasPrefix(scenario, "upload") {
					method = http.MethodPost
					body = bytes.NewReader(make([]byte, 64<<10))
				} else if strings.HasPrefix(scenario, "capacity-") {
					token := os.Getenv("P2PSTREAM_QUALIFICATION_SEQUENCE")
					requestURL += "?barrier=" + strconv.Itoa(concurrency) + "&token=" + url.QueryEscape(token)
				}
				req, err := http.NewRequest(method, requestURL, body)
				if err != nil {
					errorsSeen.Add(1)
					inflight.Add(-1)
					continue
				}
				req.Host = "public.test"
				req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
					if strings.HasPrefix(scenario, "h1-churn") {
						if !info.Reused {
							clientConnections.Add(1)
						}
						return
					}
					if _, loaded := seenConnections.LoadOrStore(info.Conn, struct{}{}); !loaded {
						clientConnections.Add(1)
					}
				}}))
				resp, err := client.Do(req)
				if err != nil {
					errorsSeen.Add(1)
					inflight.Add(-1)
					continue
				}
				observedMajor.CompareAndSwap(0, int64(resp.ProtoMajor))
				if originMajor, parseErr := strconv.ParseInt(resp.Header.Get("X-Qualification-Origin-HTTP-Major"), 10, 64); parseErr == nil {
					observedOriginMajor.CompareAndSwap(0, originMajor)
				}
				if originConnection := resp.Header.Get("X-Qualification-Origin-Connection"); originConnection != "" {
					if _, loaded := seenOriginConnections.LoadOrStore(originConnection, struct{}{}); !loaded {
						newOriginConnections.Add(1)
					}
				}
				if value, parseErr := strconv.ParseInt(resp.Header.Get("X-Qualification-Peak-Inflight"), 10, 64); parseErr == nil {
					for {
						previous := originPeak.Load()
						if value <= previous || originPeak.CompareAndSwap(previous, value) {
							break
						}
					}
				}
				var n int64
				validPayload := true
				if scenario == "slow-reader" {
					buf := make([]byte, 8<<10)
					hash := sha256.New()
					for {
						read, readErr := resp.Body.Read(buf)
						n += int64(read)
						_, _ = hash.Write(buf[:read])
						if read > 0 {
							time.Sleep(2 * time.Millisecond)
						}
						if readErr == io.EOF {
							break
						}
						if readErr != nil {
							err = readErr
							break
						}
					}
					want := sha256.Sum256(bytes.Repeat([]byte("p2pstream-qualification-payload-"), 1<<15))
					validPayload = n == 1<<20 && bytes.Equal(hash.Sum(nil), want[:])
				} else {
					var responseBody []byte
					responseBody, err = io.ReadAll(resp.Body)
					n = int64(len(responseBody))
					switch scenario {
					case "upload", "upload-duration":
						validPayload = string(responseBody) == "65536"
					case "capacity-1536-h1", "capacity-1536-h2":
						validPayload = len(responseBody) == 0
					default:
						if strings.HasPrefix(scenario, "capacity-") {
							validPayload = len(responseBody) == 0
						} else {
							validPayload = len(responseBody) == 256 && bytes.Equal(responseBody, make([]byte, 256))
						}
					}
				}
				closeErr := resp.Body.Close()
				elapsed := time.Since(started)
				class := fmt.Sprintf("%dxx", resp.StatusCode/100)
				resultMu.Lock()
				classes[class]++
				statusCodes[strconv.Itoa(resp.StatusCode)]++
				resultMu.Unlock()
				if err != nil || closeErr != nil || !validPayload {
					errorsSeen.Add(1)
					inflight.Add(-1)
					continue
				}
				resultMu.Lock()
				latencies = append(latencies, elapsed)
				resultMu.Unlock()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					useful.Add(1)
					bytesSeen.Add(n)
				}
				inflight.Add(-1)
			}
		}()
	}
	close(startGate)
	dispatchFor := func(duration time.Duration) int {
		count := 0
		deadline := time.Now().Add(duration)
		for time.Now().Before(deadline) {
			inflight.Add(1)
			jobs <- loadJob{}
			count++
		}
		for inflight.Load() != 0 {
			time.Sleep(time.Millisecond)
		}
		return count
	}
	warmupRequests := 0
	flushQualificationEvents(t, os.Getenv("P2PSTREAM_QUALIFICATION_ADMIN_URL"))
	if durationSeconds > 0 && warmupSeconds > 0 {
		warmupRequests = dispatchFor(time.Duration(warmupSeconds * float64(time.Second)))
		flushQualificationEvents(t, os.Getenv("P2PSTREAM_QUALIFICATION_ADMIN_URL"))
		useful.Store(0)
		errorsSeen.Store(0)
		bytesSeen.Store(0)
		observedMajor.Store(0)
		observedOriginMajor.Store(0)
		originPeak.Store(0)
		clientConnections.Store(0)
		newOriginConnections.Store(0)
		seenConnections.Range(func(key, _ any) bool { seenConnections.Delete(key); return true })
		resultMu.Lock()
		clear(classes)
		clear(statusCodes)
		latencies = latencies[:0]
		resultMu.Unlock()
	}
	sampler := newQualificationSampler(os.Getenv("P2PSTREAM_QUALIFICATION_PIDS"), os.Getenv("P2PSTREAM_QUALIFICATION_ADMIN_URL"), strings.HasPrefix(scenario, "capacity-"))
	sampler.start()
	started := time.Now()
	attempted := 0
	if durationSeconds > 0 {
		attempted = dispatchFor(time.Duration(durationSeconds * float64(time.Second)))
	} else {
		for i := 0; i < requests; i++ {
			var scheduled time.Time
			if offeredRPS > 0 {
				scheduled = started.Add(time.Duration(float64(time.Second) * float64(i) / offeredRPS))
				if delay := time.Until(scheduled); delay > 0 {
					time.Sleep(delay)
				}
			}
			inflight.Add(1)
			jobs <- loadJob{scheduled: scheduled}
			attempted++
		}
	}
	close(jobs)
	workers.Wait()
	elapsed := time.Since(started)
	sampler.stop()
	slices.Sort(latencies)
	result := qualificationResult{
		Label: os.Getenv("P2PSTREAM_QUALIFICATION_LABEL"), Path: os.Getenv("P2PSTREAM_QUALIFICATION_PATH"), Scenario: scenario,
		OfferedRPS: offeredRPS, WarmupSeconds: warmupSeconds, WarmupRequests: warmupRequests, DurationSeconds: durationSeconds,
		Protocol: protocol, Concurrency: concurrency, Attempted: attempted, UsefulCompleted: useful.Load(), StatusClasses: classes,
		StatusCodes: statusCodes,
		Errors:      errorsSeen.Load(), WallSeconds: elapsed.Seconds(), UsefulRPS: float64(useful.Load()) / elapsed.Seconds(),
		P50Milliseconds: durationQuantile(latencies, .50), P95Milliseconds: durationQuantile(latencies, .95), P99Milliseconds: durationQuantile(latencies, .99),
		ResponseBytes: bytesSeen.Load(), PeakRSSBytes: sampler.peakRSS, FinalRSSBytes: sampler.finalRSS, PeakFDs: sampler.peakFDs, CPUSeconds: sampler.cpuSeconds(),
		PeakHeapAlloc: sampler.peakHeap, GCCycles: sampler.gcCycles(), GCPauseSeconds: sampler.gcPauseSeconds(), ObservedHTTPMajor: int(observedMajor.Load()), ObservedOriginHTTPMajor: int(observedOriginMajor.Load()),
		CgroupMemoryPeak: sampler.cgroupPeak, CgroupMemoryFinal: sampler.cgroupFinal, CgroupMemoryMax: sampler.cgroupMax,
		OriginPeakInflight: originPeak.Load(), InitialRSSBytes: sampler.initialRSS, InitialHeapAlloc: sampler.initialHeap,
		InitialCgroupBytes: sampler.initialCgroup, PeakSocketFDs: sampler.peakSocketFDs, FinalSocketFDs: sampler.finalSocketFDs,
		PeakCgroupSock: sampler.peakCgroupSock, FinalCgroupSock: sampler.finalCgroupSock,
		ProxyCPUSet: os.Getenv("P2PSTREAM_QUALIFICATION_PROXY_CPU_SET"), LoadCPUSet: os.Getenv("P2PSTREAM_QUALIFICATION_LOAD_CPU_SET"), ResourceEnvelope: os.Getenv("P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE"),
		ClientConnections: clientConnections.Load(), NewOriginConnections: newOriginConnections.Load(), BallastBytes: int64(qualificationOptionalEnvInt("P2PSTREAM_QUALIFICATION_BALLAST_MIB")) << 20,
		OwnerPeakBytes: sampler.ownerPeakBytes, OwnerPeakFDs: sampler.ownerPeakFDs, OwnerPeakReservations: sampler.ownerPeakReservations,
		AdmissionRejections: sampler.admissionRejectionDelta(), MinimumAdaptiveAdmissionLimit: sampler.minimumAdaptiveAdmissionLimit,
		MinimumMemoryHeadroomBytes: sampler.minimumMemoryHeadroomBytes, ResourcePressureReasons: sampler.resourcePressureReasons,
	}
	if strings.HasPrefix(scenario, "capacity-") {
		result.BarrierMaxHoldSeconds = qualificationBarrierMaxHold.Seconds()
	}
	if protocol == "h2" && result.ObservedHTTPMajor != 2 {
		t.Fatalf("negotiated HTTP/%d, want HTTP/2", result.ObservedHTTPMajor)
	}
	applyQualificationEventStats(&result, sampler)
	if err := writeQualificationJSON(requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_RESULT"), result); err != nil {
		t.Fatal(err)
	}
}

func runQualificationTLSIdle(t *testing.T, target string, concurrency, attempts int) {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	sampler := newQualificationSampler(os.Getenv("P2PSTREAM_QUALIFICATION_PIDS"), os.Getenv("P2PSTREAM_QUALIFICATION_ADMIN_URL"), false)
	sampler.start()
	started := time.Now()
	sem := make(chan struct{}, concurrency)
	connections := make(chan *tls.Conn, attempts)
	latencies := make(chan time.Duration, attempts)
	var failures atomic.Int64
	var wg sync.WaitGroup
	for range attempts {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			begin := time.Now()
			conn, dialErr := tls.Dial("tcp", parsed.Host, &tls.Config{InsecureSkipVerify: true, ServerName: "public.test", NextProtos: []string{"http/1.1"}})
			if dialErr != nil {
				failures.Add(1)
				return
			}
			connections <- conn
			latencies <- time.Since(begin)
		}()
	}
	wg.Wait()
	close(connections)
	close(latencies)
	time.Sleep(500 * time.Millisecond)
	for conn := range connections {
		_ = conn.Close()
	}
	time.Sleep(500 * time.Millisecond)
	sampler.stop()
	values := make([]time.Duration, 0, attempts)
	for latency := range latencies {
		values = append(values, latency)
	}
	slices.Sort(values)
	useful := int64(len(values))
	wall := time.Since(started).Seconds()
	result := qualificationResult{
		Label: os.Getenv("P2PSTREAM_QUALIFICATION_LABEL"), Path: os.Getenv("P2PSTREAM_QUALIFICATION_PATH"), Scenario: "tls-idle", Protocol: "tls",
		Concurrency: concurrency, Attempted: attempts, UsefulCompleted: useful, StatusClasses: map[string]int{"tls": int(useful)}, Errors: failures.Load(),
		WallSeconds: wall, UsefulRPS: float64(useful) / wall, P50Milliseconds: durationQuantile(values, .50), P95Milliseconds: durationQuantile(values, .95), P99Milliseconds: durationQuantile(values, .99),
		PeakRSSBytes: sampler.peakRSS, FinalRSSBytes: sampler.finalRSS, PeakFDs: sampler.peakFDs, CPUSeconds: sampler.cpuSeconds(), PeakHeapAlloc: sampler.peakHeap,
		CgroupMemoryPeak: sampler.cgroupPeak, CgroupMemoryFinal: sampler.cgroupFinal, CgroupMemoryMax: sampler.cgroupMax, GCCycles: sampler.gcCycles(), GCPauseSeconds: sampler.gcPauseSeconds(),
		InitialRSSBytes: sampler.initialRSS, InitialHeapAlloc: sampler.initialHeap, InitialCgroupBytes: sampler.initialCgroup,
		PeakSocketFDs: sampler.peakSocketFDs, FinalSocketFDs: sampler.finalSocketFDs, PeakCgroupSock: sampler.peakCgroupSock, FinalCgroupSock: sampler.finalCgroupSock,
		ProxyCPUSet: os.Getenv("P2PSTREAM_QUALIFICATION_PROXY_CPU_SET"), LoadCPUSet: os.Getenv("P2PSTREAM_QUALIFICATION_LOAD_CPU_SET"), ResourceEnvelope: os.Getenv("P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE"),
	}
	applyQualificationEventStats(&result, sampler)
	if err := writeQualificationJSON(requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_RESULT"), result); err != nil {
		t.Fatal(err)
	}
}

func applyQualificationEventStats(result *qualificationResult, sampler *qualificationSampler) {
	if !strings.HasPrefix(result.ResourceEnvelope, "p2pstream") {
		return
	}
	if !sampler.initialEvents.EventStats || !sampler.afterLoadEvents.EventStats || !sampler.afterDrainEvents.EventStats {
		for _, stats := range []qualificationRuntime{sampler.initialEvents, sampler.afterLoadEvents, sampler.afterDrainEvents} {
			if stats.EventStatsError != "" {
				result.EventStatsError = stats.EventStatsError
				break
			}
		}
		if result.EventStatsError == "" && sampler.adminURL != "" {
			result.EventStatsError = "event statistics unavailable"
		}
		result.EventDrainError = sampler.eventDrainError
		return
	}
	result.EventStatsCaptured = true
	result.PersistedAfterLoad = sampler.afterLoadEvents.PersistedEvents
	result.PersistedAfterDrain = sampler.afterDrainEvents.PersistedEvents
	result.PersistedEventDelta = sampler.afterDrainEvents.PersistedEvents - sampler.initialEvents.PersistedEvents
	result.DroppedEvents = sampler.afterDrainEvents.DroppedEvents
	result.DroppedEventDelta = sampler.afterDrainEvents.DroppedEvents - sampler.initialEvents.DroppedEvents
	result.EventQueueAfterLoad = sampler.afterLoadEvents.PendingEvents + int64(sampler.afterLoadEvents.QueuedEvents)
	result.EventQueueAfterDrain = sampler.afterDrainEvents.PendingEvents + int64(sampler.afterDrainEvents.QueuedEvents)
	result.EventDrainSeconds = sampler.eventDrainSeconds
	result.EventDrainCPUSeconds = sampler.eventDrainCPUSeconds
	result.EventDrainError = sampler.eventDrainError
}

func flushQualificationEvents(t *testing.T, adminURL string) {
	t.Helper()
	if adminURL == "" {
		return
	}
	client := http.Client{Timeout: 35 * time.Second}
	resp, err := client.Get(adminURL + "?flush=1")
	if err != nil {
		t.Fatalf("flush observability recorder: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Fatalf("flush observability recorder: status=%d body=%q", resp.StatusCode, message)
	}
}

type qualificationSampler struct {
	pids                          []int
	adminURL                      string
	done                          chan struct{}
	wg                            sync.WaitGroup
	initialRSS                    int64
	peakRSS                       int64
	finalRSS                      int64
	peakFDs                       int
	peakSocketFDs                 int
	finalSocketFDs                int
	initialHeap                   uint64
	peakHeap                      uint64
	startCPU, endCPU              uint64
	startGC, endGC                uint32
	startPause, endPause          uint64
	cgroupPeak                    int64
	cgroupFinal                   int64
	cgroupMax                     int64
	initialCgroup                 int64
	peakCgroupSock                int64
	finalCgroupSock               int64
	initialEvents                 qualificationRuntime
	afterLoadEvents               qualificationRuntime
	afterDrainEvents              qualificationRuntime
	eventDrainSeconds             float64
	eventDrainCPUSeconds          float64
	eventDrainError               string
	ownerSamples                  bool
	ownerPeakBytes                map[string]int64
	ownerPeakFDs                  map[string]int64
	ownerPeakReservations         map[string]int64
	initialOwnerStats             qualificationRuntime
	finalOwnerStats               qualificationRuntime
	minimumAdaptiveAdmissionLimit int
	minimumMemoryHeadroomBytes    int64
	resourcePressureReasons       []string
	resourcePressureSeen          map[string]struct{}
	ownerStatsObserved            bool
}

func newQualificationSampler(rawPIDs, adminURL string, ownerSamples bool) *qualificationSampler {
	s := &qualificationSampler{
		adminURL: adminURL, done: make(chan struct{}), ownerSamples: ownerSamples,
		ownerPeakBytes: make(map[string]int64), ownerPeakFDs: make(map[string]int64), ownerPeakReservations: make(map[string]int64),
		resourcePressureSeen: make(map[string]struct{}),
	}
	for _, raw := range strings.Split(rawPIDs, ",") {
		if pid, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && pid > 0 {
			s.pids = append(s.pids, pid)
		}
	}
	return s
}

func (s *qualificationSampler) start() {
	s.startCPU = s.readCPU()
	s.cgroupMax = s.readCgroupValue("memory.max")
	s.initialRSS = s.readRSS()
	s.initialCgroup = s.readCgroupValue("memory.current")
	if stats, ok := s.readRuntime(true); ok {
		s.startGC, s.startPause = stats.NumGC, stats.PauseNS
		s.initialHeap = stats.HeapAlloc
		s.initialEvents = stats
		s.initialOwnerStats = stats
		s.observeOwnerStats(stats)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			s.sample()
			select {
			case <-ticker.C:
			case <-s.done:
				return
			}
		}
	}()
}

func (s *qualificationSampler) stop() {
	close(s.done)
	s.wg.Wait()
	s.sample()
	s.finalRSS = s.readRSS()
	s.cgroupFinal = s.readCgroupValue("memory.current")
	s.finalCgroupSock = s.readCgroupStat("sock")
	_, s.finalSocketFDs = s.readFDs()
	s.endCPU = s.readCPU()
	if stats, ok := s.readRuntime(true); ok {
		s.endGC, s.endPause = stats.NumGC, stats.PauseNS
		s.afterLoadEvents = stats
		s.finalOwnerStats = stats
		s.observeOwnerStats(stats)
	}
	s.flushEventRecorder()
}

func (s *qualificationSampler) sample() {
	rss := s.readRSS()
	fds, socketFDs := s.readFDs()
	if rss > s.peakRSS {
		s.peakRSS = rss
	}
	if fds > s.peakFDs {
		s.peakFDs = fds
	}
	if socketFDs > s.peakSocketFDs {
		s.peakSocketFDs = socketFDs
	}
	if stats, ok := s.readRuntime(false); ok {
		if stats.HeapAlloc > s.peakHeap {
			s.peakHeap = stats.HeapAlloc
		}
		s.observeOwnerStats(stats)
	}
	if current := s.readCgroupValue("memory.current"); current > s.cgroupPeak {
		s.cgroupPeak = current
	}
	if sock := s.readCgroupStat("sock"); sock > s.peakCgroupSock {
		s.peakCgroupSock = sock
	}
}

func (s *qualificationSampler) readFDs() (total, sockets int) {
	for _, pid := range s.pids {
		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			continue
		}
		total += len(entries)
		for _, entry := range entries {
			target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, entry.Name()))
			if err == nil && strings.HasPrefix(target, "socket:[") {
				sockets++
			}
		}
	}
	return total, sockets
}

func (s *qualificationSampler) readCgroupValue(name string) int64 {
	if len(s.pids) == 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/root/sys/fs/cgroup/%s", s.pids[0], name))
	if err != nil {
		return 0
	}
	value := strings.TrimSpace(string(data))
	if value == "max" {
		return -1
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func (s *qualificationSampler) readCgroupStat(name string) int64 {
	if len(s.pids) == 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/root/sys/fs/cgroup/memory.stat", s.pids[0]))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == name {
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			return value
		}
	}
	return 0
}

func (s *qualificationSampler) readRSS() int64 {
	var rss int64
	for _, pid := range s.pids {
		if fields, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
			parts := strings.Fields(string(fields))
			if len(parts) > 1 {
				pages, _ := strconv.ParseInt(parts[1], 10, 64)
				rss += pages * int64(os.Getpagesize())
			}
		}
	}
	return rss
}

func (s *qualificationSampler) readCPU() uint64 {
	var total uint64
	for _, pid := range s.pids {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(data))
		if len(fields) > 14 {
			u, _ := strconv.ParseUint(fields[13], 10, 64)
			v, _ := strconv.ParseUint(fields[14], 10, 64)
			total += u + v
		}
	}
	return total
}

func (s *qualificationSampler) readRuntime(eventStats bool) (qualificationRuntime, bool) {
	if s.adminURL == "" {
		return qualificationRuntime{}, false
	}
	client := http.Client{Timeout: 100 * time.Millisecond}
	endpoint := s.adminURL
	if eventStats {
		endpoint += "?events=1"
	}
	if s.ownerSamples {
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		endpoint += separator + "owners=1"
	}
	resp, err := client.Get(endpoint)
	if err != nil {
		return qualificationRuntime{}, false
	}
	defer resp.Body.Close()
	var stats qualificationRuntime
	err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&stats)
	return stats, err == nil
}

func (s *qualificationSampler) observeOwnerStats(stats qualificationRuntime) {
	if !stats.OwnerStats {
		return
	}
	for name, value := range stats.OwnerBytes {
		if value > s.ownerPeakBytes[name] {
			s.ownerPeakBytes[name] = value
		}
	}
	for name, value := range stats.OwnerFDs {
		if value > s.ownerPeakFDs[name] {
			s.ownerPeakFDs[name] = value
		}
	}
	for name, value := range stats.OwnerReservations {
		if value > s.ownerPeakReservations[name] {
			s.ownerPeakReservations[name] = value
		}
	}
	if !s.ownerStatsObserved || stats.AdaptiveAdmissionLimit < s.minimumAdaptiveAdmissionLimit {
		s.minimumAdaptiveAdmissionLimit = stats.AdaptiveAdmissionLimit
	}
	if !s.ownerStatsObserved || stats.MemoryHeadroomBytes < s.minimumMemoryHeadroomBytes {
		s.minimumMemoryHeadroomBytes = stats.MemoryHeadroomBytes
	}
	s.ownerStatsObserved = true
	if stats.ResourcePressureReason != "" {
		if _, seen := s.resourcePressureSeen[stats.ResourcePressureReason]; !seen {
			s.resourcePressureSeen[stats.ResourcePressureReason] = struct{}{}
			s.resourcePressureReasons = append(s.resourcePressureReasons, stats.ResourcePressureReason)
			slices.Sort(s.resourcePressureReasons)
		}
	}
}

func (s *qualificationSampler) admissionRejectionDelta() map[string]uint64 {
	if !s.ownerStatsObserved {
		return nil
	}
	result := map[string]uint64{
		"public_connection_limit":    counterDelta(s.initialOwnerStats.PublicConnectionLimitRejected, s.finalOwnerStats.PublicConnectionLimitRejected),
		"public_connection_resource": counterDelta(s.initialOwnerStats.PublicConnectionResourceRejected, s.finalOwnerStats.PublicConnectionResourceRejected),
		"public_client_request":      counterDelta(s.initialOwnerStats.PublicClientRequestRejected, s.finalOwnerStats.PublicClientRequestRejected),
	}
	for constraint, final := range s.finalOwnerStats.AdmissionMisses {
		result["agent_stream:"+constraint] = counterDelta(s.initialOwnerStats.AdmissionMisses[constraint], final)
	}
	return result
}

func counterDelta(initial, final uint64) uint64 {
	if final < initial {
		return 0
	}
	return final - initial
}

func (s *qualificationSampler) flushEventRecorder() {
	if s.adminURL == "" {
		return
	}
	started := time.Now()
	cpuStarted := s.readCPU()
	client := http.Client{Timeout: 35 * time.Second}
	resp, err := client.Get(s.adminURL + "?flush=1")
	if err != nil {
		s.eventDrainError = err.Error()
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			s.eventDrainError = resp.Status
		} else if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&s.afterDrainEvents); err != nil {
			s.eventDrainError = err.Error()
		}
	}
	if s.eventDrainError != "" {
		s.afterDrainEvents = s.afterLoadEvents
	}
	cpuEnded := s.readCPU()
	if cpuEnded >= cpuStarted {
		s.eventDrainCPUSeconds = float64(cpuEnded-cpuStarted) / 100
	}
	s.eventDrainSeconds = time.Since(started).Seconds()
}

func (s *qualificationSampler) cpuSeconds() float64 {
	if s.endCPU < s.startCPU {
		return 0
	}
	return float64(s.endCPU-s.startCPU) / 100 // Linux USER_HZ on supported qualification hosts.
}
func (s *qualificationSampler) gcCycles() uint32 {
	if s.endGC < s.startGC {
		return 0
	}
	return s.endGC - s.startGC
}
func (s *qualificationSampler) gcPauseSeconds() float64 {
	if s.endPause < s.startPause {
		return 0
	}
	return float64(s.endPause-s.startPause) / 1e9
}

func durationQuantile(values []time.Duration, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1)*q + .5)
	return float64(values[index]) / float64(time.Millisecond)
}

func writeQualificationReady(t *testing.T, ready qualificationReady) {
	t.Helper()
	if err := writeQualificationJSON(requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_READY"), ready); err != nil {
		t.Fatal(err)
	}
}

func writeQualificationJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func waitQualificationStop(t *testing.T) {
	t.Helper()
	stop := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_STOP")
	for {
		if _, err := os.Stat(stop); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func requiredQualificationEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func qualificationEnvInt(t *testing.T, key string) int {
	t.Helper()
	value, err := strconv.Atoi(requiredQualificationEnv(t, key))
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return value
}

func qualificationOptionalEnvInt(key string) int {
	value, _ := strconv.Atoi(os.Getenv(key))
	return value
}

// Keep filepath linked on baseline builds whose fixture implementation uses it
// behind build-specific files.
var _ = filepath.Separator
