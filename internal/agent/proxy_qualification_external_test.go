//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

const externalQualificationBallastChunkBytes = 64 << 20

type externalQualificationBallast struct {
	mu         sync.RWMutex
	chunks     [][]byte
	bytes      int64
	chunkBytes int64
}

func newExternalQualificationBallast(chunkBytes int64) *externalQualificationBallast {
	return &externalQualificationBallast{chunkBytes: chunkBytes}
}

func (b *externalQualificationBallast) setBytes(target int64) {
	b.mu.Lock()
	shrinking := target < b.bytes
	if shrinking {
		for b.bytes > target {
			last := len(b.chunks) - 1
			chunk := b.chunks[last]
			without := b.bytes - int64(len(chunk))
			if without >= target {
				b.chunks[last] = nil
				b.chunks = b.chunks[:last]
				b.bytes = without
				continue
			}
			keep := target - without
			replacement := make([]byte, keep)
			for offset := 0; offset < len(replacement); offset += os.Getpagesize() {
				replacement[offset] = 1
			}
			b.chunks[last] = replacement
			b.bytes = target
		}
	}
	for b.bytes < target {
		nextBytes := min(b.chunkBytes, target-b.bytes)
		next := make([]byte, nextBytes)
		for offset := 0; offset < len(next); offset += os.Getpagesize() {
			next[offset] = 1
		}
		b.chunks = append(b.chunks, next)
		b.bytes += int64(len(next))
	}
	b.mu.Unlock()
	if shrinking {
		runtime.GC()
		debug.FreeOSMemory()
	}
}

func (b *externalQualificationBallast) size() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.bytes
}

func (b *externalQualificationBallast) keepAlive() {
	b.mu.RLock()
	defer b.mu.RUnlock()
	runtime.KeepAlive(b.chunks)
}

func TestExternalQualificationBallastGrowsAndShrinksInChunks(t *testing.T) {
	ballast := newExternalQualificationBallast(64 << 10)
	for _, size := range []int64{0, 64 << 10, 160 << 10, 96 << 10, 0} {
		ballast.setBytes(size)
		if got := ballast.size(); got != size {
			t.Fatalf("ballast size = %d, want %d", got, size)
		}
	}
}

type externalQualificationAgentReady struct {
	AdminURL string `json:"admin_url"`
	PID      int    `json:"pid"`
}

type externalQualificationAgentCapacity struct {
	Adaptive                      bool   `json:"adaptive"`
	ResourceLimited               bool   `json:"resource_limited"`
	Maximum                       int    `json:"maximum"`
	AdmissionLimit                int    `json:"admission_limit"`
	OrdinaryAdmissionLimit        int    `json:"ordinary_admission_limit"`
	InUse                         int    `json:"in_use"`
	ControlInUse                  int    `json:"control_in_use"`
	ControlReservedSlots          int    `json:"control_reserved_slots"`
	ControlReservedBaseBytes      int64  `json:"control_reserved_base_bytes"`
	ControlReservedBaseFDs        int64  `json:"control_reserved_base_fds"`
	Pressure                      string `json:"pressure"`
	MemoryUsedBytes               int64  `json:"memory_used_bytes"`
	MemoryLimitBytes              int64  `json:"memory_limit_bytes"`
	MemorySource                  string `json:"memory_source"`
	FileDescriptorsUsed           int64  `json:"file_descriptors_used"`
	FileDescriptorsLimit          int64  `json:"file_descriptors_limit"`
	PressureReason                string `json:"pressure_reason"`
	SampleError                   string `json:"sample_error,omitempty"`
	StreamChargeBytes             int64  `json:"stream_charge_bytes"`
	StreamChargeFDs               int64  `json:"stream_charge_fds"`
	ReceiveWindowBytes            int64  `json:"receive_window_bytes"`
	SessionResourceBytes          int64  `json:"session_resource_bytes"`
	AggregateInitialPoolBytes     int64  `json:"aggregate_initial_pool_bytes"`
	AggregateGrowthPoolBytes      int64  `json:"aggregate_growth_pool_bytes"`
	AggregateCurrentPoolBytes     int64  `json:"aggregate_current_pool_bytes"`
	AggregateSessionMetadataBytes int64  `json:"aggregate_session_metadata_bytes"`
	LegacyControlSupplementBytes  int64  `json:"legacy_control_supplement_bytes"`
	LegacySessionEnvelopeBytes    int64  `json:"legacy_session_envelope_bytes"`
	LegacyStreamSupplementBytes   int64  `json:"legacy_stream_supplement_bytes"`
	RejectedPressure              uint64 `json:"rejected_pressure"`
	RejectedFixedLimit            uint64 `json:"rejected_fixed_limit"`
}

type externalQualificationAgentRuntime struct {
	HeapAlloc      uint64                                    `json:"heap_alloc_bytes"`
	HeapSys        uint64                                    `json:"heap_sys_bytes"`
	NumGC          uint32                                    `json:"num_gc"`
	PauseNS        uint64                                    `json:"pause_total_ns"`
	BallastBytes   int64                                     `json:"agent_ballast_bytes,omitempty"`
	Capacity       *externalQualificationAgentCapacity       `json:"agent_capacity,omitempty"`
	TunnelSessions []externalQualificationAgentTunnelSession `json:"tunnel_sessions,omitempty"`
}

type externalQualificationAgentTunnelSession struct {
	Lane                  int    `json:"lane"`
	Aggregate             bool   `json:"aggregate"`
	ReceiveLimitBytes     uint32 `json:"receive_limit_bytes,omitempty"`
	ReceiveAvailableBytes uint32 `json:"receive_available_bytes,omitempty"`
	TotalUnits            uint32 `json:"total_units,omitempty"`
	FreeUnits             uint32 `json:"free_units,omitempty"`
	BufferedUnits         uint32 `json:"buffered_units,omitempty"`
	PendingRefundBytes    uint64 `json:"pending_refund_bytes,omitempty"`
}

type externalQualificationObservedSession struct {
	aggregate bool
	session   *yamux.Session
}

type externalQualificationSessionRegistry struct {
	mu       sync.Mutex
	sessions map[int]externalQualificationObservedSession
}

func newExternalQualificationSessionRegistry(size int) *externalQualificationSessionRegistry {
	return &externalQualificationSessionRegistry{sessions: make(map[int]externalQualificationObservedSession, size)}
}

func (r *externalQualificationSessionRegistry) observe(observation tunnelSessionObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if observation.Connected {
		r.sessions[observation.Lane] = externalQualificationObservedSession{
			aggregate: observation.Aggregate,
			session:   observation.Session,
		}
		return
	}
	if current, ok := r.sessions[observation.Lane]; ok && current.session == observation.Session {
		delete(r.sessions, observation.Lane)
	}
}

func (r *externalQualificationSessionRegistry) snapshot(maxLane int) []externalQualificationAgentTunnelSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]externalQualificationAgentTunnelSession, 0, len(r.sessions))
	for lane := 0; lane <= maxLane; lane++ {
		observed, ok := r.sessions[lane]
		if !ok {
			continue
		}
		row := externalQualificationAgentTunnelSession{Lane: lane, Aggregate: observed.aggregate}
		if observed.aggregate && lane > 0 {
			pool := observed.session.AggregateReceiveWindowStats()
			row.ReceiveLimitBytes = pool.ReceiveLimit
			row.ReceiveAvailableBytes = pool.ReceiveAvailable
			row.TotalUnits = pool.TotalUnits
			row.FreeUnits = pool.FreeUnits
			row.BufferedUnits = pool.BufferedUnits
			row.PendingRefundBytes = pool.PendingRefund
		}
		result = append(result, row)
	}
	return result
}

func TestExternalQualificationSessionRegistryIgnoresStaleDisconnect(t *testing.T) {
	registry := newExternalQualificationSessionRegistry(2)
	oldSession := &yamux.Session{}
	newSession := &yamux.Session{}
	registry.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Connected: true, Session: oldSession})
	registry.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Connected: true, Session: newSession})
	registry.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: oldSession})
	if got := registry.snapshot(1); len(got) != 1 || got[0].Lane != 1 || !got[0].Aggregate {
		t.Fatalf("stale disconnect removed replacement session: %+v", got)
	}
	registry.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: newSession})
	if got := registry.snapshot(1); len(got) != 0 {
		t.Fatalf("replacement disconnect left session registered: %+v", got)
	}
}

func TestExternalProxyQualificationAgentProcess(t *testing.T) {
	if os.Getenv("P2PSTREAM_QUALIFICATION_ROLE") != "agent-external" {
		t.Skip("external qualification agent subprocess only")
	}
	runExternalProxyQualificationAgent(t)
}

func runExternalProxyQualificationAgent(t *testing.T) {
	managementURL := requiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_MANAGEMENT_URL")
	publicID := requiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID")
	token := requiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_AGENT_TOKEN")
	caFile := requiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE")
	readyPath := requiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_READY")
	stopPath := requiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_STOP")
	connections := tunnel.DefaultParallelTunnels
	if raw := strings.TrimSpace(os.Getenv("P2PSTREAM_QUALIFICATION_AGENT_TUNNELS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > tunnel.MaxParallelTunnels {
			t.Fatalf("invalid external qualification tunnel count %q", raw)
		}
		connections = parsed
	}
	ballastMiB := 0
	if raw := strings.TrimSpace(os.Getenv("P2PSTREAM_QUALIFICATION_AGENT_BALLAST_MIB")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 3840 {
			t.Fatalf("invalid external qualification agent ballast MiB %q", raw)
		}
		ballastMiB = parsed
	}
	ballast := newExternalQualificationBallast(externalQualificationBallastChunkBytes)
	ballast.setBytes(int64(ballastMiB) << 20)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var capacity atomic.Pointer[agentTunnelCapacityRuntime]
	sessions := newExternalQualificationSessionRegistry(connections + 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- RunContext(ctx, Options{
			ManagementURL: managementURL, PublicID: publicID, Token: token,
			ManagementCAFile: caFile, AllowAnyTarget: true,
			TunnelConnections: connections, TunnelMaxConcurrentRequests: tunnel.MaxConcurrentAgentRequestsLimit,
			TunnelCapacityAdaptive: true,
			capacityObserver: func(runtime *agentTunnelCapacityRuntime) {
				capacity.Store(runtime)
			},
			tunnelSessionObserver: sessions.observe,
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for capacity.Load() == nil && time.Now().Before(deadline) {
		select {
		case err := <-runDone:
			t.Fatalf("external qualification agent stopped during startup: %v", err)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if capacity.Load() == nil {
		t.Fatal("external qualification agent capacity runtime was not created")
	}

	adminBindAddress := strings.TrimSpace(os.Getenv("P2PSTREAM_QUALIFICATION_AGENT_ADMIN_BIND_ADDRESS"))
	if adminBindAddress == "" {
		adminBindAddress = "127.0.0.1"
	}
	if net.ParseIP(adminBindAddress) == nil {
		t.Fatalf("invalid external qualification agent admin bind address %q", adminBindAddress)
	}
	adminListener, err := net.Listen("tcp", net.JoinHostPort(adminBindAddress, "0"))
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := strings.TrimSpace(r.URL.Query().Get("ballast_mib")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 0 || parsed > 3840 {
				http.Error(w, "invalid ballast_mib", http.StatusBadRequest)
				return
			}
			ballast.setBytes(int64(parsed) << 20)
		}
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		result := externalQualificationAgentRuntime{
			HeapAlloc: stats.HeapAlloc, HeapSys: stats.HeapSys, NumGC: stats.NumGC, PauseNS: stats.PauseTotalNs,
			BallastBytes: ballast.size(),
		}
		result.TunnelSessions = sessions.snapshot(connections)
		if current := capacity.Load(); current != nil {
			snapshot := current.snapshot()
			result.Capacity = &externalQualificationAgentCapacity{
				Adaptive: snapshot.Adaptive, ResourceLimited: snapshot.ResourceLimited,
				Maximum: snapshot.Maximum, AdmissionLimit: snapshot.AdmissionLimit,
				OrdinaryAdmissionLimit:   snapshot.OrdinaryAdmissionLimit,
				InUse:                    snapshot.InUse,
				ControlInUse:             snapshot.ControlInUse,
				ControlReservedSlots:     snapshot.ControlReservedSlots,
				ControlReservedBaseBytes: snapshot.ControlReservedBaseBytes,
				ControlReservedBaseFDs:   snapshot.ControlReservedBaseFDs,
				Pressure:                 snapshot.Pressure.String(), MemoryUsedBytes: snapshot.MemoryUsedBytes,
				MemoryLimitBytes: snapshot.MemoryLimitBytes, MemorySource: snapshot.MemorySource,
				FileDescriptorsUsed: snapshot.FileDescriptorsUsed, FileDescriptorsLimit: snapshot.FileDescriptorsLimit,
				PressureReason: snapshot.PressureReason, SampleError: snapshot.SampleError,
				StreamChargeBytes: snapshot.StreamChargeBytes, StreamChargeFDs: snapshot.StreamChargeFDs,
				ReceiveWindowBytes: snapshot.ReceiveWindowBytes, SessionResourceBytes: snapshot.SessionResourceBytes,
				AggregateInitialPoolBytes:     snapshot.AggregateInitialPoolBytes,
				AggregateGrowthPoolBytes:      snapshot.AggregateGrowthPoolBytes,
				AggregateCurrentPoolBytes:     snapshot.AggregateCurrentPoolBytes,
				AggregateSessionMetadataBytes: snapshot.AggregateSessionMetadataBytes,
				LegacyControlSupplementBytes:  snapshot.LegacyControlSupplementBytes,
				LegacySessionEnvelopeBytes:    snapshot.LegacySessionEnvelopeBytes,
				LegacyStreamSupplementBytes:   snapshot.LegacyStreamSupplementBytes,
				RejectedPressure:              snapshot.RejectedPressure, RejectedFixedLimit: snapshot.RejectedFixedLimit,
			}
		}
		_ = json.NewEncoder(w).Encode(result)
	})}
	go func() { _ = admin.Serve(adminListener) }()
	writeExternalAgentQualificationJSON(t, readyPath, externalQualificationAgentReady{AdminURL: "http://" + adminListener.Addr().String(), PID: os.Getpid()})

	waitExternalAgentQualificationStop(t, stopPath)
	ballast.keepAlive()
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = admin.Shutdown(shutdownCtx)
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("external qualification agent stopped: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("external qualification agent did not stop")
	}
}

func requiredExternalAgentQualificationEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func writeExternalAgentQualificationJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}

func waitExternalAgentQualificationStop(t *testing.T, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
