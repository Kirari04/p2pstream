package main_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestTrafficTraceSettingsAuthAndRuntimeDefault(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)

	_, err := client.GetTrafficTraceSettings(context.Background(), connect.NewRequest(&p2pstreamv1.GetTrafficTraceSettingsRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)

	cookie := createAdminSession(t, client)
	settings := getTrafficTraceSettings(t, client, cookie)
	if settings.Enabled {
		t.Fatal("new app should start with tracing disabled")
	}
	if settings.Level != p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC {
		t.Fatalf("default trace level = %s, want BASIC", settings.Level)
	}

	settings = setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG)
	if !settings.Enabled || settings.Level != p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG {
		t.Fatalf("unexpected enabled settings: %+v", settings)
	}

	restarted := server.NewApp(&config.Config{}, newTestDB(t))
	_, restartedClient := newTestManagementClient(t, restarted)
	restartedCookie := createAdminSession(t, restartedClient)
	restartedSettings := getTrafficTraceSettings(t, restartedClient, restartedCookie)
	if restartedSettings.Enabled {
		t.Fatal("tracing should not persist across app restarts")
	}
	if restartedSettings.Level != p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC {
		t.Fatalf("restarted trace level = %s, want BASIC", restartedSettings.Level)
	}
}

func TestTrafficTraceStreamRequiresAdminAndSendsInitialSettings(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unauthStream, err := client.StreamTrafficTraceEvents(ctx, connect.NewRequest(&p2pstreamv1.StreamTrafficTraceEventsRequest{}))
	if err != nil {
		requireConnectCode(t, err, connect.CodeUnauthenticated)
	} else if unauthStream.Receive() {
		t.Fatal("unauthenticated trace stream unexpectedly received a message")
	} else {
		requireConnectCode(t, unauthStream.Err(), connect.CodeUnauthenticated)
	}

	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DETAILED)
	stream, stop := openTrafficTraceStream(t, client, cookie, false, 0, time.Second)
	defer stop()

	initial := receiveTrafficTraceResponse(t, stream)
	if initial.Settings == nil {
		t.Fatal("expected initial trace settings")
	}
	if !initial.Settings.Enabled || initial.Settings.Level != p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DETAILED {
		t.Fatalf("unexpected initial stream settings: %+v", initial.Settings)
	}
	if initial.Event != nil {
		t.Fatalf("initial stream response should not include an event: %+v", initial.Event)
	}
}

func TestTrafficTraceSubscriberCountBroadcastsOnSubscribe(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC)

	streamA, stopA := openTrafficTraceStream(t, client, cookie, false, 0, 3*time.Second)
	defer stopA()
	initialA := receiveTrafficTraceResponse(t, streamA)
	if initialA.Settings == nil || initialA.Settings.SubscriberCount != 1 {
		t.Fatalf("stream A initial subscriber count = %+v, want 1", initialA.Settings)
	}

	streamB, stopB := openTrafficTraceStream(t, client, cookie, false, 0, 3*time.Second)
	defer stopB()
	initialB := receiveTrafficTraceResponse(t, streamB)
	if initialB.Settings == nil || initialB.Settings.SubscriberCount != 2 {
		t.Fatalf("stream B initial subscriber count = %+v, want 2", initialB.Settings)
	}

	settingsA := receiveTrafficTraceSettingsUntil(t, streamA, 2)
	if settingsA.SubscriberCount != 2 {
		t.Fatalf("stream A subscriber count update = %d, want 2", settingsA.SubscriberCount)
	}
}

func TestTrafficTraceSubscriberCountBroadcastsOnUnsubscribe(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC)

	streamA, stopA := openTrafficTraceStream(t, client, cookie, false, 0, 3*time.Second)
	defer stopA()
	_ = receiveTrafficTraceResponse(t, streamA)

	streamB, stopB := openTrafficTraceStream(t, client, cookie, false, 0, 3*time.Second)
	_ = receiveTrafficTraceResponse(t, streamB)
	_ = receiveTrafficTraceSettingsUntil(t, streamA, 2)

	stopB()
	settingsA := receiveTrafficTraceSettingsUntil(t, streamA, 1)
	if settingsA.SubscriberCount != 1 {
		t.Fatalf("stream A subscriber count after stream B unsubscribe = %d, want 1", settingsA.SubscriberCount)
	}

	stopA()
	waitTrafficTraceSubscriberCount(t, client, cookie, 0)
}

func TestTrafficTraceHeartbeatPublishesSettingsWithoutEvents(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC)

	stream, stop := openTrafficTraceStream(t, client, cookie, false, 0, 7*time.Second)
	defer stop()
	initial := receiveTrafficTraceResponse(t, stream)
	if initial.Settings == nil || initial.Event != nil {
		t.Fatalf("unexpected initial trace response: %+v", initial)
	}

	heartbeat := receiveTrafficTraceResponse(t, stream)
	if heartbeat.Settings == nil {
		t.Fatalf("expected heartbeat settings response, got %+v", heartbeat)
	}
	if heartbeat.Event != nil {
		t.Fatalf("heartbeat should not include event: %+v", heartbeat.Event)
	}
	if heartbeat.Settings.SubscriberCount != 1 {
		t.Fatalf("heartbeat subscriber count = %d, want 1", heartbeat.Settings.SubscriberCount)
	}
}

func TestTrafficTraceDirectRequestStagesAndLevels(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Trace", "ok")
		_, _ = w.Write([]byte("trace ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DETAILED)

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	resp, err := http.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + "/trace/path?token=visible-at-detailed")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if body := mustReadResponseBody(t, resp); resp.StatusCode != http.StatusOK || body != "trace ok" {
		t.Fatalf("unexpected proxy response: status=%d body=%q", resp.StatusCode, body)
	}

	stream, stop := openTrafficTraceStream(t, client, cookie, true, 0, 2*time.Second)
	defer stop()
	_ = receiveTrafficTraceResponse(t, stream)
	events := collectTrafficTraceEventsUntil(t, stream, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT)

	assertTrafficTraceStages(t, events,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RECEIVED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ROUTE_RESOLVED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_BACKEND_SELECTED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_STARTED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RESPONDED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT,
	)

	finalEvent := events[len(events)-1]
	if finalEvent.Query != "token=visible-at-detailed" {
		t.Fatalf("detailed trace query = %q", finalEvent.Query)
	}
	if finalEvent.RequestHeaders != nil || finalEvent.ResponseHeaders != nil {
		t.Fatalf("detailed trace should not include headers: request=%v response=%v", finalEvent.RequestHeaders, finalEvent.ResponseHeaders)
	}
	if finalEvent.TargetOrigin != targetSrv.URL {
		t.Fatalf("target origin = %q, want %q", finalEvent.TargetOrigin, targetSrv.URL)
	}
	if finalEvent.StatusCode != http.StatusOK {
		t.Fatalf("final status = %d, want 200", finalEvent.StatusCode)
	}
	if finalEvent.BackendName == "" || finalEvent.ListenerName == "" {
		t.Fatalf("expected listener/backend names in final event: %+v", finalEvent)
	}
}

func TestTrafficTraceHeartbeatDoesNotBreakReplay(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("replay ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC)

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	resp, err := http.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + "/replay")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	_ = mustReadResponseBody(t, resp)

	stream, stop := openTrafficTraceStream(t, client, cookie, true, 0, 2*time.Second)
	defer stop()
	_ = receiveTrafficTraceResponse(t, stream)
	events := collectTrafficTraceEventsUntil(t, stream, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT)
	if len(events) == 0 {
		t.Fatal("expected replayed trace events")
	}
}

func TestTrafficTraceHeadersRedactSensitiveValues(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=secret")
		w.Header().Set("X-Upstream-Trace", "visible")
		_, _ = w.Write([]byte("headers ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_HEADERS)

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	req, err := http.NewRequest(http.MethodGet, "http://"+publicListenerBoundAddress(t, status, listener.ID)+"/headers", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sensitive")
	req.Header.Set("X-Request-Trace", "visible")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	_ = mustReadResponseBody(t, resp)

	stream, stop := openTrafficTraceStream(t, client, cookie, true, 0, 2*time.Second)
	defer stop()
	_ = receiveTrafficTraceResponse(t, stream)
	events := collectTrafficTraceEventsUntil(t, stream, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RESPONDED)
	upstreamEvent := events[len(events)-1]

	if upstreamEvent.RequestHeaders["Authorization"] != "[redacted]" {
		t.Fatalf("authorization header was not redacted: %v", upstreamEvent.RequestHeaders)
	}
	if upstreamEvent.RequestHeaders["X-Request-Trace"] != "visible" {
		t.Fatalf("visible request header missing: %v", upstreamEvent.RequestHeaders)
	}
	if upstreamEvent.ResponseHeaders["Set-Cookie"] != "[redacted]" {
		t.Fatalf("set-cookie header was not redacted: %v", upstreamEvent.ResponseHeaders)
	}
	if upstreamEvent.ResponseHeaders["X-Upstream-Trace"] != "visible" {
		t.Fatalf("visible response header missing: %v", upstreamEvent.ResponseHeaders)
	}
	if upstreamEvent.RequestBytes != 0 || upstreamEvent.ResponseBytes != 0 || upstreamEvent.DebugAttributes != nil {
		t.Fatalf("headers level should not include debug fields: %+v", upstreamEvent)
	}
}

func TestTrafficTraceDisabledDoesNotEmitEvents(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("disabled ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	setTrafficTraceSettings(t, client, cookie, false, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC)

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	resp, err := http.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + "/disabled")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	_ = mustReadResponseBody(t, resp)

	settings := getTrafficTraceSettings(t, client, cookie)
	if settings.EmittedEvents != 0 {
		t.Fatalf("disabled tracing emitted %d events", settings.EmittedEvents)
	}
}

func getTrafficTraceSettings(
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
) *p2pstreamv1.TrafficTraceSettings {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.GetTrafficTraceSettingsRequest{})
	req.Header().Set("Cookie", cookie)
	resp, err := client.GetTrafficTraceSettings(context.Background(), req)
	if err != nil {
		t.Fatalf("get trace settings: %v", err)
	}
	if resp.Msg.Settings == nil {
		t.Fatal("missing trace settings")
	}
	return resp.Msg.Settings
}

func setTrafficTraceSettings(
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	enabled bool,
	level p2pstreamv1.TrafficTraceLevel,
) *p2pstreamv1.TrafficTraceSettings {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.SetTrafficTraceSettingsRequest{
		Enabled: enabled,
		Level:   level,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.SetTrafficTraceSettings(context.Background(), req)
	if err != nil {
		t.Fatalf("set trace settings: %v", err)
	}
	if resp.Msg.Settings == nil {
		t.Fatal("missing trace settings")
	}
	return resp.Msg.Settings
}

func openTrafficTraceStream(
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	replayRecent bool,
	afterSequence uint64,
	timeout time.Duration,
) (*connect.ServerStreamForClient[p2pstreamv1.StreamTrafficTraceEventsResponse], context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	req := connect.NewRequest(&p2pstreamv1.StreamTrafficTraceEventsRequest{
		ReplayRecent:  replayRecent,
		AfterSequence: afterSequence,
	})
	req.Header().Set("Cookie", cookie)
	stream, err := client.StreamTrafficTraceEvents(ctx, req)
	if err != nil {
		cancel()
		t.Fatalf("open trace stream: %v", err)
	}
	return stream, cancel
}

func receiveTrafficTraceResponse(
	t *testing.T,
	stream *connect.ServerStreamForClient[p2pstreamv1.StreamTrafficTraceEventsResponse],
) *p2pstreamv1.StreamTrafficTraceEventsResponse {
	t.Helper()
	if !stream.Receive() {
		t.Fatalf("receive trace response: %v", stream.Err())
	}
	return stream.Msg()
}

func receiveTrafficTraceSettingsUntil(
	t *testing.T,
	stream *connect.ServerStreamForClient[p2pstreamv1.StreamTrafficTraceEventsResponse],
	count int64,
) *p2pstreamv1.TrafficTraceSettings {
	t.Helper()
	for stream.Receive() {
		msg := stream.Msg()
		if msg.Settings != nil && msg.Settings.SubscriberCount == count {
			return msg.Settings
		}
	}
	t.Fatalf("trace stream ended before subscriber count %d: %v", count, stream.Err())
	return nil
}

func waitTrafficTraceSubscriberCount(
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	count int64,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		settings := getTrafficTraceSettings(t, client, cookie)
		if settings.SubscriberCount == count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	settings := getTrafficTraceSettings(t, client, cookie)
	t.Fatalf("subscriber count = %d, want %d", settings.SubscriberCount, count)
}

func collectTrafficTraceEventsUntil(
	t *testing.T,
	stream *connect.ServerStreamForClient[p2pstreamv1.StreamTrafficTraceEventsResponse],
	stage p2pstreamv1.TrafficTraceStage,
) []*p2pstreamv1.TrafficTraceEvent {
	t.Helper()
	var events []*p2pstreamv1.TrafficTraceEvent
	for stream.Receive() {
		msg := stream.Msg()
		if msg.Event == nil {
			continue
		}
		events = append(events, msg.Event)
		if msg.Event.Stage == stage {
			return events
		}
	}
	t.Fatalf("trace stream ended before stage %s: %v", stage, stream.Err())
	return nil
}

func assertTrafficTraceStages(t *testing.T, events []*p2pstreamv1.TrafficTraceEvent, stages ...p2pstreamv1.TrafficTraceStage) {
	t.Helper()
	if len(events) < len(stages) {
		t.Fatalf("got %d trace events, want at least %d: %+v", len(events), len(stages), events)
	}
	for idx, stage := range stages {
		if events[idx].Stage != stage {
			t.Fatalf("event %d stage = %s, want %s; events=%+v", idx, events[idx].Stage, stage, events)
		}
	}
}
