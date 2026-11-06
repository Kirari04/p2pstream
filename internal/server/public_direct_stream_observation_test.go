package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real TCP test listener serves the complete publicProxyHandler pipeline.
// Per-request handler completion is joined even for ErrAbortHandler and 101;
// this fixture does not claim native-listener socket admission qualification.
func newDirectStreamObservationFixture(t *testing.T, originURL string, compose bool) (*App, string, func()) {
	t.Helper()
	app, resolution, closeDB := newTestPublicCacheApp(t)
	t.Cleanup(closeDB)
	installDeterministicServerResourceController(t, app)
	parsed, err := url.Parse(originURL)
	if err != nil {
		t.Fatal(err)
	}
	resolution.Target.URL, resolution.Target.ParsedURL = originURL, parsed
	resolution.Target.Weight = 100
	resolution.Target.HealthCheck.Enabled = true
	resolution.Route = publicRouteConfig{ID: 10, ListenerID: 1, Enabled: true,
		HostPattern: "assets.example.test", PathPrefix: "/assets", Action: publicRouteActionForward,
		PathSecurityMode: publicRoutePathSecurityModeStrict, Targets: []publicRouteTargetConfig{resolution.Target}}
	listener := publicListenerConfig{ID: 1, Name: "direct-stream-observation", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
	snapshot := *app.currentPublicSnapshot()
	snapshot.Listeners = map[int64]publicListenerConfig{1: listener}
	snapshot.RoutesByListener = map[int64][]publicRouteConfig{1: {resolution.Route}}
	snapshot.RouteTargets = map[int64]publicRouteTargetConfig{30: resolution.Target}
	if compose {
		rule := testTrafficShaperRule(73, "direct-stream", 1, publicTrafficShaperBudgetScopePerKey, 0, 128<<20)
		rule.Match = mustPublicPolicyMatchCEL(t, `true`)
		rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)
		snapshot.TrafficShaperRules = []publicTrafficShaperRuleConfig{rule}
	} else {
		snapshot.CacheSettings.Enabled = false
		snapshot.CacheRules = nil
		snapshot.CacheFingerprint = publicCacheRuntimeFingerprint(snapshot.CacheSettings, nil)
	}
	setPublicSnapshotForTest(t, app, &snapshot)
	app.PublicCache.reconcile(snapshot.CacheSettings, snapshot.CacheRules)
	app.TrafficShaper.reconcile(&snapshot)
	app.TargetHealth.reconcile(app, &snapshot, false)
	handlerDone := make(chan struct{})
	publicHandler := app.publicProxyHandler(1)
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		publicHandler(w, r)
	}))
	stopped := false
	finish := func() {
		if stopped {
			return
		}
		stopped = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := public.Config.Shutdown(ctx); err != nil {
			t.Errorf("stop direct-stream public listener: %v", err)
		}
		select {
		case <-handlerDone:
		case <-ctx.Done():
			t.Errorf("direct-stream public handler did not join: %v", ctx.Err())
		}
		public.Close()
		app.DirectTransports.closeAll()
		if err := app.FlushObservabilityRecorder(ctx); err != nil {
			t.Errorf("flush direct-stream recorder: %v", err)
		}
		if err := app.CloseObservabilityRecorder(ctx); err != nil {
			t.Errorf("close direct-stream recorder: %v", err)
		}
	}
	t.Cleanup(finish)
	address := public.Listener.Addr().String()
	return app, address, finish
}

func awaitDirectStreamSignal(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not join", label)
	}
}

func assertDirectStreamObservationEvent(t *testing.T, app *App, origins int64, wantStatus int, wantKind string, responseBytes int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var count, status, retryCount, requestBytes, recordedBytes int64
	var kind, method, host, outcome, retryKind string
	var listener, route, target, agent, retryRule, failedAgent sql.NullInt64
	err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*),status_code,error_kind,method,host,listener_id,route_id,route_target_id,agent_id,retry_rule_id,retry_failed_agent_id,retry_count,retry_outcome,retry_error_kind,request_bytes,response_bytes FROM proxy_request_events`).Scan(
		&count, &status, &kind, &method, &host, &listener, &route, &target, &agent, &retryRule, &failedAgent, &retryCount, &outcome, &retryKind, &requestBytes, &recordedBytes)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || origins != 1 || status != int64(wantStatus) || kind != wantKind {
		t.Fatalf("direct terminal event: events=%d origins=%d status=%d error=%q; want1/1/%d/%q", count, origins, status, kind, wantStatus, wantKind)
	}
	if listener != (sql.NullInt64{Int64: 1, Valid: true}) || route != (sql.NullInt64{Int64: 10, Valid: true}) || target != (sql.NullInt64{Int64: 30, Valid: true}) || agent.Valid || method != "GET" || host != "assets.example.test" || requestBytes != 0 || recordedBytes != responseBytes {
		t.Fatalf("direct attribution: listener=%v route=%v target=%v agent=%v method=%q host=%q bytes=%d/%d want0/%d", listener, route, target, agent, method, host, requestBytes, recordedBytes, responseBytes)
	}
	if retryRule.Valid || failedAgent.Valid || retryCount != 0 || outcome != "" || retryKind != "" {
		t.Fatalf("direct response changed disabled retry: rule=%v failed=%v count=%d outcome=%q error=%q", retryRule, failedAgent, retryCount, outcome, retryKind)
	}
	traces, _ := app.TargetHealth.listHealthTraces(30, 0, 10, false)
	targetConfig := app.currentPublicSnapshot().RouteTargets[30]
	if len(traces) != 0 || !app.TargetHealth.available(publicRouteTargetHealthConfigFromRouteTarget(targetConfig, nil)) || app.TargetHealth.activeRequests(30) != 0 || len(app.publicProxyRequests.slots) != 0 || app.PublicWAF.proxyActiveRequests.Load() != 0 || app.retryReplayBudget.used.Load() != 0 {
		t.Fatalf("direct terminal health/request owners: traces=%d active=%d public=%d waf=%d replay=%d", len(traces), app.TargetHealth.activeRequests(30), len(app.publicProxyRequests.slots), app.PublicWAF.proxyActiveRequests.Load(), app.retryReplayBudget.used.Load())
	}
	app.publicClientRequests.mu.Lock()
	clients := len(app.publicClientRequests.inUse)
	app.publicClientRequests.mu.Unlock()
	app.publicTargetRequests.mu.Lock()
	targetSlots := 0
	for _, limiter := range app.publicTargetRequests.entries {
		targetSlots += len(limiter.slots)
	}
	app.publicTargetRequests.mu.Unlock()
	ledger := app.agentStreamCapacity.snapshot()
	if clients != 0 || targetSlots != 0 {
		t.Fatalf("direct request leases: clients=%d targetSlots=%d", clients, targetSlots)
	}
	for _, owner := range []adaptiveResourceOwner{adaptiveResourceOwnerPublicRequest, adaptiveResourceOwnerPublicResponse} {
		if ledger.AdaptiveExternalBytesByOwner[owner] != 0 || ledger.AdaptiveExternalFDsByOwner[owner] != 0 || ledger.AdaptiveExternalReservationsByOwner[owner] != 0 {
			t.Fatalf("direct request/response adaptive owners: %+v", ledger)
		}
	}

}

func TestPublicDirectStreamingObservation(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		status                     int
		truncated, compose, cancel bool
		kind                       string
	}{
		{"truncated-200", 200, true, false, false, "upstream_response_body_truncated"},
		{"cache-miss-shaped-truncated-200", 200, true, true, false, "upstream_response_body_truncated"},
		{"complete-200", 200, false, false, false, ""},
		{"no-body-204", 204, false, false, false, ""},
		{"truncated-503-priority", 503, true, false, false, "upstream_status_503"},
		{"client-canceled", 200, true, false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := bytes.Repeat([]byte("direct-prefix\n"), 4096)
			if tc.status == http.StatusNoContent {
				prefix = nil
			}
			var origins atomic.Int64
			gate := make(chan struct{})
			var release sync.Once
			openGate := func() { release.Do(func() { close(gate) }) }
			defer openGate()
			originDone := make(chan struct{})
			originErr := make(chan error, 1)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(originDone)
				origins.Add(1)
				if r.Method != "GET" || r.URL.Path != "/assets/stream.txt" || r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Real-Ip") != "127.0.0.1" {
					originErr <- fmt.Errorf("unexpected direct origin request: method=%s path=%s forwarded=%s real=%s", r.Method, r.URL.Path, r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Real-Ip"))
					http.Error(w, "unexpected request", 400)
					return
				}
				length := len(prefix)
				if tc.truncated {
					length++
				}
				contentType := "text/event-stream"
				if tc.compose {
					contentType = "text/plain"
				}
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Cache-Control", "public, max-age=300")
				w.Header().Set("Content-Length", strconv.Itoa(length))
				w.Header().Set("Connection", "close")
				w.WriteHeader(tc.status)
				if n, err := w.Write(prefix); err != nil || n != len(prefix) {
					originErr <- fmt.Errorf("origin prefix write=%d/%d error=%v", n, len(prefix), err)
					return
				}
				w.(http.Flusher).Flush()
				if tc.cancel {
					select {
					case <-r.Context().Done():
					case <-gate:
					}
				} else {
					select {
					case <-gate:
					case <-r.Context().Done():
					}
				}
			}))
			t.Cleanup(origin.Close)
			app, address, finish := newDirectStreamObservationFixture(t, origin.URL, tc.compose)
			transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			t.Cleanup(transport.CloseIdleConnections)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/assets/stream.txt", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "assets.example.test"
			req.Header.Set("X-Forwarded-For", "198.51.100.91")
			req.Header.Set("X-Real-Ip", "198.51.100.92")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			gatedBytes := len(prefix)
			if tc.compose {
				gatedBytes = 8192
			}
			got := make([]byte, gatedBytes)
			if n, err := io.ReadFull(resp.Body, got); err != nil || n != gatedBytes || !bytes.Equal(got, prefix[:gatedBytes]) || resp.StatusCode != tc.status || resp.ProtoMajor != 1 {
				t.Fatalf("committed direct prefix: status=%d proto=%s read=%d/%d exact=%t error=%v", resp.StatusCode, resp.Proto, n, gatedBytes, bytes.Equal(got, prefix[:gatedBytes]), err)
			}
			if tc.compose {
				if resp.Header.Get("X-P2pstream-Cache") != "MISS" {
					t.Fatal("cache composition did not enter miss path")
				}
				entries, err := os.ReadDir(app.PublicCache.tempDir())
				if err != nil || len(entries) != 1 {
					t.Fatalf("live cache temp entries=%d error=%v", len(entries), err)
				}
				buffered, err := os.ReadFile(filepath.Join(app.PublicCache.tempDir(), entries[0].Name()))
				if err != nil || len(buffered) < gatedBytes || len(buffered) > len(prefix) {
					t.Fatalf("live cache prefix bytes=%d want[%d,%d] error=%v", len(buffered), gatedBytes, len(prefix), err)
				}
				if !bytes.Equal(buffered, prefix[:len(buffered)]) {
					t.Fatal("cache capture changed origin prefix")
				}
				app.TrafficShaper.mu.Lock()
				runtime := app.TrafficShaper.rules[73]
				selected := runtime != nil && len(runtime.downloadBuckets) == 1
				app.TrafficShaper.mu.Unlock()
				if !selected {
					t.Fatal("cache miss did not compose with download shaping")
				}
			}
			if tc.cancel {
				cancel()
			} else {
				openGate()
			}
			tail, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(len(prefix)-gatedBytes+1)))
			got = append(got, tail...)
			if len(got) > len(prefix) || !bytes.Equal(got, prefix[:len(got)]) {
				t.Fatalf("public body changed sent prefix: received=%d sent=%d", len(got), len(prefix))
			}
			if !tc.compose && !bytes.Equal(got, prefix) {
				t.Fatalf("SSE prefix received=%d want%d", len(got), len(prefix))
			}
			if tc.cancel {
				if !errors.Is(readErr, context.Canceled) {
					t.Fatalf("canceled client read=%v", readErr)
				}
			} else if tc.truncated {
				if !errors.Is(readErr, io.ErrUnexpectedEOF) {
					t.Fatalf("truncated client read=%v", readErr)
				}
			} else if readErr != nil {
				t.Fatalf("complete client read=%v", readErr)
			}

			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			awaitDirectStreamSignal(t, originDone, "direct origin handler")
			select {
			case err := <-originErr:
				t.Fatal(err)
			default:
			}
			finish()
			// Recorder response bytes count successful downstream Write returns.
			// A text/plain buffered tail may be discarded when HTTP aborts; it is
			// valid for the client to receive less than this accepted-byte count.
			assertDirectStreamObservationEvent(t, app, origins.Load(), tc.status, tc.kind, int64(len(prefix)))
			if tc.compose {
				entries, err := os.ReadDir(app.PublicCache.tempDir())
				if err != nil || len(entries) != 0 {
					t.Fatalf("terminal cache temp entries=%d error=%v", len(entries), err)
				}
				if err := filepath.WalkDir(app.PublicCache.cacheDir(), func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !entry.IsDir() {
						return fmt.Errorf("truncated cache left file %s", filepath.Base(path))
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				stats, err := app.publicCacheStorageStats(t.Context())
				if err != nil || stats.EntriesUsed != 0 || stats.DiskBytesUsed != 0 || stats.MemoryBytesUsed != 0 {
					t.Fatalf("truncated response committed cache data: stats=%v error=%v", stats, err)
				}
			}
		})
	}
}

func TestPublicDirectStreamingUpgradeRetainsWritableBody(t *testing.T) {
	var origins atomic.Int64
	originDone := make(chan struct{})
	originErr := make(chan error, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(originDone)
		origins.Add(1)
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			originErr <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			originErr <- err
			return
		}
		if _, err := buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: direct-echo\r\n\r\n"); err != nil {
			originErr <- err
			return
		}
		if err := buffer.Flush(); err != nil {
			originErr <- err
			return
		}
		var ping [4]byte
		if _, err := io.ReadFull(buffer, ping[:]); err != nil || string(ping[:]) != "ping" {
			originErr <- fmt.Errorf("upgrade read=%q error=%v", ping, err)
			return
		}
		if _, err := conn.Write([]byte("pong")); err != nil {
			originErr <- err
			return
		}
		var one [1]byte
		if n, err := buffer.Read(one[:]); n != 0 || err != io.EOF {
			originErr <- fmt.Errorf("upgrade Close did not delegate: n=%d error=%v", n, err)
		}
	}))
	t.Cleanup(origin.Close)
	app, address, finish := newDirectStreamObservationFixture(t, origin.URL, false)
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET /assets/stream.txt HTTP/1.1\r\nHost: assets.example.test\r\nConnection: Upgrade\r\nUpgrade: direct-echo\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "direct-echo" {
		t.Fatalf("upgrade status=%d headers=%v", resp.StatusCode, resp.Header)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	var pong [4]byte
	if _, err := io.ReadFull(reader, pong[:]); err != nil || string(pong[:]) != "pong" {
		t.Fatalf("public writable upgrade=%q error=%v", pong, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	awaitDirectStreamSignal(t, originDone, "direct upgrade origin handler")
	select {
	case err := <-originErr:
		t.Fatal(err)
	default:
	}
	finish()
	// Upgrade byte accounting is an existing, separate path. This test binds
	// status/attribution/retry/health without claiming ordinary body byte counts.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var responseBytes int64
	if err := app.DB.QueryRowContext(ctx, `SELECT response_bytes FROM proxy_request_events`).Scan(&responseBytes); err != nil {
		t.Fatal(err)
	}
	assertDirectStreamObservationEvent(t, app, origins.Load(), http.StatusSwitchingProtocols, "", responseBytes)
}

var directStreamBenchmarkSink io.ReadCloser

// Construction/read/Close cost only. This does not measure public HTTP latency,
// transport, cache/shaper work, or end-to-end benefit.
func BenchmarkPublicDirectStreamingObservation(b *testing.B) {
	for _, observed := range []bool{false, true} {
		name := "original"
		if observed {
			name = "observed"
		}
		b.Run(name, func(b *testing.B) {
			ctx := context.Background()
			var buffer [16]byte
			b.ReportAllocs()
			for b.Loop() {
				body := &noRuleTerminalBody{prefix: []byte("prefix"), firstErr: io.EOF, laterErr: io.EOF}
				directStreamBenchmarkSink = body
				kind := ""
				if observed {
					directStreamBenchmarkSink = &publicRetryObservedResponseBody{ReadCloser: body, onError: func(err error) {
						if kind == "" && !requestContextCanceled(ctx, err) {
							kind = publicRetryResponseBodyErrorKind(err)
						}
					}}
				}
				n, err := directStreamBenchmarkSink.Read(buffer[:])
				if n != 6 || err != io.EOF {
					b.Fatal("unexpected benchmark body")
				}
				if err := directStreamBenchmarkSink.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
