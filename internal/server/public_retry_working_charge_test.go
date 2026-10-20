package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2pstream/internal/sysmetrics"
)

type replayChargeGate struct {
	once sync.Once
	open chan struct{}
}

func newReplayChargeGate() *replayChargeGate { return &replayChargeGate{open: make(chan struct{})} }
func (g *replayChargeGate) release()         { g.once.Do(func() { close(g.open) }) }

type replayChargeRead struct {
	index, length, capacity int
	payload, working        int64
}

type replayChargeStep struct {
	data []byte
	err  error
	gate *replayChargeGate
}

// Close deliberately returns without unblocking Read. This is a local custom
// ReadCloser contract fixture, not an HTTP wire framing or peer exploit claim.
type replayChargeBody struct {
	app      *App
	steps    []replayChargeStep
	gates    []*replayChargeGate
	step     int
	started  chan replayChargeRead
	returned chan int
	reads    atomic.Int64
	closes   atomic.Int64
	closeErr error
}

func newReplayChargeBody(app *App, steps ...replayChargeStep) *replayChargeBody {
	body := &replayChargeBody{app: app, steps: steps, started: make(chan replayChargeRead, 32), returned: make(chan int, 32)}
	for _, step := range steps {
		if step.gate != nil {
			body.gates = append(body.gates, step.gate)
		}
	}
	return body
}

func (b *replayChargeBody) Read(p []byte) (int, error) {
	index := int(b.reads.Add(1))
	s := b.app.agentStreamCapacity.snapshot()
	b.started <- replayChargeRead{index: index, length: len(p), capacity: cap(p), payload: b.app.retryReplayBudget.used.Load(), working: s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer]}
	defer func() { b.returned <- index }()
	if b.step >= len(b.steps) {
		return 0, io.EOF
	}
	step := &b.steps[b.step]
	if step.gate != nil {
		<-step.gate.open
	}
	n := copy(p, step.data)
	step.data = step.data[n:]
	if len(step.data) != 0 {
		return n, nil
	}
	b.step++
	return n, step.err
}

func (b *replayChargeBody) Close() error { b.closes.Add(1); return b.closeErr }

func replayChargeTestApp(t *testing.T, quota int64, pressure bool) *App {
	t.Helper()
	app := NewApp(nil, nil)
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "replay-working-charge"}
	if pressure {
		usage.UsedBytes = 511 << 20
	}
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 16, &usage)
	app.retryReplayBudget = newRetryReplayBudget(quota)
	return app
}

func replayChargeAssert(t *testing.T, app *App, payload, working, owners int64) {
	t.Helper()
	s := app.agentStreamCapacity.snapshot()
	if got := app.retryReplayBudget.used.Load(); got != payload || s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer] != working || s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReplayBuffer] != owners {
		t.Fatalf("replay charge payload=%d working=%d owners=%d want=%d/%d/%d", got, s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer], s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReplayBuffer], payload, working, owners)
	}
}

func replayChargeAwaitZero(t *testing.T, app *App) {
	t.Helper()
	mixedSaturationAwait(t, "replay working owner joined and refunded", func() bool {
		s := app.agentStreamCapacity.snapshot()
		return app.retryReplayBudget.used.Load() == 0 && s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer] == 0 && s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReplayBuffer] == 0
	})
	if err := app.agentStreamCapacity.validateInvariants(); err != nil {
		t.Fatal(err)
	}
}

func replayChargeNextRead(t *testing.T, body *replayChargeBody) replayChargeRead {
	t.Helper()
	select {
	case read := <-body.started:
		return read
	case <-time.After(5 * time.Second):
		t.Fatal("body Read handshake did not arrive")
		return replayChargeRead{}
	}
}

func replayChargeNextReturn(t *testing.T, body *replayChargeBody, want int) {
	t.Helper()
	select {
	case got := <-body.returned:
		if got != want {
			t.Fatalf("Read return index=%d want=%d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("body Read return did not join")
	}
}

func TestPublicRetryRequestWorkingChargeCoversReadAllFloor(t *testing.T) {
	for _, known := range []bool{true, false} {
		name := "known-one-byte"
		if !known {
			name = "unknown-one-byte"
		}
		t.Run(name, func(t *testing.T) {
			app := replayChargeTestApp(t, 1, false)
			original := newReplayChargeBody(app, replayChargeStep{data: []byte("x"), err: io.EOF})
			req, err := http.NewRequest(http.MethodPost, "http://proxy.test/tiny", original)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = -1
			if known {
				req.ContentLength = 1
			}
			body, err := preparePublicRetryRequestBody(app, req, &publicRetryRuleConfig{BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer body.close()
			read := replayChargeNextRead(t, original)
			if read.capacity != 512 || read.payload != 1 || read.working != 1536 {
				t.Fatalf("ReadAll tiny input capacity=%d payload=%d adaptive=%d want=512/1/1536", read.capacity, read.payload, read.working)
			}
			wantRead := 2
			if known {
				wantRead = 1
			}
			if read.length != wantRead || !body.replayable || !bytes.Equal(body.buffered, []byte("x")) || original.closes.Load() != 1 {
				t.Fatalf("tiny request length=%d body=%+v closes=%d", read.length, body, original.closes.Load())
			}
			replayChargeAssert(t, app, 1, 1536, 1)
			body.close()
			body.close()
			replayChargeAwaitZero(t, app)
		})
	}
}

func TestPublicRetryRequestReadBoundAndTerminalErrors(t *testing.T) {
	closeFailure := errors.New("replay charge fixture close failure")
	for _, tc := range []struct {
		name          string
		known, limit  int64
		data          string
		terminal      error
		wantErr       error
		wantBody      string
		wantReplay    bool
		wantReadBytes int
		closeErr      error
	}{
		{"known-local-overread-bound", 1, 8, "xy", io.EOF, nil, "x", true, 1, nil},
		{"known-short", 3, 8, "x", io.EOF, io.ErrUnexpectedEOF, "", false, 3, closeFailure},
		{"known-data-and-error", 1, 8, "x", io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, "", false, 1, closeFailure},
		{"unknown-data-and-error", -1, 1, "x", io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, "", false, 2, closeFailure},
		{"unknown-data-and-cancel", -1, 1, "x", context.Canceled, context.Canceled, "", false, 2, closeFailure},
		{"unknown-prefix-and-tail", -1, 1, "xyz", io.EOF, nil, "xyz", false, 2, nil},
		{"known-small-with-max-rule", 1, math.MaxInt64, "x", io.EOF, nil, "x", true, 1, nil},
		{"known-success-close-error", 1, 8, "x", io.EOF, closeFailure, "", false, 1, closeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := replayChargeTestApp(t, 8, false)
			original := newReplayChargeBody(app, replayChargeStep{data: []byte(tc.data), err: tc.terminal})
			original.closeErr = tc.closeErr
			req, err := http.NewRequest(http.MethodPost, "http://proxy.test/input", original)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = tc.known
			body, err := preparePublicRetryRequestBody(app, req, &publicRetryRuleConfig{BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: tc.limit})
			read := replayChargeNextRead(t, original)
			if read.length != tc.wantReadBytes || !errors.Is(err, tc.wantErr) {
				t.Fatalf("read bound=%d/%d error=%v/%v", read.length, tc.wantReadBytes, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if body != nil {
					t.Fatal("failed preparation exposed a body")
				}
				if original.closes.Load() != 1 {
					t.Fatalf("failed request preparation closes=%d, want exactly one", original.closes.Load())
				}
				replayChargeAwaitZero(t, app)
				return
			}
			defer body.close()
			attempt, ok := body.next()
			if !ok {
				t.Fatal("first attempt unavailable")
			}
			got, err := io.ReadAll(attempt)
			_ = attempt.Close()
			if err != nil || string(got) != tc.wantBody || body.replayable != tc.wantReplay {
				t.Fatalf("request bytes=%q/%q replay=%t/%t err=%v", got, tc.wantBody, body.replayable, tc.wantReplay, err)
			}
			body.close()
			if original.closes.Load() != 1 {
				t.Fatalf("successful request preparation closes=%d, want exactly one", original.closes.Load())
			}
			replayChargeAwaitZero(t, app)
		})
	}
}

type replayChargePrepared struct {
	body publicRetryPreparedResponseBody
	err  error
}

func replayChargeStartResponse(t *testing.T, app *App, body *replayChargeBody, length, limit int64, wait time.Duration) (context.CancelFunc, <-chan replayChargePrepared) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://proxy.test/response", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response := &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: length, Body: body, Request: req}
	results, joined := make(chan replayChargePrepared, 1), make(chan struct{})
	go func() {
		defer close(joined)
		prepared, err := preparePublicRetryResponseBody(app, req, response, &publicRetryRuleConfig{ResponseBodyMode: publicRetryResponseBodyModeBuffered, MaxBufferedResponseBodyBytes: limit, MaxBufferedResponseWait: wait})
		results <- replayChargePrepared{prepared, err}
	}()
	t.Cleanup(func() {
		cancel()
		for _, gate := range body.gates {
			gate.release()
		}
		_ = body.Close()
		mixedSaturationAwaitChannel(t, "response preparation worker joined", joined)
		select {
		case result := <-results:
			if result.body.body != nil {
				_ = result.body.body.Close()
			}
		default:
		}
		replayChargeAwaitZero(t, app)
	})
	return cancel, results
}

func replayChargePreparedResult(t *testing.T, results <-chan replayChargePrepared) replayChargePrepared {
	t.Helper()
	select {
	case result := <-results:
		if result.body.body != nil {
			t.Cleanup(func() { _ = result.body.body.Close() })
		}
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("response preparation did not return")
		return replayChargePrepared{}
	}
}

func TestPublicRetryResponseWorkingChargeCoversPumpRead(t *testing.T) {
	app := replayChargeTestApp(t, 1, false)
	gate := newReplayChargeGate()
	original := newReplayChargeBody(app, replayChargeStep{data: []byte("x"), err: io.EOF, gate: gate})
	_, results := replayChargeStartResponse(t, app, original, 1, 1, time.Second)
	read := replayChargeNextRead(t, original)
	if read.length != 32768 || read.payload != 1 || read.working != 163841 {
		t.Fatalf("response Read buffer=%d payload=%d adaptive=%d want=32768/1/163841", read.length, read.payload, read.working)
	}
	replayChargeAssert(t, app, 1, 163841, 1)
	gate.release()
	result := replayChargePreparedResult(t, results)
	if result.err != nil || !result.body.complete || result.body.skipReason != "" {
		t.Fatalf("tiny response prepared=%+v err=%v", result.body, result.err)
	}
	got, err := io.ReadAll(result.body.body)
	if err != nil || string(got) != "x" {
		t.Fatalf("tiny response bytes=%q err=%v", got, err)
	}
	_ = result.body.body.Close()
	_ = result.body.body.Close()
	replayChargeAwaitZero(t, app)
	if original.closes.Load() != 1 {
		t.Fatalf("source closes=%d, want once before fixture cleanup", original.closes.Load())
	}
}

func TestPublicRetryResponseWorkingChargeWaitsForBlockedReadExit(t *testing.T) {
	for _, terminal := range []string{"timeout-fallback-close", "precommit-cancel"} {
		t.Run(terminal, func(t *testing.T) {
			app := replayChargeTestApp(t, 1, false)
			gate := newReplayChargeGate()
			original := newReplayChargeBody(app, replayChargeStep{err: io.EOF, gate: gate})
			wait := time.Hour
			if terminal == "timeout-fallback-close" {
				wait = 20 * time.Millisecond
			}
			cancel, results := replayChargeStartResponse(t, app, original, -1, 1, wait)
			_ = replayChargeNextRead(t, original)
			if terminal == "precommit-cancel" {
				cancel()
			}
			result := replayChargePreparedResult(t, results)
			if terminal == "precommit-cancel" {
				if !errors.Is(result.err, context.Canceled) || result.body.body != nil {
					t.Fatalf("cancelled preparation=%+v err=%v", result.body, result.err)
				}
			} else {
				if result.err != nil || result.body.complete || result.body.skipReason != "response_buffer_wait_exceeded" {
					t.Fatalf("timeout preparation=%+v err=%v", result.body, result.err)
				}
				closed := make(chan struct{})
				go func() { _ = result.body.body.Close(); close(closed) }()
				t.Cleanup(func() { gate.release(); mixedSaturationAwaitChannel(t, "response Close worker joined", closed) })
				mixedSaturationAwaitChannel(t, "Close returns before blocked source Read", closed)
				_ = result.body.body.Close()
			}
			if original.closes.Load() != 1 {
				t.Fatalf("Close did not return exactly once: %d", original.closes.Load())
			}
			replayChargeAssert(t, app, 0, 163841, 1)
			select {
			case <-original.returned:
				t.Fatal("source Read exited before its gate")
			default:
			}
			gate.release()
			replayChargeNextReturn(t, original, 1)
			replayChargeAwaitZero(t, app)
		})
	}
}

func TestPublicRetryResponseQueuedChunksPreserveTerminalSemantics(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"eof", io.EOF}, {"error", io.ErrUnexpectedEOF}, {"cancel-error", context.Canceled}, {"consumer-close", io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := replayChargeTestApp(t, 1, false)
			blocks := [3][]byte{bytes.Repeat([]byte{0x31}, 32768), bytes.Repeat([]byte{0x7f}, 32768), bytes.Repeat([]byte{0xff}, 32768)}
			original := newReplayChargeBody(app,
				replayChargeStep{data: blocks[0]}, replayChargeStep{data: blocks[1]}, replayChargeStep{data: blocks[2], err: tc.err})
			_, results := replayChargeStartResponse(t, app, original, -1, 1, time.Second)
			result := replayChargePreparedResult(t, results)
			if result.err != nil || result.body.complete || result.body.skipReason != "response_body_too_large" {
				t.Fatalf("oversize preparation=%+v err=%v", result.body, result.err)
			}
			// First chunk belongs to the returned prefix/tail, second fills the
			// one-slot queue, third has returned and cannot advance past its
			// copy/send to that full queue. No consumer Read starts until every
			// source Read-return handshake has arrived.
			for i := 1; i <= 3; i++ {
				read := replayChargeNextRead(t, original)
				if read.index != i || read.length != 32768 {
					t.Fatalf("queue Read=%+v want index=%d/len32768", read, i)
				}
				replayChargeNextReturn(t, original, i)
			}
			pump := result.body.body.(*publicRetryPrefixedResponseBody).source.(*publicRetryResponsePump)
			if len(pump.chunks) != 1 || original.reads.Load() != 3 {
				t.Fatalf("pending-send queue=%d reads=%d", len(pump.chunks), original.reads.Load())
			}
			replayChargeAssert(t, app, 1, 163841, 1)
			if tc.name == "consumer-close" {
				_ = result.body.body.Close()
				replayChargeAwaitZero(t, app)
				if original.reads.Load() != 3 || original.closes.Load() != 1 {
					t.Fatalf("pending send cleanup reads=%d closes=%d", original.reads.Load(), original.closes.Load())
				}
				return
			}
			got, err := io.ReadAll(result.body.body)
			wantErr := tc.err
			if errors.Is(wantErr, io.EOF) {
				wantErr = nil
			}
			want := bytes.Join(blocks[:], nil)
			if !bytes.Equal(got, want) || !errors.Is(err, wantErr) {
				t.Fatalf("queued reconstruction equal=%t bytes=%d/%d err=%v/%v", bytes.Equal(got, want), len(got), len(want), err, wantErr)
			}
			_ = result.body.body.Close()
			replayChargeAwaitZero(t, app)
		})
	}
}

func TestPublicRetryResponseFramingAndDataWithTerminalError(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		known      int64
		terminal   error
		wantErr    error
		wantText   string
	}{
		{"known-exact-eof", "x", 1, io.EOF, nil, ""},
		{"unknown-exact-eof", "x", -1, io.EOF, nil, ""},
		{"known-truncated", "x", 3, io.EOF, io.ErrUnexpectedEOF, ""},
		{"known-data-and-error", "x", 1, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, ""},
		{"unknown-data-and-error", "x", -1, io.ErrUnexpectedEOF, io.ErrUnexpectedEOF, ""},
		{"known-local-contract-overread", "xy", 1, io.EOF, nil, "exceeded Content-Length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := replayChargeTestApp(t, 8, false)
			original := newReplayChargeBody(app, replayChargeStep{data: []byte(tc.data), err: tc.terminal})
			_, results := replayChargeStartResponse(t, app, original, tc.known, 8, time.Hour)
			result := replayChargePreparedResult(t, results)
			if tc.wantErr != nil || tc.wantText != "" {
				if (tc.wantErr != nil && !errors.Is(result.err, tc.wantErr)) || (tc.wantText != "" && (result.err == nil || !strings.Contains(result.err.Error(), tc.wantText))) || result.body.body != nil {
					t.Fatalf("response framing body=%+v error=%v want=%v/%s", result.body, result.err, tc.wantErr, tc.wantText)
				}
			} else {
				if result.err != nil || !result.body.complete {
					t.Fatalf("exact body=%+v err=%v", result.body, result.err)
				}
				got, err := io.ReadAll(result.body.body)
				if string(got) != tc.data || err != nil {
					t.Fatalf("exact response bytes=%q/%q err=%v", got, tc.data, err)
				}
				_ = result.body.body.Close()
			}
			replayChargeAwaitZero(t, app)
		})
	}
}

func TestPublicRetryWorkingChargeDenialPrecedesBodyRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response bool
		known    int64
		limit    int64
		quota    int64
		pressure bool
	}{
		{"request-unknown-probe-overflow", false, -1, math.MaxInt64, math.MaxInt64, false},
		{"request-known-charge-overflow", false, math.MaxInt64/3 + 1, math.MaxInt64, math.MaxInt64, false},
		{"request-quota", false, -1, 2, 1, false},
		{"request-resource-pressure", false, -1, 1, 1, true},
		{"response-unknown-charge-overflow", true, -1, math.MaxInt64, math.MaxInt64, false},
		{"response-known-charge-overflow", true, math.MaxInt64, math.MaxInt64, math.MaxInt64, false},
		{"response-quota", true, -1, 2, 1, false},
		{"response-resource-pressure", true, -1, 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := replayChargeTestApp(t, tc.quota, tc.pressure)
			original := newReplayChargeBody(app, replayChargeStep{data: []byte("x"), err: io.EOF})
			req, err := http.NewRequest(http.MethodPost, "http://proxy.test/denied-buffer", original)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = tc.known
			if tc.response {
				req.Method, req.Body = http.MethodGet, nil
				response := &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: tc.known, Body: original, Request: req}
				prepared, err := preparePublicRetryResponseBody(app, req, response, &publicRetryRuleConfig{ResponseBodyMode: publicRetryResponseBodyModeBuffered, MaxBufferedResponseBodyBytes: tc.limit})
				if err != nil || prepared.body != original || prepared.complete || prepared.skipReason != "response_buffer_budget_exhausted" {
					t.Fatalf("response denial prepared=%+v err=%v", prepared, err)
				}
			} else {
				prepared, err := preparePublicRetryRequestBody(app, req, &publicRetryRuleConfig{BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: tc.limit})
				if err != nil || prepared.original != original || prepared.replayable {
					t.Fatalf("request denial prepared=%+v err=%v", prepared, err)
				}
				prepared.close()
			}
			if original.reads.Load() != 0 {
				t.Fatalf("denied reservation launched a source Read/pump: reads=%d", original.reads.Load())
			}
			replayChargeAssert(t, app, 0, 0, 0)
		})
	}
}
