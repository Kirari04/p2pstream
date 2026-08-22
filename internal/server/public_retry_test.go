package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

type retryRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn retryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type retryTrackingReadCloser struct {
	io.Reader
	closed bool
}

func (b *retryTrackingReadCloser) Close() error {
	b.closed = true
	return nil
}

func newPublicRetryTestApp(t *testing.T) (*App, *publicProxySnapshot, publicRouteTargetConfig, *AgentConn, *AgentConn) {
	t.Helper()
	app := NewApp(nil, nil)
	first := &AgentConn{AgentID: 1, PublicID: "agent-one", Name: "Agent one", Done: make(chan struct{})}
	second := &AgentConn{AgentID: 2, PublicID: "agent-two", Name: "Agent two", Done: make(chan struct{})}
	if err := app.AgentHub.connect(first); err != nil {
		t.Fatalf("connect first agent: %v", err)
	}
	if err := app.AgentHub.connect(second); err != nil {
		t.Fatalf("connect second agent: %v", err)
	}
	origin, err := url.Parse("http://origin.internal:8080")
	if err != nil {
		t.Fatalf("parse origin: %v", err)
	}
	target := publicRouteTargetConfig{
		ID: 10, Name: "pool", TargetType: publicRouteTargetTypeProxy,
		Transport: publicRouteTargetTransportAgent, ParsedURL: origin,
		AgentSelector:      publicAgentSelectorConfig{MatchLabels: map[string]string{"pool": "blue"}},
		AgentLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
	}
	snapshot := &publicProxySnapshot{Agents: map[int64]publicAgentConfig{
		1: {ID: 1, PublicID: first.PublicID, Enabled: true, Labels: map[string]string{"pool": "blue"}},
		2: {ID: 2, PublicID: second.PublicID, Enabled: true, Labels: map[string]string{"pool": "blue"}},
	}}
	return app, snapshot, target, first, second
}

func TestPublicAgentRetryUsesDifferentAgentAfterDialFailure(t *testing.T) {
	app, snapshot, target, first, second := newPublicRetryTestApp(t)
	var attempts []int64
	result := &publicRetryAttemptResult{}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule:      &publicRetryRuleConfig{ID: 1, Name: "reads", MaxRetries: 1, FailureMode: publicRetryFailureModeConnectionFailures, BodyMode: publicRetryBodyModeNever},
		requestID: "request-1", result: result,
		transportForAgent: func(agent *AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts = append(attempts, agent.AgentID)
				if agent.AgentID == first.AgentID {
					return nil, agentDialError{Kind: "dial_timeout", Err: "vpn unavailable"}
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read retried body: %v", err)
				}
				if string(body) != "payload" {
					t.Fatalf("retried body = %q, want payload", body)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
			})
		},
	}
	requestBody := &retryTrackingReadCloser{Reader: strings.NewReader("payload")}
	req, err := http.NewRequest(http.MethodPost, "http://proxy.test/upload", requestBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// A non-nil client request body may legally use zero to mean unknown length.
	req.ContentLength = 0
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if first.ActiveRequests.Load() != 0 || second.ActiveRequests.Load() != 1 {
		t.Fatalf("active requests before response close = [%d %d], want [0 1]", first.ActiveRequests.Load(), second.ActiveRequests.Load())
	}
	if requestBody.closed {
		t.Fatal("request body closed before the successful response body")
	}
	_ = resp.Body.Close()
	if first.ActiveRequests.Load() != 0 || second.ActiveRequests.Load() != 0 {
		t.Fatalf("active requests after response close = [%d %d], want [0 0]", first.ActiveRequests.Load(), second.ActiveRequests.Load())
	}
	if !requestBody.closed {
		t.Fatal("request body remains open after the successful response body closed")
	}
	if len(attempts) != 2 || attempts[0] != first.AgentID || attempts[1] != second.AgentID {
		t.Fatalf("attempted agents = %v, want [%d %d]", attempts, first.AgentID, second.AgentID)
	}
	if result.RetryCount != 1 || result.Outcome != publicRetryOutcomeRecovered || result.FinalAgent != second || result.FirstFailedAgent != first {
		t.Fatalf("retry result = %+v", result)
	}
}

func TestPreparePublicRetryRequestBodyReservesKnownBodySize(t *testing.T) {
	app := NewApp(nil, nil)
	app.retryReplayBudget = newRetryReplayBudget(1024)
	rule := &publicRetryRuleConfig{
		BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: 4096,
	}
	req, err := http.NewRequest(http.MethodPost, "http://proxy.test/upload", strings.NewReader(strings.Repeat("x", 512)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	body, err := preparePublicRetryRequestBody(app, req, rule)
	if err != nil {
		t.Fatalf("prepare retry body: %v", err)
	}
	defer body.close()
	if !body.replayable || len(body.buffered) != 512 {
		t.Fatalf("prepared body replayable=%t bytes=%d, want true and 512", body.replayable, len(body.buffered))
	}
	if got := app.retryReplayBudget.used.Load(); got != 512 {
		t.Fatalf("replay reservation = %d, want 512", got)
	}
}

func TestPublicAgentRetryBuffersBodyForPreResponseFailure(t *testing.T) {
	app, snapshot, target, first, second := newPublicRetryTestApp(t)
	payload := "mutation-payload"
	readBodies := make(map[int64]string)
	result := &publicRetryAttemptResult{}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 2, Name: "mutations", MaxRetries: 1,
			FailureMode: publicRetryFailureModePreResponseFailures,
			BodyMode:    publicRetryBodyModeBuffered, MaxReplayBodyBytes: 1024,
		},
		requestID: "request-2", result: result,
		transportForAgent: func(agent *AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read attempt body: %v", err)
				}
				readBodies[agent.AgentID] = string(body)
				if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				if agent.AgentID == first.AgentID {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodPost, "http://proxy.test/mutate", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_ = resp.Body.Close()
	if readBodies[first.AgentID] != payload || readBodies[second.AgentID] != payload {
		t.Fatalf("attempt bodies = %#v, want identical payloads", readBodies)
	}
	if result.RetryCount != 1 || result.Outcome != publicRetryOutcomeRecovered {
		t.Fatalf("retry result = %+v", result)
	}
}

func TestPublicAgentRetrySkipsConsumedUnbufferedBody(t *testing.T) {
	app, snapshot, target, first, _ := newPublicRetryTestApp(t)
	attempts := 0
	result := &publicRetryAttemptResult{}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule:      &publicRetryRuleConfig{ID: 3, Name: "unsafe", MaxRetries: 1, FailureMode: publicRetryFailureModePreResponseFailures, BodyMode: publicRetryBodyModeNever},
		requestID: "request-3", result: result,
		transportForAgent: func(agent *AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				_, _ = io.ReadAll(req.Body)
				if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				return nil, io.ErrUnexpectedEOF
			})
		},
	}
	req, err := http.NewRequest(http.MethodPost, "http://proxy.test/mutate", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := rt.RoundTrip(req); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("round trip error = %v, want unexpected EOF", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if result.RetryCount != 0 || result.Outcome != publicRetryOutcomeSkipped || result.ReplaySkippedReason != "request_already_written" {
		t.Fatalf("retry result = %+v", result)
	}
}

func TestValidatePublicRetryRuleRequiresExplicitDuplicateRiskAcknowledgement(t *testing.T) {
	app := NewApp(nil, nil)
	_, err := app.validatePublicRetryRuleInput(
		context.Background(), "mutations", 100, true, []string{http.MethodPost}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED,
		1024, nil, nil, nil, false,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("validation error = %v, want invalid argument", err)
	}

	if _, err := app.validatePublicRetryRuleInput(
		context.Background(), "mutations", 100, true, []string{http.MethodPost}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED,
		1024, nil, nil, nil, true,
	); err != nil {
		t.Fatalf("acknowledged validation: %v", err)
	}
}

func TestPublicRetryManagementAPICreateUpdateDeleteReadback(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "retry-management.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	app := NewApp(&config.Config{PublicCacheDir: filepath.Join(t.TempDir(), "cache")}, database)
	header := createTestAdminSession(t, app)

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicRetryRuleRequest{
		Name: "vpn-reads", Priority: 20, Enabled: true, Methods: []string{http.MethodGet, http.MethodHead},
		MaxRetries:  1,
		FailureMode: p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		BodyMode:    p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER,
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	created, err := app.CreatePublicRetryRule(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create retry rule: %v", err)
	}
	if created.Msg.Rule == nil || created.Msg.Rule.Name != "vpn-reads" || created.Msg.Rule.MaxRetries != 1 {
		t.Fatalf("create readback = %+v", created.Msg.Rule)
	}
	if snap := app.currentPublicSnapshot(); snap == nil || len(snap.RetryRules) != 1 || snap.RetryRules[0].Name != "vpn-reads" {
		t.Fatalf("snapshot after create = %+v", snap)
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicRetryRuleRequest{
		Id: created.Msg.Rule.Id, Name: "vpn-mutations", Priority: 10, Enabled: true,
		Methods: []string{http.MethodPost}, MaxRetries: 2,
		FailureMode:               p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES,
		BodyMode:                  p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED,
		MaxReplayBodyBytes:        2048,
		DuplicateRiskAcknowledged: true,
	})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	updated, err := app.UpdatePublicRetryRule(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update retry rule: %v", err)
	}
	if updated.Msg.Rule.Name != "vpn-mutations" || updated.Msg.Rule.MaxRetries != 2 || updated.Msg.Rule.MaxReplayBodyBytes != 2048 {
		t.Fatalf("update readback = %+v", updated.Msg.Rule)
	}

	deleteReq := connect.NewRequest(&p2pstreamv1.DeletePublicRetryRuleRequest{Id: created.Msg.Rule.Id})
	deleteReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.DeletePublicRetryRule(context.Background(), deleteReq); err != nil {
		t.Fatalf("delete retry rule: %v", err)
	}
	rows, err := database.ListPublicRetryRules(context.Background())
	if err != nil {
		t.Fatalf("list retry rules: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("retry rules after delete = %d, want 0", len(rows))
	}
}
