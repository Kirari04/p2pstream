package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"p2pstream/internal/sysmetrics"
)

type requestOwnershipBody struct {
	io.Reader
	closes atomic.Int64
}

func (b *requestOwnershipBody) Close() error { b.closes.Add(1); return nil }

func requestOwnershipApp(t *testing.T, limit int64) *App {
	t.Helper()
	app := NewApp(nil, nil)
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "request-ownership-test"}
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 16, &usage)
	app.retryReplayBudget = newRetryReplayBudget(limit)
	return app
}

func assertRequestOwnershipReservation(t *testing.T, app *App, want, wantWorking int64) {
	t.Helper()
	s := app.agentStreamCapacity.snapshot()
	wantOwners := int64(0)
	if want > 0 {
		wantOwners = 1
	}
	if got := app.retryReplayBudget.used.Load(); got != want || s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer] != wantWorking || s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReplayBuffer] != wantOwners {
		t.Fatalf("request replay ownership: budget=%d bytes=%d owners=%d, want %d/%d/%d", got, s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer], s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReplayBuffer], want, wantWorking, wantOwners)
	}
}

// Unknown-length input keeps the bounded prefix alive in a MultiReader even
// after it is classified as non-replayable. Its reservation must follow that
// retained prefix, including after the attempt body is closed by a transport.
func TestPreparePublicRetryRequestBodyRetainsUnknownOversizeReservation(t *testing.T) {
	const limit = int64(8192)
	app := requestOwnershipApp(t, limit)
	payload := bytes.Repeat([]byte{0, 1, 0x7f, 0xff, 0x3a}, 2200)
	original := &requestOwnershipBody{Reader: bytes.NewReader(payload)}
	req, err := http.NewRequest(http.MethodPost, "http://proxy.test/upload", original)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	body, err := preparePublicRetryRequestBody(app, req, &publicRetryRuleConfig{BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	defer body.close()
	if body.replayable || body.bodyless || len(body.buffered) != 0 {
		t.Fatalf("oversize input became replayable: %+v", body)
	}
	assertRequestOwnershipReservation(t, app, limit, 24579)
	if release, ok := app.tryAcquireRetryReplayBudget(1); ok {
		release()
		t.Fatal("competing reservation spent the retained oversize prefix budget")
	}
	attempt, ok := body.next()
	if !ok {
		t.Fatal("first oversize attempt unavailable")
	}
	got, err := io.ReadAll(attempt)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("prefix plus tail reconstruction: bytes=%d/%d equal=%t err=%v", len(got), len(payload), bytes.Equal(got, payload), err)
	}
	if err := attempt.Close(); err != nil {
		t.Fatal(err)
	}
	assertRequestOwnershipReservation(t, app, limit, 24579)
	if original.closes.Load() != 0 {
		t.Fatal("attempt closed the retry owner before terminal cleanup")
	}
	body.close()
	body.close()
	assertRequestOwnershipReservation(t, app, 0, 0)
	if original.closes.Load() != 1 {
		t.Fatalf("original closes=%d, want exactly one", original.closes.Load())
	}
	release, ok := app.tryAcquireRetryReplayBudget(limit)
	if !ok {
		t.Fatal("terminal cleanup did not restore the entire replay budget")
	}
	release()
	release()
	assertRequestOwnershipReservation(t, app, 0, 0)
}

func TestPublicAgentRetryUnknownOversizeRefundsTerminalPaths(t *testing.T) {
	for _, terminal := range []string{"response-close", "cancel", "tail-error"} {
		t.Run(terminal, func(t *testing.T) {
			const limit = int64(8192)
			app, snapshot, target, first, second := newPublicRetryTestApp(t)
			owned := requestOwnershipApp(t, limit)
			app.agentStreamCapacity, app.retryReplayBudget = owned.agentStreamCapacity, owned.retryReplayBudget
			payload := bytes.Repeat([]byte{0xff, 0, 0x61}, 3100)
			var reader io.Reader = bytes.NewReader(payload)
			if terminal == "tail-error" {
				reader = io.MultiReader(reader, publicRetryResponseErrorReader{err: io.ErrUnexpectedEOF})
			}
			original := &requestOwnershipBody{Reader: reader}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://proxy.test/upload", original)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = -1
			attempts := 0
			result := &publicRetryAttemptResult{}
			rt := &publicAgentAttemptRoundTripper{
				app: app, snapshot: snapshot, resolution: publicRouteResolution{Snapshot: snapshot, Target: target}, initial: first,
				rule: &publicRetryRuleConfig{ID: 1, MaxRetries: 1, FailureMode: publicRetryFailureModePreResponseFailures, BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: limit,
					RetryStatusCodes: []int64{503}, retryStatusCodeSet: map[int]struct{}{503: {}}},
				result: result,
				transportForAgent: func(agent *AgentConn) http.RoundTripper {
					return retryRoundTripFunc(func(attempt *http.Request) (*http.Response, error) {
						attempts++
						if agent != first || attempts != 1 {
							t.Fatal("consumed non-replayable POST reached another attempt")
						}
						assertRequestOwnershipReservation(t, app, limit, 24579)
						got, readErr := io.ReadAll(attempt.Body)
						wantErr := error(nil)
						if terminal == "tail-error" {
							wantErr = io.ErrUnexpectedEOF
						}
						if !bytes.Equal(got, payload) || !errors.Is(readErr, wantErr) {
							t.Fatalf("attempt body equal=%t err=%v want=%v", bytes.Equal(got, payload), readErr, wantErr)
						}
						_ = attempt.Body.Close()
						assertRequestOwnershipReservation(t, app, limit, 24579)
						switch terminal {
						case "cancel":
							cancel()
							return nil, attempt.Context().Err()
						case "tail-error":
							return nil, readErr
						default:
							return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("retry\n"))), Request: attempt}, nil
						}
					})
				},
			}
			resp, err := rt.RoundTrip(req)
			if terminal == "response-close" {
				if err != nil || resp == nil || resp.StatusCode != 503 || result.Outcome != publicRetryOutcomeSkipped || result.ReplaySkippedReason != "request_body_not_replayable" {
					t.Fatalf("response=%v err=%v result=%+v", resp, err, result)
				}
				assertRequestOwnershipReservation(t, app, limit, 24579)
				_ = resp.Body.Close()
				_ = resp.Body.Close()
			} else {
				want := error(io.ErrUnexpectedEOF)
				if terminal == "cancel" {
					want = context.Canceled
				}
				if resp != nil || !errors.Is(err, want) {
					t.Fatalf("response=%v err=%v want=%v", resp, err, want)
				}
			}
			assertRequestOwnershipReservation(t, app, 0, 0)
			if attempts != 1 || original.closes.Load() != 1 || first.ActiveRequests.Load() != 0 || second.ActiveRequests.Load() != 0 || result.RetryCount != 0 {
				t.Fatalf("terminal ownership attempts=%d closes=%d active=%d/%d result=%+v", attempts, original.closes.Load(), first.ActiveRequests.Load(), second.ActiveRequests.Load(), result)
			}
		})
	}
}

func TestPreparePublicRetryRequestBodyOwnershipControls(t *testing.T) {
	for _, known := range []bool{false, true} {
		name := "buffered-known"
		if !known {
			name = "known-oversize"
		}
		t.Run(name, func(t *testing.T) {
			app := requestOwnershipApp(t, 8192)
			size := 4096
			if !known {
				size = 8193
			}
			payload := bytes.Repeat([]byte{0x9a}, size)
			original := &requestOwnershipBody{Reader: bytes.NewReader(payload)}
			req, err := http.NewRequest(http.MethodPost, "http://proxy.test/upload", original)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = int64(size)
			body, err := preparePublicRetryRequestBody(app, req, &publicRetryRuleConfig{BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: 8192})
			if err != nil {
				t.Fatal(err)
			}
			defer body.close()
			reservation := int64(0)
			if known {
				reservation = int64(size)
			}
			assertRequestOwnershipReservation(t, app, reservation, reservation*3)
			if body.replayable != known || (!known && original.Reader.(*bytes.Reader).Len() != size) {
				t.Fatal("known oversize input was read/buffered, or bounded input lost replayability")
			}
			for attempt := 0; attempt < 1+btoiRequestOwnership(known); attempt++ {
				r, ok := body.next()
				if !ok {
					t.Fatal("prepared body unavailable")
				}
				got, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("control reconstruction equal=%t err=%v", bytes.Equal(got, payload), err)
				}
			}
			body.close()
			body.close()
			assertRequestOwnershipReservation(t, app, 0, 0)
			if original.closes.Load() != 1 {
				t.Fatalf("control original closes=%d", original.closes.Load())
			}
		})
	}
}

func btoiRequestOwnership(value bool) int {
	if value {
		return 1
	}
	return 0
}
