package main_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/server"
)

func TestDirectPublicBackendProxiesWithoutAgent(t *testing.T) {
	targetMux := http.NewServeMux()
	targetMux.HandleFunc("/direct", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "x=1" {
			t.Fatalf("query = %q, want x=1", r.URL.RawQuery)
		}
		w.Header().Set("X-Direct-Method", r.Method)
		w.Header().Set("X-Direct-Body", mustReadBody(t, r))
		_, _ = w.Write([]byte("direct ok"))
	})
	targetSrv := httptest.NewServer(targetMux)
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	app := server.NewApp(testManagementConfig(config.Config{}), database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	req, err := http.NewRequest(http.MethodPost, "http://"+publicListenerBoundAddress(t, status, listener.ID)+"/direct?x=1", bytes.NewReader([]byte("payload")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("direct request: %v", err)
	}
	defer resp.Body.Close()
	body := mustReadResponseBody(t, resp)
	if resp.StatusCode != http.StatusOK || body != "direct ok" {
		t.Fatalf("unexpected direct response: status=%d body=%q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Direct-Method") != http.MethodPost || resp.Header.Get("X-Direct-Body") != "payload" {
		t.Fatalf("direct proxy did not preserve method/body headers: %+v", resp.Header)
	}
}

func TestDirectPublicBackendHonorsTLSSkipVerify(t *testing.T) {
	targetSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tls ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:          "direct-tls",
		TargetOrigin:  targetSrv.URL,
		BackendType:   "proxy_forward",
		ForwardMode:   "direct",
		LoadBalancing: "round_robin",
		TlsSkipVerify: 1,
		Enabled:       1,
	})
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "direct-tls-listener",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	app := server.NewApp(testManagementConfig(config.Config{}), database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	resp, err := http.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + "/")
	if err != nil {
		t.Fatalf("direct tls request: %v", err)
	}
	defer resp.Body.Close()
	body := mustReadResponseBody(t, resp)
	if resp.StatusCode != http.StatusOK || body != "tls ok" {
		t.Fatalf("unexpected direct tls response: status=%d body=%q", resp.StatusCode, body)
	}
}

func TestAgentPoolBackendReturns503WhenAssignedAgentOffline(t *testing.T) {
	database := newTestDB(t)
	app := server.NewApp(&config.Config{
		BootstrapAgentID:     "offline-agent",
		BootstrapAgentName:   "Offline Agent",
		BootstrapAgentToken:  "offline-token",
		ManagementSetupToken: testSetupToken,
	}, database)
	agent, err := database.GetAgentByPublicID(context.Background(), "offline-agent")
	if err != nil {
		t.Fatalf("get bootstrap agent: %v", err)
	}
	backend := createAgentPoolBackend(t, database, "offline-backend", "http://127.0.0.1:1", agent.ID, 100)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "offline-listener",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	resp, err := http.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + "/")
	if err != nil {
		t.Fatalf("offline agent request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestAgentPoolBackendsRouteOnlyToAssignedAgents(t *testing.T) {
	targetMux := http.NewServeMux()
	targetMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("agent ok"))
	})
	targetSrv := httptest.NewServer(targetMux)
	defer targetSrv.Close()

	database := newTestDB(t)
	app := server.NewApp(testManagementConfig(config.Config{}), database)
	mgmtSrv, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	agentA, tokenA := createRegisteredAgent(t, client, cookie, "agent-a", "Agent A")
	agentB, tokenB := createRegisteredAgent(t, client, cookie, "agent-b", "Agent B")

	backendA := createAgentPoolBackend(t, database, "backend-a", targetSrv.URL, agentA.Id, 100)
	backendB := createAgentPoolBackend(t, database, "backend-b", targetSrv.URL, agentB.Id, 100)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "multi-listener",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backendA.ID,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	if _, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID: listener.ID,
		Priority:   1,
		PathPrefix: "/b",
		BackendID:  sql.NullInt64{Int64: backendB.ID, Valid: true},
		Enabled:    1,
	}); err != nil {
		t.Fatalf("create backend-b route: %v", err)
	}

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	doneA := make(chan struct{})
	doneB := make(chan struct{})
	go func() {
		_ = runAgent(ctx, mgmtSrv.URL, agentA.GetPublicId(), tokenA)
		close(doneA)
	}()
	go func() {
		_ = runAgent(ctx, mgmtSrv.URL, agentB.GetPublicId(), tokenB)
		close(doneB)
	}()
	time.Sleep(250 * time.Millisecond)

	baseURL := "http://" + publicListenerBoundAddress(t, status, listener.ID)
	assertAgentRequest(t, baseURL+"/a")
	assertAgentRequest(t, baseURL+"/b")
	assertSelectedAgents(t, database, []int64{agentA.GetId(), agentB.GetId()})

	cancel()
	<-doneA
	<-doneB
}

func createRegisteredAgent(
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	_ string,
	name string,
) (*p2pstreamv1.Agent, string) {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    name,
		Enabled: true,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreateAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("create agent %s: %v", name, err)
	}
	if resp.Msg.GetAgent().GetPublicId() == "" {
		t.Fatalf("create agent %s returned empty public id", name)
	}
	return resp.Msg.GetAgent(), resp.Msg.GetToken()
}

func createAgentPoolBackend(t *testing.T, database *db.DB, name string, targetOrigin string, agentID int64, weight int64) db.PublicBackend {
	t.Helper()
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:          name,
		TargetOrigin:  targetOrigin,
		BackendType:   "proxy_forward",
		ForwardMode:   "agent_pool",
		LoadBalancing: "weighted_round_robin",
		Enabled:       1,
	})
	if err != nil {
		t.Fatalf("create backend %s: %v", name, err)
	}
	if _, err := database.CreatePublicBackendAgent(context.Background(), db.CreatePublicBackendAgentParams{
		BackendID: backend.ID,
		AgentID:   agentID,
		Position:  0,
		Weight:    weight,
		Enabled:   1,
	}); err != nil {
		t.Fatalf("assign backend %s to agent %d: %v", name, agentID, err)
	}
	return backend
}

func assertAgentRequest(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request %s status = %d, want 200", url, resp.StatusCode)
	}
}

func assertSelectedAgents(t *testing.T, database *db.DB, want []int64) {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `SELECT agent_id FROM proxy_request_events WHERE agent_id IS NOT NULL ORDER BY id`)
	if err != nil {
		t.Fatalf("query proxy request events: %v", err)
	}
	defer rows.Close()
	got := make([]int64, 0, len(want))
	for rows.Next() {
		var agentID sql.NullInt64
		if err := rows.Scan(&agentID); err != nil {
			t.Fatalf("scan proxy request event: %v", err)
		}
		if agentID.Valid {
			got = append(got, agentID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate proxy request events: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("selected agents = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selected agents = %v, want %v", got, want)
		}
	}
}

func mustReadBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(body)
}

func mustReadResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}
