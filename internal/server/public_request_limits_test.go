package server

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/config"
)

func TestPublicRequestAdmissionRejectsDeclaredOversizeBody(t *testing.T) {
	app := NewApp(&config.Config{
		PublicMaxRequestBodyBytes:   4,
		PublicRequestBodyIdleMillis: 30_000,
		PublicMaxConcurrentRequests: 2,
	}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://public.test/upload", strings.NewReader("12345"))

	app.publicProxyHandler(1)(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestPublicProxyRejectsOversizeChunkedBody(t *testing.T) {
	app, handler := newPublicBodyLimitTestProxy(t, 4, 30*time.Second)
	request := httptest.NewRequest(http.MethodPost, "http://public.test/upload", strings.NewReader("12345"))
	request.ContentLength = -1
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%q", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
	}
	if app.TargetHealth != nil && app.TargetHealth.activeRequests(20) != 0 {
		t.Fatal("oversize body leaked active target accounting")
	}
}

func TestPublicProxyRejectsIdleChunkedUpload(t *testing.T) {
	_, handler := newPublicBodyLimitTestProxy(t, 1024, 50*time.Millisecond)
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	conn, err := net.DialTimeout("tcp", proxyURL.Host, time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "POST /upload HTTP/1.1\r\nHost: public.test\r\nTransfer-Encoding: chunked\r\n\r\n"); err != nil {
		t.Fatalf("write partial upload: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read timeout response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestTimeout {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want %d; body=%q", response.StatusCode, http.StatusRequestTimeout, body)
	}
}

func TestPublicRequestAdmissionRejectsAtCapacityAndRecovers(t *testing.T) {
	app := NewApp(&config.Config{
		PublicMaxRequestBodyBytes:   1024,
		PublicRequestBodyIdleMillis: 30_000,
		PublicMaxConcurrentRequests: 1,
	}, nil)
	release, ok := app.publicProxyRequests.tryAcquire()
	if !ok {
		t.Fatal("failed to reserve public request capacity")
	}

	rec := httptest.NewRecorder()
	app.publicProxyHandler(1)(rec, httptest.NewRequest(http.MethodGet, "http://public.test/", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("capacity response = %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}

	release()
	if releaseAgain, acquired := app.publicProxyRequests.tryAcquire(); !acquired {
		t.Fatal("capacity was not released")
	} else {
		releaseAgain()
	}
}

func TestPublicCapacityRejectionDoesNotDrainStalledChunkedUpload(t *testing.T) {
	app := NewApp(&config.Config{
		PublicMaxRequestBodyBytes:   1024,
		PublicRequestBodyIdleMillis: 30_000,
		PublicMaxConcurrentRequests: 1,
	}, nil)
	release, ok := app.publicProxyRequests.tryAcquire()
	if !ok {
		t.Fatal("failed to reserve public request capacity")
	}
	defer release()

	proxy := httptest.NewServer(app.publicProxyHandler(1))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	conn, err := net.DialTimeout("tcp", proxyURL.Host, time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: public.test\r\nTransfer-Encoding: chunked\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read capacity response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	if !response.Close {
		t.Fatal("capacity rejection did not close the incomplete HTTP/1 request connection")
	}
}

func TestKeyedRequestCapacityLimiterIsolatesTargets(t *testing.T) {
	limiter := newKeyedRequestCapacityLimiter(1, 1)
	release, ok := limiter.tryAcquire(10)
	if !ok {
		t.Fatal("first target acquisition failed")
	}
	if _, ok := limiter.tryAcquire(10); ok {
		t.Fatal("same target exceeded its request capacity")
	}
	releaseOther, ok := limiter.tryAcquire(11)
	if !ok {
		t.Fatal("one saturated target blocked a different target")
	}
	releaseOther()
	release()
}

func TestIdleRequestBodySetsSlidingDeadlineAndPreservesReads(t *testing.T) {
	w := &deadlineResponseWriter{header: make(http.Header)}
	body := &idleRequestBody{
		body:       io.NopCloser(strings.NewReader("streaming body")),
		controller: http.NewResponseController(w),
		timeout:    time.Minute,
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "streaming body" {
		t.Fatalf("body = %q, want streaming body", got)
	}
	if w.lastReadDeadline.IsZero() {
		t.Fatal("request body read did not set an idle deadline")
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !w.lastReadDeadline.IsZero() {
		t.Fatal("closing request body did not clear the read deadline")
	}
}

func TestPublicRequestBodyErrorClassification(t *testing.T) {
	maxErr := &http.MaxBytesError{Limit: 10}
	status, kind, _, ok := publicRequestBodyError(maxErr)
	if !ok || status != http.StatusRequestEntityTooLarge || kind != "request_body_too_large" {
		t.Fatalf("max body classification = %d/%q/%v", status, kind, ok)
	}
	status, kind, _, ok = publicRequestBodyError(errors.Join(errors.New("transport"), errPublicRequestBodyIdleTimeout))
	if !ok || status != http.StatusRequestTimeout || kind != "request_body_idle_timeout" {
		t.Fatalf("idle body classification = %d/%q/%v", status, kind, ok)
	}
}

type deadlineResponseWriter struct {
	header           http.Header
	lastReadDeadline time.Time
}

func (w *deadlineResponseWriter) Header() http.Header         { return w.header }
func (w *deadlineResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *deadlineResponseWriter) WriteHeader(int)             {}
func (w *deadlineResponseWriter) SetReadDeadline(deadline time.Time) error {
	w.lastReadDeadline = deadline
	return nil
}

func newPublicBodyLimitTestProxy(t *testing.T, maxBodyBytes int64, idleTimeout time.Duration) (*App, http.Handler) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	target := publicRouteTargetConfig{
		ID:         20,
		RouteID:    10,
		Name:       "upload-upstream",
		Enabled:    true,
		TargetType: publicRouteTargetTypeProxy,
		Transport:  publicRouteTargetTransportDirect,
		ParsedURL:  origin,
	}
	app := NewApp(&config.Config{
		PublicMaxRequestBodyBytes:    maxBodyBytes,
		PublicRequestBodyIdleMillis:  idleTimeout.Milliseconds(),
		PublicMaxConcurrentRequests:  8,
		PublicMaxConcurrentPerTarget: 4,
	}, nil)
	setPublicSnapshotForTest(t, app, &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP, Enabled: true}},
		RoutesByListener: map[int64][]publicRouteConfig{1: {{
			ID:               10,
			Enabled:          true,
			PathPrefix:       "/",
			Action:           publicRouteActionForward,
			PathSecurityMode: publicRoutePathSecurityModeStrict,
			Targets:          []publicRouteTargetConfig{target},
		}}},
		RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target},
	})
	return app, app.publicProxyHandler(1)
}
