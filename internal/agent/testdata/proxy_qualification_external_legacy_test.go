//go:build linux

// This frozen overlay is copied into a clean 3ec137c archive. The testdata
// directory keeps it out of current package builds while preserving the exact
// historical external-process fixture source.
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
	"testing"
	"time"

	"p2pstream/internal/tunnel"
)

type legacyExternalQualificationAgentReady struct {
	AdminURL string `json:"admin_url"`
	PID      int    `json:"pid"`
}

func TestExternalProxyQualificationAgentProcess(t *testing.T) {
	if os.Getenv("P2PSTREAM_QUALIFICATION_ROLE") != "agent-external" {
		t.Skip("external qualification agent subprocess only")
	}
	managementURL := legacyRequiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_MANAGEMENT_URL")
	publicID := legacyRequiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID")
	token := legacyRequiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_AGENT_TOKEN")
	caFile := legacyRequiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE")
	readyPath := legacyRequiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_READY")
	stopPath := legacyRequiredExternalAgentQualificationEnv(t, "P2PSTREAM_QUALIFICATION_STOP")
	connections := tunnel.DefaultParallelTunnels
	if raw := strings.TrimSpace(os.Getenv("P2PSTREAM_QUALIFICATION_AGENT_TUNNELS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > tunnel.MaxParallelTunnels {
			t.Fatalf("invalid external qualification tunnel count %q", raw)
		}
		connections = parsed
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- RunContext(ctx, Options{
			ManagementURL: managementURL, PublicID: publicID, Token: token,
			ManagementCAFile: caFile, AllowAnyTarget: true,
			TunnelConnections: connections, TunnelMaxConcurrentRequests: tunnel.MaxConcurrentAgentRequestsLimit,
			TunnelCapacityAdaptive: true,
		})
	}()

	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"heap_alloc_bytes": stats.HeapAlloc,
			"heap_sys_bytes":   stats.HeapSys,
			"num_gc":           stats.NumGC,
			"pause_total_ns":   stats.PauseTotalNs,
		})
	})}
	go func() { _ = admin.Serve(adminListener) }()
	legacyWriteExternalAgentQualificationJSON(t, readyPath, legacyExternalQualificationAgentReady{AdminURL: "http://" + adminListener.Addr().String(), PID: os.Getpid()})

	legacyWaitExternalAgentQualificationStop(t, stopPath)
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

func legacyRequiredExternalAgentQualificationEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func legacyWriteExternalAgentQualificationJSON(t *testing.T, path string, value any) {
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

func legacyWaitExternalAgentQualificationStop(t *testing.T, path string) {
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
