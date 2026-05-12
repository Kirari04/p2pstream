package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func TestPublicBackendHealthMonitorExplicitCheckRecovery(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusInternalServerError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	origin, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	backend := publicBackendConfig{
		ID:           1,
		Enabled:      true,
		BackendType:  publicBackendTypeProxyForward,
		ForwardMode:  publicBackendForwardModeDirect,
		ParsedOrigin: origin,
		HealthCheck: publicBackendHealthCheckConfig{
			Enabled:            true,
			Method:             http.MethodGet,
			Path:               "/health",
			Interval:           10 * time.Millisecond,
			Timeout:            time.Second,
			HealthyThreshold:   1,
			UnhealthyThreshold: 1,
			ExpectedStatusMin:  200,
			ExpectedStatusMax:  399,
		},
	}
	monitor := newPublicBackendHealthMonitor()
	snap := &publicProxySnapshot{Backends: map[int64]publicBackendConfig{1: backend}}
	monitor.reconcile(snap, true)
	t.Cleanup(func() { monitor.reconcile(nil, false) })

	waitForHealthStatus(t, monitor, 1, p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable after explicit unhealthy check")
	}
	status.Store(http.StatusOK)
	waitForHealthStatus(t, monitor, 1, p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY)
	if !monitor.available(backend) {
		t.Fatal("backend should be available after explicit health recovery")
	}
}

func TestPublicBackendHealthMonitorPassiveCooldown(t *testing.T) {
	monitor := newPublicBackendHealthMonitor()
	backend := publicBackendConfig{ID: 2, Enabled: true}
	monitor.markPassiveFailure(backend.ID, nil)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable during passive cooldown")
	}
	snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: backend.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY || snapshot.PassiveUnhealthyUntilUnixMillis == 0 {
		t.Fatalf("unexpected passive health snapshot: %+v", snapshot)
	}
}

func waitForHealthStatus(t *testing.T, monitor *publicBackendHealthMonitor, backendID int64, want p2pstreamv1.PublicBackendHealthStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: backendID, enabled: true})
		if snapshot != nil && snapshot.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: backendID, enabled: true})
	t.Fatalf("health status = %+v, want %s", snapshot, want)
}
