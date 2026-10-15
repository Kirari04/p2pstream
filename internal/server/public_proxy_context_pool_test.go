package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedPublicProxyContextPoolResetsAndBoundsRetention(t *testing.T) {
	pool := newBoundedPublicProxyContextPool(2)
	first := pool.acquire()
	second := pool.acquire()
	third := pool.acquire()
	if first == second || first == third || second == third {
		t.Fatal("empty pool returned the same context to concurrent owners")
	}

	*first = publicProxyContext{
		App:                          &App{},
		ListenerID:                   41,
		StartedAt:                    time.Now(),
		Snapshot:                     &publicProxySnapshot{},
		Writer:                       httptest.NewRecorder(),
		ResponseWriter:               httptest.NewRecorder(),
		Recorder:                     &proxyResponseRecorder{},
		Request:                      httptest.NewRequest(http.MethodPost, "http://old.example/private", nil),
		Observability:                proxyRequestObservability{requestBytes: &atomic.Uint64{}},
		RequestContext:               proxyRequestContext{Method: "POST", Host: "old.example", PathPrefix: "/private"},
		Trace:                        &trafficRequestTrace{method: "POST", host: "old.example", path: "/private"},
		Resolution:                   publicRouteResolution{Action: "old-action", CacheKeyDigest: "old-digest"},
		RouteMatch:                   publicRouteMatch{Err: errors.New("old route error")},
		HasRouteMatch:                true,
		EncodedPathSeparatorsAllowed: true,
		PathIssues:                   publicPathIssueSet{EncodedSlash: true},
		TrafficShaperSelected:        true,
		TrafficShaperDecision:        publicTrafficShaperDecision{MatchError: errors.New("old shaper error")},
		CacheDecision:                &publicCacheDecision{KeyDigest: "old-digest"},
		RouteResolutionFailure:       errors.New("old resolution error"),
		cleanup:                      []func(){func() {}},
	}

	pool.release(first)
	pool.release(second)
	pool.release(third)
	if got := len(pool.contexts); got != 2 {
		t.Fatalf("retained contexts = %d, want hard cap 2", got)
	}

	reused := pool.acquire()
	if reused != first {
		t.Fatalf("first retained context = %p, want released first context %p", reused, first)
	}
	if !reflect.DeepEqual(*reused, publicProxyContext{}) {
		t.Fatalf("reused context retained prior request state: %+v", *reused)
	}
}

func TestPublicProxyContextCleanupAndReleaseRunOnPanic(t *testing.T) {
	pool := newBoundedPublicProxyContextPool(1)
	app := &App{publicProxyRequests: newRequestCapacityLimiter(1, 1)}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.URL.Path = "/../private"
	w := panicPublicProxyResponseWriter{}
	owned := pool.acquire()
	pool.release(owned)

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("public proxy run did not propagate response writer panic")
			}
		}()
		servePublicProxyRequest(pool, app, 7, w, req)
	}()

	requestRelease, acquired := app.publicProxyRequests.tryAcquire()
	if !acquired {
		t.Fatal("panic path did not run request admission cleanup")
	}
	requestRelease()
	reused := pool.acquire()
	if reused != owned {
		t.Fatalf("panic path retained context = %p, want %p", reused, owned)
	}
	if !reflect.DeepEqual(*reused, publicProxyContext{}) {
		t.Fatalf("panic path retained request state: %+v", *reused)
	}
}

func TestPublicProxyContextReuseDoesNotReuseNestedBodyOrRecorderState(t *testing.T) {
	pool := newBoundedPublicProxyContextPool(1)
	firstWriter := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodPost, "http://first.example/upload", strings.NewReader("first-body"))
	first := pool.acquire()
	initializePublicProxyContext(first, nil, 1, firstWriter, firstRequest)
	retainedBody := first.Request.Body
	retainedBytes := first.Observability.requestBytes
	retainedRecorder := first.Recorder
	pool.release(first)

	secondWriter := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, "http://second.example/", nil)
	second := pool.acquire()
	if second != first {
		t.Fatalf("reused context = %p, want %p", second, first)
	}
	initializePublicProxyContext(second, nil, 2, secondWriter, secondRequest)
	if retainedBytes == second.Observability.requestBytes || retainedRecorder == second.Recorder {
		t.Fatal("context reuse also reused nested request or response lifetime state")
	}

	body, err := io.ReadAll(retainedBody)
	if err != nil {
		t.Fatalf("read retained first request body: %v", err)
	}
	if string(body) != "first-body" || retainedBytes.Load() != uint64(len(body)) {
		t.Fatalf("retained request body = %q/%d bytes, want first-body/%d", body, retainedBytes.Load(), len(body))
	}
	if second.Observability.requestBytes.Load() != 0 {
		t.Fatalf("retained first body updated reused request counter: %d", second.Observability.requestBytes.Load())
	}
	if _, err := retainedRecorder.Write([]byte("first-response")); err != nil {
		t.Fatalf("write retained first recorder: %v", err)
	}
	if firstWriter.Body.String() != "first-response" || secondWriter.Body.Len() != 0 {
		t.Fatalf("retained recorder wrote first/second bodies %q/%q", firstWriter.Body.String(), secondWriter.Body.String())
	}
	pool.release(second)
}

type panicPublicProxyResponseWriter struct{}

func (panicPublicProxyResponseWriter) Header() http.Header { panic("response writer panic") }
func (panicPublicProxyResponseWriter) Write([]byte) (int, error) {
	panic("response writer panic")
}
func (panicPublicProxyResponseWriter) WriteHeader(int) { panic("response writer panic") }

var benchmarkPublicProxyContextSink *publicProxyContext

func BenchmarkPublicProxyContextInitialization(b *testing.B) {
	request := httptest.NewRequest(http.MethodGet, "http://benchmark.example/resource", nil)
	writer := httptest.NewRecorder()
	b.Run("unpooled", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkPublicProxyContextSink = newPublicProxyContext(nil, 1, writer, request)
		}
	})
	b.Run("bounded-reuse", func(b *testing.B) {
		pool := newBoundedPublicProxyContextPool(1)
		b.ReportAllocs()
		for range b.N {
			ctx := pool.acquire()
			initializePublicProxyContext(ctx, nil, 1, writer, request)
			benchmarkPublicProxyContextSink = ctx
			pool.release(ctx)
		}
	})
}
