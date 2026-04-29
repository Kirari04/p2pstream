package main_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/google/uuid"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/httpmsg"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
	"p2pstream/msg"
	"p2pstream/stats"
)

func TestE2E_ReportStats(t *testing.T) {
	// Setup Management Server
	app := server.NewApp(&config.Config{}, nil)
	mgmtMux := http.NewServeMux()
	app.RegisterManagementRoutes(mgmtMux)

	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	mgmtSrv := httptest.NewUnstartedServer(mgmtMux)
	mgmtSrv.Config.Protocols = p
	mgmtSrv.Start()
	defer mgmtSrv.Close()

	// Setup Connect Client
	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		http.DefaultClient,
		mgmtSrv.URL,
		connect.WithGRPC(),
	)

	req := &p2pstreamv1.AgentStatsRequest{
		MemorySysMb:      100,
		NumGoroutine:     5,
		ActiveRequests:   2,
		ReqSuccess:       10,
		ReqInternalError: 3,
	}

	_, err := client.ReportStats(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("Failed to report stats via ConnectRPC: %v", err)
	}

	// Verify the server stored it
	stats := app.LatestAgentStats.Load()
	if stats == nil {
		t.Fatal("Expected stats to be stored in app, got nil")
	}

	if stats.AllocAllocated != 100 {
		t.Errorf("Expected memory 100, got %d", stats.AllocAllocated)
	}
	if stats.ReqSuccess != 10 {
		t.Errorf("Expected 10 successful reqs, got %d", stats.ReqSuccess)
	}
	if stats.ReqInternalError != 3 {
		t.Errorf("Expected 3 internal errors, got %d", stats.ReqInternalError)
	}
}

func TestE2E_GetStatus(t *testing.T) {
	targetOrigin := "https://example.com"
	database := newTestDB(t)
	seedTestHTTPPublicListener(t, database, targetOrigin)
	app := server.NewApp(&config.Config{
		Port:         "0",
		TargetOrigin: targetOrigin,
	}, database)
	if _, err := app.StartProxyListener(context.Background()); err != nil {
		t.Fatalf("start proxy listener: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := app.StopProxyListener(shutdownCtx); err != nil {
			t.Errorf("stop proxy listener: %v", err)
		}
	})

	lastError := "proxy failed previously"
	app.ProxyLastError.Store(&lastError)
	app.ActiveAgent.Store(&server.AgentConn{})
	app.LatestAgentStats.Store(&stats.AgentStats{
		Timestamp:        time.UnixMilli(1700000000123),
		NumGoroutine:     7,
		AllocAllocated:   128,
		ActiveRequests:   4,
		ReqSuccess:       11,
		ReqClientError:   2,
		ReqServerError:   1,
		ReqInternalError: 5,
		BytesReceived:    1234,
		BytesSent:        5678,
	})

	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	req := connect.NewRequest(&p2pstreamv1.GetStatusRequest{})
	req.Header().Set("Cookie", cookie)
	resp, err := client.GetStatus(context.Background(), req)
	if err != nil {
		t.Fatalf("Failed to get status via ConnectRPC: %v", err)
	}

	status := resp.Msg
	if !status.ProxyRunning {
		t.Error("Expected proxy to be running")
	}
	if status.ProxyLastError != lastError {
		t.Errorf("Expected proxy last error %q, got %q", lastError, status.ProxyLastError)
	}
	if !status.AgentConnected {
		t.Error("Expected agent to be connected")
	}
	if status.TargetOrigin != targetOrigin {
		t.Errorf("Expected target origin %q, got %q", targetOrigin, status.TargetOrigin)
	}
	if status.Proxy == nil {
		t.Fatal("Expected proxy status")
	}
	if status.Proxy.State != p2pstreamv1.ProxyState_PROXY_STATE_RUNNING {
		t.Errorf("Expected proxy state RUNNING, got %s", status.Proxy.State)
	}
	if status.LatestAgentStats == nil {
		t.Fatal("Expected latest agent stats")
	}
	if status.LatestAgentStats.MemorySysMb != 128 {
		t.Errorf("Expected memory 128, got %d", status.LatestAgentStats.MemorySysMb)
	}
	if status.LatestAgentStats.ReqInternalError != 5 {
		t.Errorf("Expected internal errors 5, got %d", status.LatestAgentStats.ReqInternalError)
	}
	if status.LatestAgentStats.ReportedAtUnixMillis != 1700000000123 {
		t.Errorf("Expected reported time 1700000000123, got %d", status.LatestAgentStats.ReportedAtUnixMillis)
	}
}

// --- MOCK AGENT LOGIC ---

var agentWriteCh chan *msg.Request
var incomingRequests sync.Map

func runAgent(ctx context.Context, wsURL string) error {
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusInternalError, "agent stopping")

	c.SetReadLimit(128 * 1024)

	agentWriteCh = make(chan *msg.Request, 100)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-agentWriteCh:
				cw, err := c.Writer(ctx, websocket.MessageBinary)
				if err != nil {
					return
				}
				req.WriteTo(cw)
				cw.Close()
			}
		}
	}()

	for {
		_, reader, err := c.Reader(ctx)
		if err != nil {
			return err
		}

		b, err := io.ReadAll(reader)
		if err != nil {
			return err
		}

		m, _ := msg.ParseRequest(bytes.NewReader(b))
		if m != nil {
			if m.Type == msg.RequestTypeHeader || m.Type == msg.RequestTypeHeaderAndBody {
				reqCh := make(chan *msg.Request, 100)
				incomingRequests.Store(m.ID, reqCh)
				reqCh <- m
				go handleAgentRequest(m.ID, reqCh)
			} else {
				if ch, ok := incomingRequests.Load(m.ID); ok {
					ch.(chan *msg.Request) <- m
				}
			}
		}
	}
}

func handleAgentRequest(id uuid.UUID, reqCh chan *msg.Request) {
	defer incomingRequests.Delete(id)

	stream := &httpmsg.ChannelStream{Ch: reqCh}
	firstMsg, err := stream.Next()
	if err != nil {
		return
	}

	req, err := httpmsg.DecodeRequest(firstMsg, stream)
	if err != nil {
		return
	}

	req.RequestURI = ""

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		resp = &http.Response{
			StatusCode:    http.StatusBadGateway,
			Status:        http.StatusText(http.StatusBadGateway),
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader([]byte(err.Error()))),
			ContentLength: int64(len(err.Error())),
		}
	}

	enc := httpmsg.NewResponseEncoder(id, resp)
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err == nil {
			agentWriteCh <- m
		}
	}
}

// --- ACTUAL E2E TEST ---

func TestE2E_RoundTrip(t *testing.T) {
	// 1. Setup target (origin) server
	targetMux := http.NewServeMux()
	targetMux.HandleFunc("/test-e2e-path", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Custom", r.Header.Get("X-E2E-Custom"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("TARGET_RESPONSE"))
		io.Copy(w, r.Body) // echo body back
	})
	targetSrv := httptest.NewServer(targetMux)
	defer targetSrv.Close()

	// Parse the target URL to configure the proxy handler
	targetOrigin, err := url.Parse(targetSrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Setup Server with App architecture
	cfg := &config.Config{
		TargetOrigin:       targetSrv.URL,
		ParsedTargetOrigin: targetOrigin,
	}
	// No DB provided for testing core proxying logic
	app := server.NewApp(cfg, nil)

	proxyMux := http.NewServeMux()
	app.RegisterProxyRoutes(proxyMux)
	proxySrv := httptest.NewServer(proxyMux)
	defer proxySrv.Close()

	mgmtMux := http.NewServeMux()
	app.RegisterManagementRoutes(mgmtMux)

	// h2c protocols for ConnectRPC tests
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	mgmtSrv := httptest.NewUnstartedServer(mgmtMux)
	mgmtSrv.Config.Protocols = p
	mgmtSrv.Start()
	defer mgmtSrv.Close()

	// 3. Setup Agent
	wsURL := "ws" + mgmtSrv.URL[4:] + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentDone := make(chan struct{})
	go func() {
		_ = runAgent(ctx, wsURL)
		close(agentDone)
	}()

	// Wait for agent connection
	time.Sleep(200 * time.Millisecond)

	// 4. Make HTTP request through Proxy
	bodyData := bytes.Repeat([]byte("test_e2e_data_"), 1000) // ~14KB
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, proxySrv.URL+"/test-e2e-path", bytes.NewReader(bodyData))
	req.Header.Set("X-E2E-Custom", "Hello")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if resp.Header.Get("X-Echo-Custom") != "Hello" {
		t.Errorf("Expected custom header to round trip, got %s", resp.Header.Get("X-Echo-Custom"))
	}

	respBody, _ := io.ReadAll(resp.Body)

	// Verify response body contains the origin's response
	if !bytes.Contains(respBody, []byte("TARGET_RESPONSE")) {
		t.Errorf("Expected TARGET_RESPONSE in response, got %s", string(respBody))
	}
	if !bytes.Contains(respBody, []byte("test_e2e_data_")) {
		t.Errorf("Expected body payload in echoed response")
	}

	// 5. Teardown
	cancel() // signal agent to stop
	<-agentDone
}
