package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRealPublicOversizeReplayPrefixHeldUntilIdle408Cleanup(t *testing.T) {
	const limit = int64(8192)
	f := newRealAgentProxyFixture(t, 1, 512<<10, 0)
	f.app.Config.PublicRequestBodyIdleMillis = 300
	f.app.retryReplayBudget = newRetryReplayBudget(limit)
	prefix := bytes.Repeat([]byte{0x61}, int(limit)+1)
	observed := make(chan error, 1)
	var uploads atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			io.WriteString(w, "ok")
			return
		}
		uploads.Add(1)
		got := make([]byte, len(prefix))
		_, err := io.ReadFull(r.Body, got)
		if err == nil && !bytes.Equal(got, prefix) {
			err = fmt.Errorf("prefix changed")
		}
		observed <- err
		io.Copy(io.Discard, r.Body)
	}))
	defer origin.Close()
	address := startPressurePublicListener(t, f, origin.URL)
	snapshot := *f.app.currentPublicSnapshot()
	snapshot.RetryRules = []publicRetryRuleConfig{{ID: 1, Enabled: true, AllMethods: true, MaxRetries: 1, FailureMode: publicRetryFailureModePreResponseFailures, BodyMode: publicRetryBodyModeBuffered, MaxReplayBodyBytes: limit}}
	setPublicSnapshotForTest(t, f.app, &snapshot)
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprintf(conn, "POST /upload HTTP/1.1\r\nHost: pressure.test\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n", len(prefix)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(prefix); err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "\r\n") // omit final chunk: actual stalled public upload
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("oversize prefix did not reach authenticated origin")
	}
	assertRequestOwnershipReservation(t, f.app, limit, 24579)
	if release, ok := f.app.tryAcquireRetryReplayBudget(1); ok {
		release()
		t.Fatal("retained prefix budget was prematurely reused")
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 408 {
		t.Fatalf("stalled oversize upload status%d, want408", response.StatusCode)
	}
	awaitLaneRetirement(t, "replay prefix/working lease retained after408", func() bool {
		s := f.app.agentStreamCapacity.snapshot()
		return f.app.retryReplayBudget.used.Load() == 0 && s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer] == 0 && s.ActiveLeases == 0
	})
	if uploads.Load() != 1 {
		t.Fatalf("non-replayable upload attempted%d origins", uploads.Load())
	}
	assertRequestOwnershipReservation(t, f.app, 0, 0)
	if n, err := f.roundTrip(t.Context(), "/api", nil); err != nil || n != 2 {
		t.Fatalf("lane did not recover after408: %d %v", n, err)
	}
	f.app.CloseAgentTransports()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f.app.CloseObservabilityRecorder(ctx)
}
