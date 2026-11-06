package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"p2pstream/internal/config"
)

func TestPublicProxyCleanupLIFOAndReferenceClearing(t *testing.T) {
	for _, count := range []int{0, 1, 5, 8} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ctx := &publicProxyContext{}
			var got []int
			ctx.deferCleanup(nil)
			for i := 0; i < count; i++ {
				ctx.deferCleanup(func() {
					// The executing callback must already be detached, including
					// from the overflow backing array retained by a caller.
					if i < publicProxyInlineCleanupCapacity {
						if ctx.cleanupInline[i] != nil {
							t.Error("executing inline callback retained in context")
						}
					} else if len(ctx.cleanup) != i-publicProxyInlineCleanupCapacity {
						t.Error("executing overflow callback retained in context")
					}
					got = append(got, i)
				})
			}
			retainedOverflow := ctx.cleanup
			ctx.runCleanup()
			ctx.runCleanup()
			var want []int
			for i := count - 1; i >= 0; i-- {
				want = append(want, i)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("cleanup order = %v, want %v", got, want)
			}
			for _, fn := range retainedOverflow {
				if fn != nil {
					t.Fatal("overflow backing storage retained a callback")
				}
			}
			if !reflect.DeepEqual(*ctx, publicProxyContext{}) {
				t.Fatal("cleanup retained state after drain")
			}
		})
	}
}

func TestPublicProxyCleanupRunsAllCallbacksOnHandlerPanic(t *testing.T) {
	for _, count := range []int{5, 8} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ctx := newPublicProxyContext(nil, 1, panicPublicProxyResponseWriter{}, httptest.NewRequest(http.MethodGet, "http://example.test/../private", nil))
			var got []int
			for i := 0; i < count; i++ {
				ctx.deferCleanup(func() { got = append(got, i) })
			}
			func() {
				defer func() {
					if recovered := recover(); recovered != "response writer panic" {
						t.Fatalf("handler panic = %v, want response writer panic", recovered)
					}
				}()
				ctx.run()
			}()
			for i := 0; i < count; i++ {
				if len(got) != count || got[i] != count-1-i {
					t.Fatalf("panic cleanup order = %v, want all %d callbacks in LIFO order", got, count)
				}
			}
			if ctx.cleanup != nil || ctx.cleanupInlineCount != 0 {
				t.Fatal("handler panic retained cleanup state")
			}
		})
	}
}

func TestPublicProxyCleanupDetachesBeforeReentrantDrain(t *testing.T) {
	for _, count := range []int{5, 8} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ctx := &publicProxyContext{}
			var got []int
			for i := 0; i < count; i++ {
				ctx.deferCleanup(func() {
					got = append(got, i)
					if len(got) > count {
						t.Fatal("reentrant drain repeated an executing callback")
					}
					if i == count-1 {
						ctx.runCleanup()
					}
				})
			}
			ctx.runCleanup()
			for i := 0; i < count; i++ {
				if len(got) != count || got[i] != count-1-i {
					t.Fatalf("reentrant cleanup order = %v, want all %d callbacks in LIFO order", got, count)
				}
			}
			if !reflect.DeepEqual(*ctx, publicProxyContext{}) {
				t.Fatal("reentrant cleanup retained state")
			}
		})
	}
}

func TestPublicProxyBodylessCounterIsAbsent(t *testing.T) {
	for _, body := range []io.ReadCloser{nil, http.NoBody} {
		req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
		req.Body = body
		ctx := newPublicProxyContext(nil, 1, httptest.NewRecorder(), req)
		if ctx.Request.Body != body || ctx.Observability.requestBytes != nil || ctx.Observability.requestBytesValue() != 0 {
			t.Fatal("bodyless initialization changed the body or allocated a counter")
		}
	}
}

func TestPublicProxyBodyCounterPreservesLimitsAndConsumedBytes(t *testing.T) {
	for _, length := range []int64{6, -1} {
		t.Run(strconv.FormatInt(length, 10), func(t *testing.T) {
			app := &App{Config: &config.Config{PublicMaxRequestBodyBytes: 4}}
			req := httptest.NewRequest(http.MethodPost, "http://example.test/", strings.NewReader("123456"))
			req.ContentLength = length
			ctx := newPublicProxyContext(app, 1, httptest.NewRecorder(), req)
			if ctx.Observability.requestBytes == nil {
				t.Fatal("real body has no independent byte counter")
			}
			body, err := io.ReadAll(ctx.Request.Body)
			var limitError *http.MaxBytesError
			if !errors.As(err, &limitError) || string(body) != "1234" {
				t.Fatalf("limited body = %q, error %v, want 1234 and MaxBytesError", body, err)
			}
			if got := ctx.Observability.requestBytesValue(); got != 4 {
				t.Fatalf("consumed bytes = %d, want 4", got)
			}
			status, kind, _, ok := publicRequestBodyErrorForRequest(ctx.Request, err)
			if !ok || status != http.StatusRequestEntityTooLarge || kind != "request_body_too_large" {
				t.Fatalf("body failure classification = %d/%s/%t", status, kind, ok)
			}
		})
	}
}

func TestPublicProxyBodyCountersRemainIndependentAcrossReuse(t *testing.T) {
	pool := newBoundedPublicProxyContextPool(1)
	first := pool.acquire()
	initializePublicProxyContext(first, nil, 1, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://first.example/", strings.NewReader("first")))
	firstBody, firstBytes := first.Request.Body, first.Observability.requestBytes
	pool.release(first)
	second := pool.acquire()
	initializePublicProxyContext(second, nil, 2, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://second.example/", strings.NewReader("second")))
	defer pool.release(second)
	if firstBytes == second.Observability.requestBytes {
		t.Fatal("pooled context reused a body counter")
	}
	if _, err := io.Copy(io.Discard, firstBody); err != nil {
		t.Fatal(err)
	}
	if firstBytes.Load() != 5 || second.Observability.requestBytesValue() != 0 {
		t.Fatal("retained first body changed the next request's accounting")
	}
	if _, err := io.Copy(io.Discard, second.Request.Body); err != nil {
		t.Fatal(err)
	}
	if firstBytes.Load() != 5 || second.Observability.requestBytesValue() != 6 {
		t.Fatal("second body changed the previous request's accounting")
	}
}
