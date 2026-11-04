package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestAggregateTransportLanePrefersFreePoolWithoutDisconnectingPressure(t *testing.T) {
	first, firstPeer := newAggregateAgentTunnelTestSession(t)
	second, secondPeer := newAggregateAgentTunnelTestSession(t)
	agent := &AgentConn{AggregateFlowControl: true}
	agent.installTunnel(1, first)
	agent.installTunnel(2, second)
	var wg sync.WaitGroup
	defer func() { first.Close(); second.Close(); wg.Wait() }()
	fill := func(sender, receiver *yamux.Session) {
		for i := 0; i < 4; i++ {
			stream, err := sender.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := receiver.AcceptStream(); err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func() { defer wg.Done(); stream.Write(make([]byte, 512<<10)) }()
		}
		deadline := time.Now().Add(time.Second)
		for receiver.AggregateReceiveWindowStats().FreeUnits != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if receiver.AggregateReceiveWindowStats().FreeUnits != 0 {
			t.Fatal("pool did not fill")
		}
	}
	fill(firstPeer, first)
	if got := agent.transportLane(); got != 2 {
		t.Fatalf("pressured lane selected over free bulk lane: %d", got)
	}
	fill(secondPeer, second)
	if got := agent.transportLane(); got != 1 && got != 2 {
		t.Fatalf("pressure misclassified as disconnection/control: %d", got)
	}
}

// Actual public listener, authenticated agent runtime, one bulk lane, TCP
// clients with tiny receive buffers, and a finite origin response. No client
// drains the eight held responses to make the fresh request progress.
func TestPublicAggregateSlowReadersDoNotStarveSameLane(t *testing.T) {
	f := newRealAgentProxyFixture(t, 1, 512<<10, 0)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := f.app.CloseObservabilityRecorder(ctx); err != nil {
			t.Error(err)
		}
	})
	var origins sync.WaitGroup
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins.Add(1)
		defer origins.Done()
		if r.URL.Path != "/slow" {
			io.WriteString(w, "fresh")
			return
		}
		const total = 64 << 20
		w.Header().Set("Content-Length", fmt.Sprint(total))
		chunk := make([]byte, 32<<10)
		for written := 0; written < total; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer origin.Close()
	address := startPressurePublicListener(t, f, origin.URL)
	var clients []net.Conn
	defer func() {
		for _, conn := range clients {
			conn.Close()
		}
		f.app.CloseAgentTransports()
	}()
	for i := 0; i < 8; i++ {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		if err := conn.(*net.TCPConn).SetReadBuffer(4096); err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(8 * time.Second))
		fmt.Fprintf(conn, "GET /slow HTTP/1.1\r\nHost: pressure.test\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("slow response=%d", resp.StatusCode)
		}
	}
	lane := f.agent.tunnelSession(1)
	deadline := time.Now().Add(3 * time.Second)
	for lane.AggregateReceiveWindowStats().FreeUnits != 0 && lane.AggregateReceiveWindowStats().PressureResets == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := lane.AggregateReceiveWindowStats(); stats.FreeUnits != 0 && stats.PressureResets == 0 {
		t.Fatalf("real slow clients never pressured lane: %+v", stats)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/fresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "pressure.test"
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || readErr != nil || string(data) != "fresh" {
		t.Fatalf("fresh same-lane response=%d %q %v", resp.StatusCode, data, readErr)
	}
	stats := lane.AggregateReceiveWindowStats()
	if stats.PressureResets == 0 || stats.ReceiveLimit != 2<<20 {
		t.Fatalf("progress bypassed fixed-pool eviction: %+v", stats)
	}
	if f.agent.tunnelSession(1) != lane || lane.IsClosed() || !realAgentAggregateLaneShape(f.agent, 1) {
		t.Fatal("progress replaced lane or generation")
	}
	for _, conn := range clients {
		conn.Close()
	}
	f.app.CloseAgentTransports()
	origin.CloseClientConnections()
	origins.Wait()
}

func startPressurePublicListener(t *testing.T, f *realAgentProxyFixture, originURL string) string {
	t.Helper()
	listener := publicListenerConfig{ID: 1, Name: "pressure-progress", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
	target := f.target
	target.RouteID = 10
	target.URL = originURL
	target.ParsedURL = mustParseAggregateTestURL(t, originURL)
	target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"pressure": "yes"}}
	target.AgentLoadBalancing = publicRouteTargetLoadBalancingRoundRobin
	snapshot := &publicProxySnapshot{Listeners: map[int64]publicListenerConfig{1: listener}, RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 10, ListenerID: 1, Enabled: true, HostPattern: "pressure.test", PathPrefix: "/", PathSecurityMode: publicRoutePathSecurityModeStrict, Action: publicRouteActionForward, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Targets: []publicRouteTargetConfig{target}}}}, RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target}, Agents: map[int64]publicAgentConfig{f.agent.AgentID: {ID: f.agent.AgentID, PublicID: f.agent.PublicID, Enabled: true, Labels: map[string]string{"pressure": "yes"}}}, CacheSettings: defaultPublicCacheSettings()}
	setPublicSnapshotForTest(t, f.app, snapshot)
	if _, err := f.app.startPublicListenerFromSnapshot(listener, snapshot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		f.app.stopPublicListenerRuntime(ctx, 1)
	})
	f.app.proxyMu.Lock()
	address := f.app.publicListenerState[1].BoundAddress
	f.app.proxyMu.Unlock()
	return address
}

type pressureUploadSource struct{}

func (pressureUploadSource) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestPublicAggregateUnreadOriginsDoNotStarveSameLane(t *testing.T) {
	f := newRealAgentProxyFixture(t, 1, 512<<10, 0)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := f.app.CloseObservabilityRecorder(ctx); err != nil {
			t.Error(err)
		}
	})
	started := make(chan struct{}, 8)
	stop := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload" {
			io.WriteString(w, "fresh")
			return
		}
		started <- struct{}{}
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer origin.Close()
	defer close(stop)
	address := startPressurePublicListener(t, f, origin.URL)
	ctx, cancel := context.WithCancel(t.Context())
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	var wg sync.WaitGroup
	defer func() { cancel(); f.app.CloseAgentTransports(); transport.CloseIdleConnections(); wg.Wait() }()
	for i := 0; i < 8; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/upload", io.NopCloser(io.LimitReader(pressureUploadSource{}, 16<<20)))
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = 16 << 20
		req.Host = "pressure.test"
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("unread origins did not start")
		}
	}
	lane := f.agent.tunnelSession(1)
	deadline := time.Now().Add(3 * time.Second)
	for lane.AggregateReceiveWindowStats().SendAvailable != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := lane.AggregateReceiveWindowStats(); stats.SendAvailable != 0 {
		t.Fatalf("unread origins never exhausted peer allowance: %+v", stats)
	}
	freshCtx, finish := context.WithTimeout(t.Context(), 3*time.Second)
	defer finish()
	req, err := http.NewRequestWithContext(freshCtx, http.MethodGet, "http://"+address+"/fresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "pressure.test"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || readErr != nil || string(data) != "fresh" {
		t.Fatalf("fresh after stalled uploads=%d %q %v", resp.StatusCode, data, readErr)
	}
	if stats := lane.AggregateReceiveWindowStats(); stats.SendLimit != 2<<20 {
		t.Fatalf("remote pool grew: %+v", stats)
	}
	if f.agent.tunnelSession(1) != lane || lane.IsClosed() || !realAgentAggregateLaneShape(f.agent, 1) {
		t.Fatal("progress replaced lane or generation")
	}
}
