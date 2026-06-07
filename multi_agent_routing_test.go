package main_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

func TestDirectPublicRouteTargetProxiesWithoutAgent(t *testing.T) {
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

func TestDirectPublicRouteTargetHonorsTLSSkipVerify(t *testing.T) {
	targetSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tls ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "direct-tls-listener",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	createProxyRouteTargetForListener(t, database, listener.ID, "/", "direct-tls", targetSrv.URL, "direct", "", true, true)
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
	ensureAgentSystemLabelForTest(t, database, agent.ID, agent.PublicID)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "offline-listener",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	createProxyRouteTargetForListener(t, database, listener.ID, "/", "offline-backend", "http://127.0.0.1:1", "agent", agent.PublicID, false, true)

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
	ensureAgentSystemLabelForTest(t, database, agentA.GetId(), agentA.GetPublicId())
	ensureAgentSystemLabelForTest(t, database, agentB.GetId(), agentB.GetPublicId())

	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "multi-listener",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	createProxyRouteTargetForListener(t, database, listener.ID, "/", "backend-a", targetSrv.URL, "agent", agentA.GetPublicId(), false, true)
	createProxyRouteTargetForListener(t, database, listener.ID, "/b", "backend-b", targetSrv.URL, "agent", agentB.GetPublicId(), false, false)

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

func ensureAgentSystemLabelForTest(t *testing.T, database *db.DB, agentID int64, publicID string) {
	t.Helper()
	if _, err := database.UpsertAgentLabel(context.Background(), db.UpsertAgentLabelParams{
		AgentID: agentID,
		Key:     "p2pstream.io/agent-id",
		Value:   publicID,
		Source:  "system",
	}); err != nil {
		t.Fatalf("upsert system label for agent %d: %v", agentID, err)
	}
}

func createProxyRouteTargetForListener(t *testing.T, database *db.DB, listenerID int64, pathPrefix string, name string, targetOrigin string, transport string, agentPublicID string, tlsSkipVerify bool, isDefault bool) db.PublicRouteTarget {
	t.Helper()
	selector := "{}"
	if transport == "agent" {
		payload, err := json.Marshal(map[string]map[string]string{
			"match_labels": {"p2pstream.io/agent-id": agentPublicID},
		})
		if err != nil {
			t.Fatalf("marshal agent selector: %v", err)
		}
		selector = string(payload)
	}
	route, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID:                 listenerID,
		Priority:                   10,
		PathPrefix:                 pathPrefix,
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  boolIntForTest(isDefault),
		Action:                     "forward",
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route target route %s: %v", name, err)
	}
	target, err := database.CreatePublicRouteTarget(context.Background(), db.CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                name,
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 targetOrigin,
		Transport:                           transport,
		AgentSelectorJson:                   selector,
		AgentLoadBalancing:                  "round_robin",
		TlsSkipVerify:                       boolIntForTest(tlsSkipVerify),
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckMethod:                   http.MethodGet,
		HealthCheckPath:                     "/",
		HealthCheckIntervalMillis:           10000,
		HealthCheckTimeoutMillis:            2000,
		HealthCheckHealthyThreshold:         2,
		HealthCheckUnhealthyThreshold:       2,
		HealthCheckExpectedStatusMin:        200,
		HealthCheckExpectedStatusMax:        399,
		StaticStatusCode:                    http.StatusOK,
		StaticResponseBodyMode:              "inline",
	})
	if err != nil {
		t.Fatalf("create route target %s: %v", name, err)
	}
	return target
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
