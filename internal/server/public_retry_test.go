package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

type retryTrackingReadWriteCloser struct {
	io.Reader
	writes strings.Builder
	closed bool
}

type retryErrorAfterReadCloser struct {
	reader io.Reader
	err    error
	closed bool
}

func (b *retryErrorAfterReadCloser) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF && b.err != nil {
		failure := b.err
		b.err = nil
		return n, failure
	}
	return n, err
}

func (b *retryErrorAfterReadCloser) Close() error {
	b.closed = true
	return nil
}

func (b *retryTrackingReadWriteCloser) Write(p []byte) (int, error) {
	return b.writes.Write(p)
}

func (b *retryTrackingReadWriteCloser) Close() error {
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

func TestPublicAgentRetryUsesDifferentAgentAfterRetryableStatus(t *testing.T) {
	app, snapshot, target, first, second := newPublicRetryTestApp(t)
	var attempts []int64
	firstBody := &retryTrackingReadCloser{Reader: strings.NewReader("temporary failure")}
	result := &publicRetryAttemptResult{}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 4, Name: "gateway-errors", MaxRetries: 1,
			FailureMode: publicRetryFailureModeConnectionFailures, BodyMode: publicRetryBodyModeNever,
			RetryStatusCodes: []int64{http.StatusServiceUnavailable}, retryStatusCodeSet: map[int]struct{}{http.StatusServiceUnavailable: {}},
		},
		requestID: "request-status", result: result,
		transportForAgent: func(agent *AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts = append(attempts, agent.AgentID)
				if agent.AgentID == first.AgentID {
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: firstBody, Header: make(http.Header), Request: req}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("recovered")), Header: make(http.Header), Request: req}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/app.js", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read recovered response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "recovered" {
		t.Fatalf("response = %d %q, want 200 recovered", resp.StatusCode, body)
	}
	if !firstBody.closed {
		t.Fatal("retryable response body was not closed before the next attempt")
	}
	if len(attempts) != 2 || attempts[0] != first.AgentID || attempts[1] != second.AgentID {
		t.Fatalf("attempted agents = %v, want [%d %d]", attempts, first.AgentID, second.AgentID)
	}
	if result.RetryCount != 1 || result.Outcome != publicRetryOutcomeRecovered || result.FirstErrorKind != "upstream_status_503" || result.FirstFailedAgent != first || result.FinalAgent != second {
		t.Fatalf("retry result = %+v", result)
	}
	if first.ActiveRequests.Load() != 0 || second.ActiveRequests.Load() != 0 {
		t.Fatalf("active requests after close = [%d %d], want [0 0]", first.ActiveRequests.Load(), second.ActiveRequests.Load())
	}
}

func TestPublicAgentRetryReturnsFinalRetryableStatusWhenExhausted(t *testing.T) {
	app, snapshot, target, first, second := newPublicRetryTestApp(t)
	result := &publicRetryAttemptResult{}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 5, Name: "gateway-errors", MaxRetries: 1,
			FailureMode: publicRetryFailureModeConnectionFailures, BodyMode: publicRetryBodyModeNever,
			RetryStatusCodes: []int64{http.StatusBadGateway}, retryStatusCodeSet: map[int]struct{}{http.StatusBadGateway: {}},
		},
		requestID: "request-status-exhausted", result: result,
		transportForAgent: func(agent *AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadGateway,
					Body:       io.NopCloser(strings.NewReader(agent.PublicID)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/app.js", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read exhausted response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || string(body) != second.PublicID {
		t.Fatalf("final response = %d %q, want second agent's 502", resp.StatusCode, body)
	}
	if result.RetryCount != 1 || result.Outcome != publicRetryOutcomeExhausted || result.FirstErrorKind != "upstream_status_502" || result.FirstFailedAgent != first || result.FinalAgent != second {
		t.Fatalf("retry result = %+v", result)
	}
}

func TestPublicAgentRetryRecoversTruncatedCompressedResponseBeforeDelivery(t *testing.T) {
	app, snapshot, target, first, second := newPublicRetryTestApp(t)
	var encoded bytes.Buffer
	compressor := gzip.NewWriter(&encoded)
	plain := []byte(strings.Repeat("general response payload\n", 32))
	if _, err := compressor.Write(plain); err != nil {
		t.Fatalf("compress response: %v", err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatalf("close compressor: %v", err)
	}
	compressed := encoded.Bytes()
	partial := append([]byte(nil), compressed[:len(compressed)/2]...)
	firstBody := &retryErrorAfterReadCloser{reader: bytes.NewReader(partial), err: io.ErrUnexpectedEOF}
	result := &publicRetryAttemptResult{}
	var attempts []int64
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 7, Name: "completed-responses", MaxRetries: 1,
			FailureMode:                  publicRetryFailureModePreResponseFailures,
			BodyMode:                     publicRetryBodyModeNever,
			ResponseBodyMode:             publicRetryResponseBodyModeBuffered,
			MaxBufferedResponseBodyBytes: 4096,
		},
		requestID: "request-response-body", result: result,
		transportForAgent: func(agent *AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts = append(attempts, agent.AgentID)
				body := io.ReadCloser(io.NopCloser(bytes.NewReader(compressed)))
				if agent.AgentID == first.AgentID {
					body = firstBody
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type":     []string{"application/octet-stream"},
						"Content-Encoding": []string{"gzip"},
					},
					ContentLength: int64(len(compressed)),
					Body:          body,
					Request:       req,
				}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/download", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if first.ActiveRequests.Load() != 0 || second.ActiveRequests.Load() != 0 {
		t.Fatalf("active requests after completed buffering = [%d %d], want [0 0]", first.ActiveRequests.Load(), second.ActiveRequests.Load())
	}
	wireBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read protected response: %v", err)
	}
	if !bytes.Equal(wireBody, compressed) {
		t.Fatal("protected response did not preserve the complete encoded wire body")
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("content encoding = %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(bytes.NewReader(wireBody))
	if err != nil {
		t.Fatalf("open recovered gzip response: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decode recovered gzip response: %v", err)
	}
	_ = reader.Close()
	if !bytes.Equal(decoded, plain) {
		t.Fatal("decoded recovered response differs from the upstream payload")
	}
	if !firstBody.closed {
		t.Fatal("failed response body was not closed before retry")
	}
	if len(attempts) != 2 || attempts[0] != first.AgentID || attempts[1] != second.AgentID {
		t.Fatalf("attempted agents = %v, want [%d %d]", attempts, first.AgentID, second.AgentID)
	}
	if result.RetryCount != 1 || result.Outcome != publicRetryOutcomeRecovered || result.FirstErrorKind != "upstream_response_body_truncated" || result.TerminalErrorKind != "" || result.FirstFailedAgent != first || result.FinalAgent != second {
		t.Fatalf("retry result = %+v", result)
	}
	if got := app.retryReplayBudget.used.Load(); got != int64(len(compressed)) {
		t.Fatalf("response buffer reservation = %d, want %d until client close", got, len(compressed))
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}
	if got := app.retryReplayBudget.used.Load(); got != 0 {
		t.Fatalf("response buffer reservation after close = %d, want 0", got)
	}
}

func TestPublicAgentRetryRecoversUnknownLengthAgentDisconnect(t *testing.T) {
	app, snapshot, target, first, second := newPublicRetryTestApp(t)
	result := &publicRetryAttemptResult{}
	var attempts []int64
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 11, Name: "unknown-length", MaxRetries: 1,
			FailureMode: publicRetryFailureModePreResponseFailures, BodyMode: publicRetryBodyModeNever,
			ResponseBodyMode: publicRetryResponseBodyModeBuffered, MaxBufferedResponseBodyBytes: 1024,
		},
		requestID: "request-unknown-length", result: result,
		transportForAgent: func(agent *AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts = append(attempts, agent.AgentID)
				body := io.ReadCloser(io.NopCloser(strings.NewReader("complete response")))
				if agent.AgentID == first.AgentID {
					body = &retryErrorAfterReadCloser{reader: strings.NewReader("partial"), err: errors.Join(errAgentDisconnected, io.EOF)}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1, Body: body, Request: req}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/close-delimited", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read recovered response: %v", err)
	}
	_ = resp.Body.Close()
	if string(body) != "complete response" || len(attempts) != 2 || attempts[0] != first.AgentID || attempts[1] != second.AgentID {
		t.Fatalf("recovered response/attempts = %q/%v", body, attempts)
	}
	if result.Outcome != publicRetryOutcomeRecovered || result.FirstErrorKind != "agent_disconnected_during_response" {
		t.Fatalf("retry result = %+v", result)
	}
}

func TestPreparePublicRetryResponseBodyFallsBackAfterCompletionWait(t *testing.T) {
	app := NewApp(nil, nil)
	app.retryReplayBudget = newRetryReplayBudget(1024)
	rule := &publicRetryRuleConfig{
		ResponseBodyMode: publicRetryResponseBodyModeBuffered, MaxBufferedResponseBodyBytes: 1024,
		MaxBufferedResponseWait: 20 * time.Millisecond,
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/slow-stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	reader, writer := io.Pipe()
	releaseRemainder := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		if _, err := writer.Write([]byte("prefix-")); err != nil {
			writerDone <- err
			return
		}
		<-releaseRemainder
		if _, err := writer.Write([]byte("remainder")); err != nil {
			writerDone <- err
			return
		}
		writerDone <- writer.Close()
	}()
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1, Body: reader, Request: req}
	started := time.Now()
	prepared, err := preparePublicRetryResponseBody(app, req, resp, rule)
	if err != nil {
		t.Fatalf("prepare slow response: %v", err)
	}
	if prepared.complete || prepared.skipReason != "response_buffer_wait_exceeded" {
		t.Fatalf("prepared response = %+v, want timed streaming fallback", prepared)
	}
	if elapsed := time.Since(started); elapsed < rule.MaxBufferedResponseWait || elapsed > time.Second {
		t.Fatalf("buffer wait elapsed = %v, want bounded near %v", elapsed, rule.MaxBufferedResponseWait)
	}
	close(releaseRemainder)
	body, err := io.ReadAll(prepared.body)
	if err != nil {
		t.Fatalf("read fallback response: %v", err)
	}
	if string(body) != "prefix-remainder" {
		t.Fatalf("fallback response = %q, want complete body", body)
	}
	if err := <-writerDone; err != nil {
		t.Fatalf("write fallback response: %v", err)
	}
	if err := prepared.body.Close(); err != nil {
		t.Fatalf("close fallback response: %v", err)
	}
	if got := app.retryReplayBudget.used.Load(); got != 0 {
		t.Fatalf("response buffer reservation after close = %d, want 0", got)
	}
}

func TestPublicAgentRetryStreamsWhenResponseBufferBudgetIsExhausted(t *testing.T) {
	app, snapshot, target, first, _ := newPublicRetryTestApp(t)
	app.retryReplayBudget = newRetryReplayBudget(1024)
	releaseHeld, ok := app.retryReplayBudget.tryAcquire(1024)
	if !ok {
		t.Fatal("reserve response replay budget")
	}
	defer releaseHeld()
	result := &publicRetryAttemptResult{}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 12, Name: "budget-pressure", MaxRetries: 1,
			FailureMode: publicRetryFailureModePreResponseFailures, BodyMode: publicRetryBodyModeNever,
			ResponseBodyMode: publicRetryResponseBodyModeBuffered, MaxBufferedResponseBodyBytes: 1024,
		},
		requestID: "request-budget-pressure", result: result,
		transportForAgent: func(*AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: int64(len("stream safely")),
					Body: io.NopCloser(strings.NewReader("stream safely")), Request: req,
				}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/budget-pressure", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read streaming fallback: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close streaming fallback: %v", err)
	}
	if string(body) != "stream safely" {
		t.Fatalf("streaming fallback body = %q", body)
	}
	if result.Outcome != publicRetryOutcomeSkipped || result.ReplaySkippedReason != "response_buffer_budget_exhausted" || result.RetryCount != 0 {
		t.Fatalf("budget fallback result = %+v", result)
	}
	if got := app.retryReplayBudget.used.Load(); got != 1024 {
		t.Fatalf("response replay budget after fallback = %d, want held reservation only", got)
	}
}

func TestPublicAgentRetryReturnsErrorWhenBufferedResponseRetriesAreExhausted(t *testing.T) {
	app, snapshot, target, first, second := newPublicRetryTestApp(t)
	result := &publicRetryAttemptResult{}
	attempts := 0
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 8, Name: "completed-responses", MaxRetries: 1,
			FailureMode: publicRetryFailureModePreResponseFailures,
			BodyMode:    publicRetryBodyModeNever, ResponseBodyMode: publicRetryResponseBodyModeBuffered,
			MaxBufferedResponseBodyBytes: 1024,
		},
		requestID: "request-response-exhausted", result: result,
		transportForAgent: func(*AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: 64,
					Body: &retryErrorAfterReadCloser{reader: strings.NewReader("partial"), err: io.ErrUnexpectedEOF}, Request: req,
				}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/download", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if resp != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("round trip = response %v, error %v; want no committed response and unexpected EOF", resp, err)
	}
	if attempts != 2 || result.Outcome != publicRetryOutcomeExhausted || result.RetryCount != 1 || result.TerminalErrorKind != "upstream_response_body_truncated" || result.FinalAgent != second {
		t.Fatalf("retry result after %d attempts = %+v", attempts, result)
	}
	if first.ActiveRequests.Load() != 0 || second.ActiveRequests.Load() != 0 || app.retryReplayBudget.used.Load() != 0 {
		t.Fatalf("resources leaked: active=[%d %d] budget=%d", first.ActiveRequests.Load(), second.ActiveRequests.Load(), app.retryReplayBudget.used.Load())
	}
}

func TestPublicAgentRetryStreamsOversizedResponseAndRecordsBodyFailure(t *testing.T) {
	app, snapshot, target, first, _ := newPublicRetryTestApp(t)
	result := &publicRetryAttemptResult{}
	attempts := 0
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 9, Name: "small-responses", MaxRetries: 1,
			FailureMode: publicRetryFailureModePreResponseFailures,
			BodyMode:    publicRetryBodyModeNever, ResponseBodyMode: publicRetryResponseBodyModeBuffered,
			MaxBufferedResponseBodyBytes: 4,
		},
		requestID: "request-response-too-large", result: result,
		transportForAgent: func(*AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: 64,
					Body: &retryErrorAfterReadCloser{reader: strings.NewReader("partial"), err: io.ErrUnexpectedEOF}, Request: req,
				}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/large", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip headers: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("streaming body error = %v, want unexpected EOF", err)
	}
	_ = resp.Body.Close()
	if attempts != 1 || result.Outcome != publicRetryOutcomeSkipped || result.ReplaySkippedReason != "response_body_too_large" || result.TerminalErrorKind != "upstream_response_body_truncated" {
		t.Fatalf("streaming retry result after %d attempts = %+v", attempts, result)
	}
	if first.ActiveRequests.Load() != 0 || app.retryReplayBudget.used.Load() != 0 {
		t.Fatalf("streaming resources leaked: active=%d budget=%d", first.ActiveRequests.Load(), app.retryReplayBudget.used.Load())
	}
}

func TestPublicAgentRetryDoesNotReplayConsumedRequestAfterResponseBodyFailure(t *testing.T) {
	app, snapshot, target, first, _ := newPublicRetryTestApp(t)
	result := &publicRetryAttemptResult{}
	attempts := 0
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 10, Name: "unsafe-response-replay", MaxRetries: 1,
			FailureMode: publicRetryFailureModePreResponseFailures,
			BodyMode:    publicRetryBodyModeNever, ResponseBodyMode: publicRetryResponseBodyModeBuffered,
			MaxBufferedResponseBodyBytes: 1024,
		},
		requestID: "request-response-consumed-post", result: result,
		transportForAgent: func(*AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				if _, err := io.ReadAll(req.Body); err != nil {
					t.Fatalf("read request body: %v", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: 64,
					Body: &retryErrorAfterReadCloser{reader: strings.NewReader("partial"), err: io.ErrUnexpectedEOF}, Request: req,
				}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodPost, "http://proxy.test/mutate", strings.NewReader("mutation"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if resp != nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("round trip = response %v, error %v; want no response and unexpected EOF", resp, err)
	}
	if attempts != 1 || result.Outcome != publicRetryOutcomeSkipped || result.ReplaySkippedReason != "request_body_not_replayable" {
		t.Fatalf("non-replayable result after %d attempts = %+v", attempts, result)
	}
}

func TestPreparePublicRetryResponseBodyStreamsUnknownOversizeWithoutLosingPrefix(t *testing.T) {
	app := NewApp(nil, nil)
	app.retryReplayBudget = newRetryReplayBudget(4)
	rule := &publicRetryRuleConfig{ResponseBodyMode: publicRetryResponseBodyModeBuffered, MaxBufferedResponseBodyBytes: 4}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/download", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp := &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1,
		Body: io.NopCloser(strings.NewReader("oversized")), Request: req,
	}
	prepared, err := preparePublicRetryResponseBody(app, req, resp, rule)
	if err != nil {
		t.Fatalf("prepare unknown response: %v", err)
	}
	if prepared.complete || prepared.skipReason != "response_body_too_large" {
		t.Fatalf("prepared response = %+v, want streaming oversize", prepared)
	}
	body, err := io.ReadAll(prepared.body)
	if err != nil {
		t.Fatalf("read reconstructed response: %v", err)
	}
	if string(body) != "oversized" {
		t.Fatalf("reconstructed response = %q, want oversized", body)
	}
	if got := app.retryReplayBudget.used.Load(); got != 4 {
		t.Fatalf("response prefix reservation = %d, want 4 until close", got)
	}
	if err := prepared.body.Close(); err != nil {
		t.Fatalf("close reconstructed response: %v", err)
	}
	if got := app.retryReplayBudget.used.Load(); got != 0 {
		t.Fatalf("response prefix reservation after close = %d, want 0", got)
	}
}

func TestPublicRetryResponseBufferExcludesStreamingProtocolsAndRanges(t *testing.T) {
	rule := &publicRetryRuleConfig{ResponseBodyMode: publicRetryResponseBodyModeBuffered, MaxBufferedResponseBodyBytes: 1024}
	tests := []struct {
		name        string
		method      string
		request     http.Header
		status      int
		response    http.Header
		contentType string
		want        string
	}{
		{name: "head", method: http.MethodHead, status: http.StatusOK, want: "response_body_absent"},
		{name: "request range", method: http.MethodGet, request: http.Header{"Range": []string{"bytes=0-10"}}, status: http.StatusOK, want: "response_range_streamed"},
		{name: "partial response", method: http.MethodGet, status: http.StatusPartialContent, want: "response_range_streamed"},
		{name: "upstream opt out", method: http.MethodGet, status: http.StatusOK, response: http.Header{"X-Accel-Buffering": []string{"no"}}, want: "response_buffering_disabled_by_upstream"},
		{name: "server sent events", method: http.MethodGet, status: http.StatusOK, contentType: "text/event-stream; charset=utf-8", want: "streaming_response_type"},
		{name: "grpc variant", method: http.MethodGet, status: http.StatusOK, contentType: "application/grpc+json", want: "streaming_response_type"},
		{name: "multipart stream", method: http.MethodGet, status: http.StatusOK, contentType: "multipart/x-mixed-replace; boundary=frame", want: "streaming_response_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{Method: tt.method, Header: tt.request}
			if req.Header == nil {
				req.Header = make(http.Header)
			}
			respHeader := tt.response
			if respHeader == nil {
				respHeader = make(http.Header)
			}
			if tt.contentType != "" {
				respHeader.Set("Content-Type", tt.contentType)
			}
			resp := &http.Response{StatusCode: tt.status, Header: respHeader, ContentLength: -1, Body: io.NopCloser(strings.NewReader("body"))}
			if got := publicRetryResponseBufferExclusion(req, resp, rule); got != tt.want {
				t.Fatalf("exclusion = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPublicAgentRetrySkipsStatusRetryForConsumedUnbufferedBody(t *testing.T) {
	app, snapshot, target, first, _ := newPublicRetryTestApp(t)
	attempts := 0
	result := &publicRetryAttemptResult{}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		rule: &publicRetryRuleConfig{
			ID: 6, Name: "post-status", MaxRetries: 1,
			FailureMode: publicRetryFailureModeConnectionFailures, BodyMode: publicRetryBodyModeNever,
			RetryStatusCodes: []int64{http.StatusServiceUnavailable}, retryStatusCodeSet: map[int]struct{}{http.StatusServiceUnavailable: {}},
		},
		requestID: "request-status-unbuffered", result: result,
		transportForAgent: func(*AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts++
				_, _ = io.ReadAll(req.Body)
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodPost, "http://proxy.test/mutate", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_ = resp.Body.Close()
	if attempts != 1 || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts/status = %d/%d, want 1/503", attempts, resp.StatusCode)
	}
	if result.RetryCount != 0 || result.Outcome != publicRetryOutcomeSkipped || result.ReplaySkippedReason != "request_body_not_replayable" {
		t.Fatalf("retry result = %+v", result)
	}
}

func TestPublicAgentRetryPreservesWebSocketUpgradeBody(t *testing.T) {
	app, snapshot, target, first, _ := newPublicRetryTestApp(t)
	body := &retryTrackingReadWriteCloser{Reader: strings.NewReader("pong")}
	rt := &publicAgentAttemptRoundTripper{
		app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
		requestID: "websocket-request",
		transportForAgent: func(*AgentConn) http.RoundTripper {
			return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: body, Header: make(http.Header), Request: req}, nil
			})
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://proxy.test/ws", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	upgraded, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("upgrade body type %T does not implement io.ReadWriteCloser", resp.Body)
	}
	if _, err := upgraded.Write([]byte("ping")); err != nil {
		t.Fatalf("write upgrade body: %v", err)
	}
	if body.writes.String() != "ping" {
		t.Fatalf("upgrade write = %q, want ping", body.writes.String())
	}
	if err := upgraded.Close(); err != nil {
		t.Fatalf("close upgrade body: %v", err)
	}
	if !body.closed {
		t.Fatal("underlying upgrade body was not closed")
	}
	if first.ActiveRequests.Load() != 0 {
		t.Fatalf("active requests after upgrade close = %d, want 0", first.ActiveRequests.Load())
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
		1024, p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_STREAM, 0,
		0, nil, nil, nil, nil, false,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("validation error = %v, want invalid argument", err)
	}

	if _, err := app.validatePublicRetryRuleInput(
		context.Background(), "mutations", 100, true, []string{http.MethodPost}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED,
		1024, p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_STREAM, 0,
		0, nil, nil, nil, nil, true,
	); err != nil {
		t.Fatalf("acknowledged validation: %v", err)
	}
}

func TestValidatePublicRetryRuleStatusCodes(t *testing.T) {
	app := NewApp(nil, nil)
	_, err := app.validatePublicRetryRuleInput(
		context.Background(), "gateway-errors", 100, true, []string{http.MethodGet}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER,
		0, p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_STREAM, 0,
		0, []int64{http.StatusServiceUnavailable}, nil, nil, nil, false,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unacknowledged status retry error = %v, want invalid argument", err)
	}

	input, err := app.validatePublicRetryRuleInput(
		context.Background(), "gateway-errors", 100, true, []string{http.MethodGet}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER,
		0, p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_STREAM, 0,
		0, []int64{504, 502, 503, 502}, nil, nil, nil, true,
	)
	if err != nil {
		t.Fatalf("validate retry statuses: %v", err)
	}
	if input.RetryStatusJSON != "[502,503,504]" {
		t.Fatalf("normalized retry statuses = %s", input.RetryStatusJSON)
	}

	_, err = app.validatePublicRetryRuleInput(
		context.Background(), "gateway-errors", 100, true, []string{http.MethodGet}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER,
		0, p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_STREAM, 0,
		0, []int64{399}, nil, nil, nil, true,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid retry status error = %v, want invalid argument", err)
	}
}

func TestValidatePublicRetryRuleExplainsMissingScopeReferences(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	tests := []struct {
		name      string
		routeIDs  []int64
		targetIDs []int64
		want      string
	}{
		{name: "route", routeIDs: []int64{404}, want: "retry rule route 404 no longer exists"},
		{name: "target", targetIDs: []int64{405}, want: "retry rule route target 405 no longer exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := app.validatePublicRetryRuleInput(
				context.Background(), "stale-scope", 100, true, []string{http.MethodGet}, 1,
				p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
				p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER,
				0, p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_STREAM, 0,
				0, nil, tt.routeIDs, tt.targetIDs, nil, false,
			)
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want invalid argument containing %q", err, tt.want)
			}
		})
	}
}

func TestValidatePublicRetryRuleBufferedResponseRequiresRiskAcknowledgementAndBound(t *testing.T) {
	app := NewApp(nil, nil)
	_, err := app.validatePublicRetryRuleInput(
		context.Background(), "completed-responses", 100, true, []string{http.MethodGet}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER, 0,
		p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED, 1024,
		0, nil, nil, nil, nil, false,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unacknowledged response buffering error = %v, want invalid argument", err)
	}

	input, err := app.validatePublicRetryRuleInput(
		context.Background(), "completed-responses", 100, true, []string{http.MethodGet}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER, 0,
		p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED, 1024,
		0, nil, nil, nil, nil, true,
	)
	if err != nil {
		t.Fatalf("validate response buffering: %v", err)
	}
	if input.ResponseBodyMode != publicRetryResponseBodyModeBuffered || input.MaxBufferedResponseBodyBytes != 1024 || input.MaxBufferedResponseWaitMillis != defaultPublicRetryResponseWaitMillis {
		t.Fatalf("validated response buffering = mode %q limit %d wait %d", input.ResponseBodyMode, input.MaxBufferedResponseBodyBytes, input.MaxBufferedResponseWaitMillis)
	}

	_, err = app.validatePublicRetryRuleInput(
		context.Background(), "completed-responses", 100, true, []string{http.MethodGet}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER, 0,
		p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED, maxPublicRetryBufferedResponseBodyBytes+1,
		0, nil, nil, nil, nil, true,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized response buffering error = %v, want invalid argument", err)
	}

	_, err = app.validatePublicRetryRuleInput(
		context.Background(), "completed-responses", 100, true, []string{http.MethodGet}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER, 0,
		p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED, 1024,
		minPublicRetryResponseWaitMillis-1, nil, nil, nil, nil, true,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("short response wait error = %v, want invalid argument", err)
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
		FailureMode:                   p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES,
		BodyMode:                      p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED,
		MaxReplayBodyBytes:            2048,
		ResponseBodyMode:              p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED,
		MaxBufferedResponseBodyBytes:  8 * 1024 * 1024,
		MaxBufferedResponseWaitMillis: 45_000,
		RetryStatusCodes:              []int64{http.StatusBadGateway, http.StatusServiceUnavailable},
		DuplicateRiskAcknowledged:     true,
	})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	updated, err := app.UpdatePublicRetryRule(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update retry rule: %v", err)
	}
	if updated.Msg.Rule.Name != "vpn-mutations" || updated.Msg.Rule.MaxRetries != 2 || updated.Msg.Rule.MaxReplayBodyBytes != 2048 || updated.Msg.Rule.ResponseBodyMode != p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED || updated.Msg.Rule.MaxBufferedResponseBodyBytes != 8*1024*1024 || updated.Msg.Rule.MaxBufferedResponseWaitMillis != 45_000 || len(updated.Msg.Rule.RetryStatusCodes) != 2 {
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
