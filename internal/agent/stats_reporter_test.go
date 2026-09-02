package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/sysmetrics"
)

type fakeAgentStatsReportClient struct {
	report func(context.Context, *connect.Request[p2pstreamv1.AgentStatsRequest]) (*connect.Response[p2pstreamv1.AgentStatsResponse], error)
}

func (c fakeAgentStatsReportClient) ReportStats(ctx context.Context, req *connect.Request[p2pstreamv1.AgentStatsRequest]) (*connect.Response[p2pstreamv1.AgentStatsResponse], error) {
	return c.report(ctx, req)
}

func TestAgentStatsMemoryUsesSysNotAlloc(t *testing.T) {
	mem := runtime.MemStats{
		Alloc: 12 << 20,
		Sys:   34 << 20,
	}
	if got := agentStatsMemorySysMB(mem); got != 34 {
		t.Fatalf("agentStatsMemorySysMB() = %d, want 34", got)
	}
}

func TestAgentStatsRequestIncludesBuildIdentity(t *testing.T) {
	req := buildAgentStatsRequest("agent-build-test", nil)
	if req.AgentVersion != buildinfo.Version || req.AgentCommit != buildinfo.Commit {
		t.Fatalf("agent build identity = %q/%q, want %q/%q", req.AgentVersion, req.AgentCommit, buildinfo.Version, buildinfo.Commit)
	}
}

func TestAgentStatsRequestIncludesAdaptiveCapacityAndRealMemorySignal(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "cgroup_v2"}
	controller, err := sysmetrics.NewAdaptiveMemoryController(
		sysmetrics.DefaultAdaptiveMemoryConfig(),
		sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) { return usage, nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	capacity := newAgentTunnelCapacityRuntime(2048, true, controller)
	capacity.forceRefresh()
	release, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("healthy adaptive capacity rejected test lease")
	}
	defer release()

	req := buildAgentStatsRequestWithCapacity("agent-adaptive-stats", nil, nil, capacity)
	if !req.TunnelCapacityAdaptive || req.TunnelAdmissionLimit <= 1 || req.TunnelStreamsInUse != 1 || req.MemoryPressure != "healthy" {
		t.Fatalf("adaptive stats = %+v", req)
	}
	if req.MemoryUsageBytes != usage.UsedBytes || req.MemoryLimitBytes != usage.LimitBytes || req.MemorySource != usage.Source {
		t.Fatalf("adaptive memory stats = used %d limit %d source %q", req.MemoryUsageBytes, req.MemoryLimitBytes, req.MemorySource)
	}
}

func TestStatsReporterStopsWhenContextCanceled(t *testing.T) {
	withAgentStatsReportInterval(t, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	reportCalled := make(chan struct{}, 1)
	client := fakeAgentStatsReportClient{
		report: func(context.Context, *connect.Request[p2pstreamv1.AgentStatsRequest]) (*connect.Response[p2pstreamv1.AgentStatsResponse], error) {
			select {
			case reportCalled <- struct{}{}:
			default:
			}
			return connect.NewResponse(&p2pstreamv1.AgentStatsResponse{}), nil
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runStatsReporter(ctx, client, "agent-stats-test", "token", nil)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stats reporter to stop")
	}
	select {
	case <-reportCalled:
		t.Fatal("ReportStats ran before cancellation")
	default:
	}
}

func TestReportAgentStatsUsesPerAttemptTimeout(t *testing.T) {
	withAgentStatsReportTimeout(t, 25*time.Millisecond)
	client := fakeAgentStatsReportClient{
		report: func(ctx context.Context, req *connect.Request[p2pstreamv1.AgentStatsRequest]) (*connect.Response[p2pstreamv1.AgentStatsResponse], error) {
			if got := req.Header().Get("Authorization"); got != "Bearer stats-token" {
				t.Fatalf("authorization header = %q, want bearer token", got)
			}
			if req.Msg.AgentPublicId != "agent-stats-test" {
				t.Fatalf("agent public id = %q, want agent-stats-test", req.Msg.AgentPublicId)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	started := time.Now()
	err := reportAgentStats(context.Background(), client, "agent-stats-test", "stats-token", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reportAgentStats() error = %T %[1]v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("reportAgentStats() took %s, want under 1s", elapsed)
	}
}

func TestRunContextReturnsAfterCancellationDuringReconnectBackoff(t *testing.T) {
	withAgentReconnectBackoff(t, time.Hour, time.Hour)
	withAgentStatsReportInterval(t, time.Hour)

	tunnelAttempt := make(chan struct{}, 1)
	mgmt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/tunnel" {
			select {
			case tunnelAttempt <- struct{}{}:
			default:
			}
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	}))
	defer mgmt.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunContext(ctx, Options{
			ManagementURL:           mgmt.URL,
			PublicID:                "agent-backoff-test",
			Token:                   "token",
			AllowInsecureManagement: true,
		})
	}()

	select {
	case <-tunnelAttempt:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel attempt")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunContext() error = %T %[1]v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RunContext to stop")
	}
}

func withAgentStatsReportInterval(t *testing.T, interval time.Duration) {
	t.Helper()
	previous := agentStatsReportInterval
	agentStatsReportInterval = interval
	t.Cleanup(func() {
		agentStatsReportInterval = previous
	})
}

func withAgentStatsReportTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := agentStatsReportTimeout
	agentStatsReportTimeout = timeout
	t.Cleanup(func() {
		agentStatsReportTimeout = previous
	})
}

func withAgentReconnectBackoff(t *testing.T, min time.Duration, max time.Duration) {
	t.Helper()
	previousMin := agentReconnectBackoffMin
	previousMax := agentReconnectBackoffMax
	agentReconnectBackoffMin = min
	agentReconnectBackoffMax = max
	t.Cleanup(func() {
		agentReconnectBackoffMin = previousMin
		agentReconnectBackoffMax = previousMax
	})
}
