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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2pstream/internal/tunnel"
)

type externalQualificationAgentReady struct {
	AdminURL string `json:"admin_url"`
	PID      int    `json:"pid"`
}

type externalQualificationAgentCapacity struct {
	Adaptive                      bool   `json:"adaptive"`
	ResourceLimited               bool   `json:"resource_limited"`
	Maximum                       int    `json:"maximum"`
	AdmissionLimit                int    `json:"admission_limit"`
	InUse                         int    `json:"in_use"`
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
	HeapAlloc    uint64                              `json:"heap_alloc_bytes"`
	HeapSys      uint64                              `json:"heap_sys_bytes"`
	NumGC        uint32                              `json:"num_gc"`
	PauseNS      uint64                              `json:"pause_total_ns"`
	BallastBytes int64                               `json:"agent_ballast_bytes,omitempty"`
	Capacity     *externalQualificationAgentCapacity `json:"agent_capacity,omitempty"`
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
		if err != nil || parsed < 0 || parsed > 2048 {
			t.Fatalf("invalid external qualification agent ballast MiB %q", raw)
		}
		ballastMiB = parsed
	}
	var ballastMu sync.RWMutex
	var ballast []byte
	setBallast := func(mib int) {
		next := make([]byte, mib<<20)
		for offset := 0; offset < len(next); offset += os.Getpagesize() {
			next[offset] = 1
		}
		ballastMu.Lock()
		ballast = next
		ballastMu.Unlock()
		if mib == 0 {
			runtime.GC()
		}
	}
	setBallast(ballastMiB)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var capacity atomic.Pointer[agentTunnelCapacityRuntime]
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
			if parseErr != nil || parsed < 0 || parsed > 2048 {
				http.Error(w, "invalid ballast_mib", http.StatusBadRequest)
				return
			}
			setBallast(parsed)
		}
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		ballastMu.RLock()
		ballastBytes := int64(len(ballast))
		ballastMu.RUnlock()
		result := externalQualificationAgentRuntime{
			HeapAlloc: stats.HeapAlloc, HeapSys: stats.HeapSys, NumGC: stats.NumGC, PauseNS: stats.PauseTotalNs,
			BallastBytes: ballastBytes,
		}
		if current := capacity.Load(); current != nil {
			snapshot := current.snapshot()
			result.Capacity = &externalQualificationAgentCapacity{
				Adaptive: snapshot.Adaptive, ResourceLimited: snapshot.ResourceLimited,
				Maximum: snapshot.Maximum, AdmissionLimit: snapshot.AdmissionLimit, InUse: snapshot.InUse,
				Pressure: snapshot.Pressure.String(), MemoryUsedBytes: snapshot.MemoryUsedBytes,
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
	ballastMu.RLock()
	runtime.KeepAlive(ballast)
	ballastMu.RUnlock()
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
