package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
)

// Common to A and B: use the real stage, ReverseProxy and HTTP transport. The
// existing direct streaming tests own event attribution/cache/shaper checks;
// this small fixture adds the target-stage lifetime assertions they lack.
func TestPublicTargetStageOwnsHardSlotThroughForwarding(t *testing.T) {
	for _, mode := range []string{"success", "terminal-body-error", "cancellation", "panic"} {
		t.Run(mode, func(t *testing.T) {
			headers, bodyGate, originDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			outerGate, stageReturned, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var bodyOnce, outerOnce sync.Once
			openBody := func() { bodyOnce.Do(func() { close(bodyGate) }) }
			openOuter := func() { outerOnce.Do(func() { close(outerGate) }) }
			requestContext, cancel := context.WithCancel(t.Context())
			// Model a real net/http handler, so a terminal streaming copy error
			// propagates ErrAbortHandler instead of the test-only suppression.
			requestContext = context.WithValue(requestContext, http.ServerContextKey, &http.Server{})
			var origin *httptest.Server
			var app *App
			var ctx *publicProxyContext
			pool := newBoundedPublicProxyContextPool(1)
			started := false
			// Install all unblock/join/refund paths before fallible setup.
			t.Cleanup(func() {
				cancel()
				openBody()
				openOuter()
				if started {
					awaitDirectStreamSignal(t, joined, "target stage goroutine")
				} else if ctx != nil {
					ctx.runCleanup()
					pool.release(ctx)
				}
				if origin != nil {
					origin.Close()
				}
				if app != nil && app.DirectTransports != nil {
					app.DirectTransports.closeAll()
				}
			})
			origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(originDone)
				length := "4"
				if mode == "terminal-body-error" {
					length = "5"
				}
				w.Header().Set("Content-Length", length)
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(headers)
				select {
				case <-bodyGate:
					if mode != "cancellation" {
						_, _ = w.Write([]byte("late"))
					}
				case <-r.Context().Done():
				}
			}))
			originURL, err := url.Parse(origin.URL)
			if err != nil {
				t.Fatal(err)
			}
			app = NewApp(nil, nil)
			app.publicTargetRequests = newKeyedRequestCapacityLimiter(1, 1)
			app.publicProxyRequests = newRequestCapacityLimiter(1, 1)
			app.publicClientRequests = newKeyedStringRequestCapacityLimiter(1)
			app.TargetHealth = newPublicRouteTargetHealthMonitor()
			app.PublicWAF = newPublicWAF()
			ctx = pool.acquire()
			writer := httptest.NewRecorder()
			committed := make(chan struct{})
			downstream := &targetStageWriter{ResponseRecorder: writer, committed: committed, panicWrite: mode == "panic"}
			request := httptest.NewRequest(http.MethodGet, "http://public.test/late", nil).WithContext(requestContext)
			initializePublicProxyContext(ctx, app, 1, downstream, request)
			ctx.Resolution = publicRouteResolution{
				Listener:      publicListenerConfig{Protocol: publicListenerProtocolHTTP},
				RouteTargetID: sql.NullInt64{Int64: 71, Valid: true},
				Target: publicRouteTargetConfig{ID: 71, Name: "stage-lifetime", Enabled: true,
					TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportDirect, ParsedURL: originURL},
			}
			if publicRequestAdmissionStage(ctx) != publicProxyStageContinue || beginWAFPressureStage(ctx) != publicProxyStageContinue || publicResponseAdmissionStage(ctx) != publicProxyStageContinue {
				t.Fatal("fixture request/response admission denied")
			}
			// Retain a precreated entry for race-safe exact channel reads, then
			// use the unchanged legacy API to probe exclusion/recovery in A/B.
			initial, ok := app.publicTargetRequests.tryAcquire(71)
			if !ok {
				t.Fatal("initial target grant denied")
			}
			initial()
			entry := app.publicTargetRequests.entries[71]
			type observation struct {
				slots               int
				active, global, waf int64
			}
			outerObserved := make(chan observation, 1)
			ctx.deferCleanup(func() {
				outerObserved <- observation{len(entry.slots), app.TargetHealth.activeRequests(71), int64(len(app.publicProxyRequests.slots)), app.PublicWAF.proxyActiveRequests.Load()}
			})
			var recovered any
			var result publicProxyStageResult
			started = true
			go func() {
				defer close(joined)
				defer pool.release(ctx)
				// The lexical stage must unwind before this outer cleanup.
				defer ctx.runCleanup()
				func() {
					defer func() { recovered = recover() }()
					result = activeTargetAccountingAndForwardStage(ctx)
				}()
				close(stageReturned)
				<-outerGate
			}()
			awaitDirectStreamSignal(t, headers, "late upstream headers")
			awaitDirectStreamSignal(t, committed, "downstream 200 commitment")
			if len(entry.slots) != 1 || app.TargetHealth.activeRequests(71) != 1 {
				t.Fatalf("late-body target ownership = slots %d active %d, want 1/1", len(entry.slots), app.TargetHealth.activeRequests(71))
			}
			if release, ok := app.publicTargetRequests.tryAcquire(71); ok {
				release()
				t.Fatal("target slot refunded while synchronous forwarding awaited late body")
			}
			select {
			case <-stageReturned:
				t.Fatal("stage returned before late body/cancellation")
			default:
			}
			if mode == "cancellation" {
				cancel()
			} else {
				openBody()
			}
			awaitDirectStreamSignal(t, stageReturned, "forwarding return/unwind")
			awaitDirectStreamSignal(t, originDone, "upstream handler")
			if len(entry.slots) != 0 || app.TargetHealth.activeRequests(71) != 0 {
				t.Fatalf("stage failed exact refund before outer cleanup: slots=%d active=%d", len(entry.slots), app.TargetHealth.activeRequests(71))
			}
			release, ok := app.publicTargetRequests.tryAcquire(71)
			if !ok {
				t.Fatal("target capacity unavailable before outer cleanup")
			}
			release()
			if len(app.publicProxyRequests.slots) != 1 || app.PublicWAF.proxyActiveRequests.Load() != 1 {
				t.Fatal("outer cleanup ran before its gate")
			}
			wantPanic := any(nil)
			if mode == "terminal-body-error" || mode == "cancellation" {
				wantPanic = http.ErrAbortHandler
			}
			if mode == "panic" {
				wantPanic = "target-stage-write-panic"
			}
			if recovered != wantPanic {
				t.Errorf("forwarding panic = %v, want %v", recovered, wantPanic)
			}
			if mode == "success" && (result != publicProxyStageDone || writer.Code != http.StatusOK || writer.Body.String() != "late") {
				t.Errorf("success = stage %d status %d body %q", result, writer.Code, writer.Body.String())
			}
			openOuter()
			awaitDirectStreamSignal(t, joined, "outer cleanup and context release")
			select {
			case got := <-outerObserved:
				if got != (observation{global: 1, waf: 1}) {
					t.Errorf("first outer callback ownership = %+v", got)
				}
			default:
				t.Fatal("joined stage did not report its outer cleanup observation")
			}
			app.publicClientRequests.mu.Lock()
			clients := len(app.publicClientRequests.inUse)
			app.publicClientRequests.mu.Unlock()
			if len(entry.slots) != 0 || app.TargetHealth.activeRequests(71) != 0 || len(app.publicProxyRequests.slots) != 0 || clients != 0 || app.PublicWAF.proxyActiveRequests.Load() != 0 {
				t.Fatal("target/global/client/WAF ownership did not drain exactly")
			}
			reused := pool.acquire()
			defer pool.release(reused)
			if reused != ctx || !reflect.DeepEqual(*reused, publicProxyContext{}) {
				t.Fatal("joined stage failed to reset/release context")
			}
		})
	}
}

type targetStageWriter struct {
	*httptest.ResponseRecorder
	committed  chan struct{}
	once       sync.Once
	panicWrite bool
}

func (w *targetStageWriter) WriteHeader(status int) {
	w.ResponseRecorder.WriteHeader(status)
	if status == http.StatusOK {
		w.once.Do(func() { close(w.committed) })
	}
}
func (w *targetStageWriter) Write(p []byte) (int, error) {
	if w.panicWrite {
		panic("target-stage-write-panic")
	}
	return w.ResponseRecorder.Write(p)
}
