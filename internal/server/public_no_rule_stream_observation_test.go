package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type noRuleTerminalBody struct {
	prefix    []byte
	firstErr  error
	laterErr  error
	firstDone bool
	closeErr  error
	closes    int
}

func (b *noRuleTerminalBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := copy(p, b.prefix)
	b.prefix = b.prefix[n:]
	if len(b.prefix) > 0 {
		return n, nil
	}
	if !b.firstDone {
		b.firstDone = true
		return n, b.firstErr
	}
	return n, b.laterErr
}

func (b *noRuleTerminalBody) Close() error {
	b.closes++
	return b.closeErr
}

func assertNoRuleRetryMetadata(t *testing.T, result *publicRetryAttemptResult) {
	t.Helper()
	if result.Rule != nil || result.RetryCount != 0 || result.Outcome != "" || result.FirstErrorKind != "" || result.LastErrorKind != "" || result.FirstFailedAgent != nil || result.ReplaySkippedReason != "" {
		t.Fatalf("ordinary response changed retry metadata: %+v", result)
	}
}

func TestPublicAgentNoRuleStreamingFailurePreservesResponseAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind string
	}{
		{"truncated", io.ErrUnexpectedEOF, "upstream_response_body_truncated"},
		{"agent-disconnected", fmt.Errorf("lane ended: %w", errAgentDisconnected), "agent_disconnected_during_response"},
		{"timeout", context.DeadlineExceeded, "upstream_response_body_timeout"},
		{"other", errors.New("source failed"), "upstream_response_body_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, snapshot, target, first, second := newPublicRetryTestApp(t)
			target.Enabled = true
			target.HealthCheck.Enabled = true
			snapshot.RouteTargets = map[int64]publicRouteTargetConfig{target.ID: target}
			app.TargetHealth.reconcile(app, snapshot, false)
			before, _ := app.TargetHealth.listHealthTraces(target.ID, first.AgentID, 10, false)
			available := app.TargetHealth.agentAvailable(target.ID, first.AgentID)
			body := &noRuleTerminalBody{prefix: []byte("committed-prefix"), firstErr: tc.err, laterErr: errAgentDisconnected}
			requestBody := &retryTrackingReadCloser{Reader: strings.NewReader("one-mutation")}
			result := &publicRetryAttemptResult{}
			attempts := 0
			rt := &publicAgentAttemptRoundTripper{
				app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
				result: result, requestID: "ordinary-body-failure",
				transportForAgent: func(agent *AgentConn) http.RoundTripper {
					return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
						attempts++
						if agent != first {
							t.Fatal("ordinary body failure tried another agent")
						}
						payload, err := io.ReadAll(req.Body)
						if err != nil || string(payload) != "one-mutation" {
							t.Fatalf("origin request = %q, %v", payload, err)
						}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, ContentLength: 64, Request: req}, nil
					})
				},
			}
			req := httptest.NewRequest(http.MethodPost, "http://public.test/ordinary", requestBody)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			if resp.StatusCode != http.StatusOK || first.ActiveRequests.Load() != 1 || result.TerminalErrorKind != "" || body.firstDone {
				t.Fatalf("before body read: status=%d active=%d result=%+v", resp.StatusCode, first.ActiveRequests.Load(), result)
			}
			var buffer [64]byte
			n, readErr := resp.Body.Read(buffer[:])
			if string(buffer[:n]) != "committed-prefix" || !errors.Is(readErr, tc.err) {
				t.Fatalf("first read = %q, %v; want unchanged prefix and source error", buffer[:n], readErr)
			}
			if result.TerminalErrorKind != tc.kind {
				t.Fatalf("terminal error kind = %q, want %q", result.TerminalErrorKind, tc.kind)
			}
			if _, err := resp.Body.Read(buffer[:]); !errors.Is(err, errAgentDisconnected) {
				t.Fatalf("repeated read error = %v", err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if result.TerminalErrorKind != tc.kind || attempts != 1 || result.FinalAgent != first {
				t.Fatalf("terminal attribution/attempts changed: attempts=%d result=%+v", attempts, result)
			}
			assertNoRuleRetryMetadata(t, result)
			after, _ := app.TargetHealth.listHealthTraces(target.ID, first.AgentID, 10, false)
			if len(after) != len(before) || app.TargetHealth.agentAvailable(target.ID, first.AgentID) != available {
				t.Fatal("ordinary body observation changed passive health")
			}
			if first.ActiveRequests.Load() != 0 || second.ActiveRequests.Load() != 0 || app.retryReplayBudget.used.Load() != 0 || !requestBody.closed {
				t.Fatalf("terminal owners: active=%d/%d quota=%d requestClosed=%t", first.ActiveRequests.Load(), second.ActiveRequests.Load(), app.retryReplayBudget.used.Load(), requestBody.closed)
			}
		})
	}
}

func TestPublicAgentNoRuleStreamingTerminalExclusions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		cancel bool
	}{
		{"eof-with-data", io.EOF, false},
		{"canceled-error", context.Canceled, false},
		{"wrapped-canceled-error", fmt.Errorf("read: %w", context.Canceled), false},
		{"canceled-request", io.ErrUnexpectedEOF, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			req := httptest.NewRequest(http.MethodGet, "http://public.test/ordinary", nil).WithContext(ctx)
			result := &publicRetryAttemptResult{}
			body := &noRuleTerminalBody{prefix: []byte("prefix"), firstErr: tc.err, laterErr: tc.err}
			resp := &http.Response{StatusCode: http.StatusOK, Body: body}
			rt := &publicAgentAttemptRoundTripper{result: result}
			rt.observeStreamingResponseBody(req, resp, nil, 1, "response_body_streamed")
			var buffer [16]byte
			n, err := resp.Body.Read(buffer[:])
			if string(buffer[:n]) != "prefix" || !errors.Is(err, tc.err) {
				t.Fatalf("excluded terminal read = %q, %v", buffer[:n], err)
			}
			_ = resp.Body.Close()
			if result.TerminalErrorKind != "" || body.closes != 1 {
				t.Fatalf("excluded result=%+v closes=%d", result, body.closes)
			}
			assertNoRuleRetryMetadata(t, result)
		})
	}
}

func TestPublicObservedResponseBodyCallsErrorOnceAndPreservesClose(t *testing.T) {
	closeErr := errors.New("close failed")
	body := &noRuleTerminalBody{prefix: []byte("prefix"), firstErr: io.ErrUnexpectedEOF, laterErr: errAgentDisconnected, closeErr: closeErr}
	calls := 0
	observed := &publicRetryObservedResponseBody{ReadCloser: body, onError: func(err error) {
		calls++
		if err != io.ErrUnexpectedEOF {
			t.Fatalf("callback error = %v", err)
		}
	}}
	var buffer [16]byte
	if n, err := observed.Read(buffer[:]); string(buffer[:n]) != "prefix" || err != io.ErrUnexpectedEOF {
		t.Fatalf("first observed read = %q, %v", buffer[:n], err)
	}
	if _, err := observed.Read(buffer[:]); err != errAgentDisconnected {
		t.Fatalf("repeated observed read = %v", err)
	}
	for range 2 {
		if err := observed.Close(); err != closeErr {
			t.Fatalf("close error = %v", err)
		}
	}
	if calls != 1 || body.closes != 2 {
		t.Fatalf("callback/underlying closes = %d/%d, want 1/2", calls, body.closes)
	}
}

func TestPublicAgentNoRuleUpgradeRetainsOriginalWritableBody(t *testing.T) {
	body := &retryTrackingReadWriteCloser{Reader: strings.NewReader("pong")}
	result := &publicRetryAttemptResult{}
	resp := &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: body}
	rt := &publicAgentAttemptRoundTripper{result: result}
	rt.observeStreamingResponseBody(httptest.NewRequest(http.MethodGet, "http://public.test/ws", nil), resp, nil, 1, "response_body_streamed")
	if resp.Body != body {
		t.Fatalf("upgrade body identity changed: %T", resp.Body)
	}
	upgraded, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("upgrade body %T lost io.ReadWriteCloser", resp.Body)
	}
	if _, err := upgraded.Write([]byte("ping")); err != nil || body.writes.String() != "ping" {
		t.Fatalf("upgrade write = %q, %v", body.writes.String(), err)
	}
	if err := upgraded.Close(); err != nil || !body.closed {
		t.Fatalf("upgrade close = %v, closed=%t", err, body.closed)
	}
	assertNoRuleRetryMetadata(t, result)
	if result.TerminalErrorKind != "" {
		t.Fatalf("upgrade unexpectedly observed: %+v", result)
	}
}

func TestPublicAgentNoRuleProxyCommitsPrefixAndRecordsTruncation(t *testing.T) {
	prefix := bytes.Repeat([]byte("ordinary-prefix\n"), 32)
	var origins atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(prefix)+1))
		_, _ = w.Write(prefix)
		w.(http.Flusher).Flush()
		// Return one byte short after flushing. This exercises a committed body
		// failure without reconnecting agents or relying on sleeps/timeouts.
	}))
	t.Cleanup(origin.Close)
	database := newServerTestDB(t)
	dbAgent := createAgentRegistryTestAgent(t, database, "ordinary-observation", "ordinary-observation", "secret")
	app, target, agent, _ := newAgentProxyTunnelTestApp(t, dbAgent.ID, origin.URL, 2*time.Second)
	app.DB = database
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.CloseObservabilityRecorder(ctx)
	})
	handlerDone := make(chan struct{})
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		app.proxyAgentTargetRequest(w, r, publicRouteResolution{Target: target, Agent: agent}, nil, nil, nil, proxyRequestObservability{})
	}))
	t.Cleanup(public.Close)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(client.CloseIdleConnections)
	resp, err := client.Get(public.URL + "/ordinary")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, prefix) || !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("public response = %d %q %v; want committed200/exact prefix/unexpectedEOF", resp.StatusCode, body, readErr)
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("public handler did not join")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := app.FlushObservabilityRecorder(ctx); err != nil {
		t.Fatal(err)
	}
	var count, status, attempts, agentID int64
	var kind, outcome, retryKind string
	var retryRule, failedAgent sql.NullInt64
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*),status_code,error_kind,agent_id,retry_rule_id,retry_failed_agent_id,retry_count,retry_outcome,retry_error_kind FROM proxy_request_events`).Scan(&count, &status, &kind, &agentID, &retryRule, &failedAgent, &attempts, &outcome, &retryKind); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != http.StatusOK || agentID != dbAgent.ID || kind != "upstream_response_body_truncated" {
		t.Fatalf("event = count%d status%d agent%d error%q; want1/200/%d/upstream_response_body_truncated", count, status, agentID, kind, dbAgent.ID)
	}
	if origins.Load() != 1 || attempts != 0 || retryRule.Valid || failedAgent.Valid || outcome != "" || retryKind != "" {
		t.Fatalf("origin/retry semantics = origins%d count%d rule%v failedAgent%v outcome%q error%q", origins.Load(), attempts, retryRule, failedAgent, outcome, retryKind)
	}
	traces, _ := app.TargetHealth.listHealthTraces(target.ID, agent.AgentID, 10, false)
	if len(traces) != 0 || !app.TargetHealth.agentAvailable(target.ID, agent.AgentID) || agent.ActiveRequests.Load() != 0 || app.retryReplayBudget.used.Load() != 0 {
		t.Fatalf("ordinary terminal health/owners: traces%d active%d quota%d", len(traces), agent.ActiveRequests.Load(), app.retryReplayBudget.used.Load())
	}
}
