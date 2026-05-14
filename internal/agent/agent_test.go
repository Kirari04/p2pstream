package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"p2pstream/httpmsg"
	"p2pstream/msg"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	*bytes.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestHandleRequestTLSMetadataControlsVerification(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(httpmsg.MetadataTLSSkipVerify) != "" {
			t.Fatalf("internal TLS metadata was forwarded upstream")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	status, _ := performAgentRequest(t, target.URL, false)
	if status != http.StatusBadGateway {
		t.Fatalf("expected self-signed upstream to fail without tls skip verify, got %d", status)
	}

	status, body := performAgentRequest(t, target.URL, true)
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("expected tls skip verify request to succeed, got status=%d body=%q", status, body)
	}
}

func TestAgentHealthCheckMetadataDoesNotIncrementRequestCounters(t *testing.T) {
	originalClient := defaultForwardClient
	defaultForwardClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get(httpmsg.MetadataHealthCheck) != "" {
			t.Fatalf("internal health check metadata was forwarded upstream")
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        http.StatusText(http.StatusOK),
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader([]byte("ok"))),
			ContentLength: 2,
		}, nil
	})}
	t.Cleanup(func() {
		defaultForwardClient = originalClient
		resetAgentRequestCounters()
	})
	resetAgentRequestCounters()

	conn, done := startAgentRequestHandlerWithMetadata(t, "http://upstream.test/health", map[string]string{
		httpmsg.MetadataHealthCheck: "true",
	})
	select {
	case <-conn.writeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler")
	}

	if activeRequests.Load() != 0 || reqSuccess.Load() != 0 || reqClientError.Load() != 0 || reqServerError.Load() != 0 || reqInternalError.Load() != 0 {
		t.Fatalf("health check changed counters: active=%d success=%d client=%d server=%d internal=%d",
			activeRequests.Load(), reqSuccess.Load(), reqClientError.Load(), reqServerError.Load(), reqInternalError.Load())
	}
}

func performAgentRequest(t *testing.T, targetURL string, tlsSkipVerify bool) (int, string) {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new request id: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	enc := httpmsg.NewRequestEncoderWithMetadata(id, req, map[string]string{
		httpmsg.MetadataTLSSkipVerify: strconv.FormatBool(tlsSkipVerify),
	})
	firstReq, err := enc.Next()
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	reqCh := make(chan *msg.Request, 10)
	reqCh <- firstReq
	conn := &agentConnection{
		ctx:     context.Background(),
		writeCh: make(chan *msg.Request, 10),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.handleRequest(id, reqCh)
	}()

	var firstResp *msg.Request
	select {
	case firstResp = <-conn.writeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent response")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent request handler")
	}

	resp, err := httpmsg.DecodeResponse(firstResp, &httpmsg.ChannelStream{Ch: conn.writeCh})
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestHandleRequestClosesSuccessfulUpstreamBody(t *testing.T) {
	originalClient := defaultForwardClient
	body := &trackingReadCloser{Reader: bytes.NewReader([]byte("ok"))}
	defaultForwardClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        http.StatusText(http.StatusOK),
			Header:        make(http.Header),
			Body:          body,
			ContentLength: int64(body.Len()),
		}, nil
	})}
	t.Cleanup(func() {
		defaultForwardClient = originalClient
	})

	conn, done := startAgentRequestHandler(t, "http://upstream.test/ok")
	select {
	case <-conn.writeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler")
	}
	if !body.closed {
		t.Fatal("upstream response body was not closed")
	}
}

func TestHandleRequestUsesGenericBadGatewayBody(t *testing.T) {
	originalClient := defaultForwardClient
	defaultForwardClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 10.0.0.5:8080: secret internal route")
	})}
	t.Cleanup(func() {
		defaultForwardClient = originalClient
	})

	conn, done := startAgentRequestHandler(t, "http://upstream.test/fail")
	var firstResp *msg.Request
	select {
	case firstResp = <-conn.writeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler")
	}

	resp, err := httpmsg.DecodeResponse(firstResp, &httpmsg.ChannelStream{Ch: conn.writeCh})
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway || string(body) != "Bad Gateway\n" {
		t.Fatalf("unexpected error response: status=%d body=%q", resp.StatusCode, body)
	}
}

func startAgentRequestHandler(t *testing.T, targetURL string) (*agentConnection, <-chan struct{}) {
	t.Helper()
	return startAgentRequestHandlerWithMetadata(t, targetURL, nil)
}

func startAgentRequestHandlerWithMetadata(t *testing.T, targetURL string, metadata map[string]string) (*agentConnection, <-chan struct{}) {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new request id: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	enc := httpmsg.NewRequestEncoderWithMetadata(id, req, metadata)
	firstReq, err := enc.Next()
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	reqCh := make(chan *msg.Request, 10)
	reqCh <- firstReq
	conn := &agentConnection{
		ctx:     context.Background(),
		writeCh: make(chan *msg.Request, 10),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.handleRequest(id, reqCh)
	}()
	return conn, done
}

func resetAgentRequestCounters() {
	activeRequests.Store(0)
	reqSuccess.Store(0)
	reqClientError.Store(0)
	reqServerError.Store(0)
	reqInternalError.Store(0)
}
