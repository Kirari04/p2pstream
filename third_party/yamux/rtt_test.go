package yamux

import (
	"sync"
	"testing"
	"time"
)

func TestRTTEstimatorBoundsOutliersAndAges(t *testing.T) {
	base := time.Unix(100, 0)
	var estimator rttEstimator
	if estimator.observe(10*time.Second, base) {
		t.Fatal("accepted a sample above the hard bound")
	}
	if !estimator.observe(100*time.Microsecond, base) {
		t.Fatal("rejected the first valid sample")
	}
	// A queued one-second Ping must not turn a LAN estimate into a large
	// flow-control target. The lower envelope retains the successful baseline.
	if !estimator.observe(time.Second, base.Add(time.Second)) {
		t.Fatal("rejected a bounded outlier")
	}
	if got := estimator.value; got > 125*time.Microsecond {
		t.Fatalf("outlier moved estimate to %v, want at most 125us", got)
	}
	if got := estimator.valueAt(base.Add(time.Second + rttSampleStaleAfter - time.Nanosecond)); got == 0 {
		t.Fatal("sample aged before the staleness deadline")
	}
	if got := estimator.valueAt(base.Add(time.Second + rttSampleStaleAfter)); got != 0 {
		t.Fatalf("stale estimate = %v, want zero", got)
	}
}

func TestRTTEstimatorUsesWindowedMinimumAgainstQueuedPings(t *testing.T) {
	base := time.Unix(300, 0)
	var estimator rttEstimator
	samples := []time.Duration{
		82 * time.Millisecond,
		103 * time.Millisecond,
		129 * time.Millisecond,
		149 * time.Millisecond,
		171 * time.Millisecond,
		191 * time.Millisecond,
		267 * time.Millisecond,
	}
	for i, sample := range samples {
		if !estimator.observe(sample, base.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("sample %d rejected", i)
		}
	}
	if got := estimator.value; got != samples[0] {
		t.Fatalf("queued samples raised estimate to %v, want %v", got, samples[0])
	}
}

func TestRTTEstimatorRebasesAfterMinimumAges(t *testing.T) {
	base := time.Unix(400, 0)
	var estimator rttEstimator
	if !estimator.observe(80*time.Millisecond, base) {
		t.Fatal("rejected baseline sample")
	}
	if !estimator.observe(160*time.Millisecond, base.Add(time.Second)) {
		t.Fatal("rejected path-change sample")
	}
	if got := estimator.valueAt(base.Add(rttSampleStaleAfter - time.Nanosecond)); got != 80*time.Millisecond {
		t.Fatalf("baseline aged too early: %v", got)
	}
	if got := estimator.valueAt(base.Add(rttSampleStaleAfter)); got != 160*time.Millisecond {
		t.Fatalf("path-change sample did not become baseline: %v", got)
	}
}

func TestRTTEstimatorDoesNotMoveTimestampBackwards(t *testing.T) {
	base := time.Unix(200, 0)
	var estimator rttEstimator
	if !estimator.observe(80*time.Millisecond, base.Add(time.Second)) {
		t.Fatal("rejected first sample")
	}
	if !estimator.observe(80*time.Millisecond, base) {
		t.Fatal("rejected sample after clock rollback")
	}
	if got := estimator.valueAt(base.Add(time.Second + rttSampleStaleAfter - time.Nanosecond)); got == 0 {
		t.Fatal("clock rollback shortened sample lifetime")
	}
}

func TestRequestRTTSuccess(t *testing.T) {
	clientConn, serverConn := testConnPipe(t)
	client, _ := testClientServerConfig(t, clientConn, serverConn, testConfNoKeepAlive(), testConfNoKeepAlive())
	client.RequestRTT()
	waitForRTT(t, client, time.Second)
	client.pingLock.Lock()
	pings := client.pingID
	left := len(client.pings)
	client.pingLock.Unlock()
	if pings != 1 {
		t.Fatalf("ping count = %d, want 1", pings)
	}
	if left != 0 {
		t.Fatalf("pending pings = %d, want zero", left)
	}
}

func TestRequestRTTIsSessionSingleFlight(t *testing.T) {
	clientConn, serverConn := testConnPipe(t)
	client, _ := testClientServerConfig(t, clientConn, serverConn, testConfNoKeepAlive(), testConfNoKeepAlive())
	var workers sync.WaitGroup
	for i := 0; i < 100; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			client.RequestRTT()
		}()
	}
	workers.Wait()
	waitForRTT(t, client, time.Second)
	client.pingLock.Lock()
	pings := client.pingID
	client.pingLock.Unlock()
	if pings != 1 {
		t.Fatalf("concurrent RequestRTT sent %d pings, want 1", pings)
	}
}

func TestRequestRTTFailureCleansProbeAndPendingPing(t *testing.T) {
	clientConn, serverConn := testConnPipe(t)
	clientConf := testConfNoKeepAlive()
	serverConf := testConfNoKeepAlive()
	clientConf.ConnectionWriteTimeout = 25 * time.Millisecond
	serverConf.ConnectionWriteTimeout = 25 * time.Millisecond
	client, server := testClientServerConfig(t, clientConn, serverConn, clientConf, serverConf)
	clientPipe := client.conn.(*pipeConn)
	clientPipe.writeBlocker.Lock()
	server.RequestRTT()
	waitForProbeState(t, server, false, time.Second)

	server.pingLock.Lock()
	left := len(server.pings)
	server.pingLock.Unlock()
	if left != 0 {
		t.Fatalf("pending pings after failed RequestRTT = %d, want zero", left)
	}

	// The failed asynchronous probe must leave the gate reusable. Clear only
	// the one-second admission throttle so this test can immediately exercise
	// the recovery path.
	server.rttMu.Lock()
	server.rttProbed = time.Time{}
	server.rttMu.Unlock()
	clientPipe.writeBlocker.Unlock()
	server.RequestRTT()
	waitForRTT(t, server, time.Second)
}

func TestKeepaliveAndRequestRTTShareProbeGate(t *testing.T) {
	clientConn, serverConn := testConnPipe(t)
	client, _ := testClientServerConfig(t, clientConn, serverConn, testConfNoKeepAlive(), testConfNoKeepAlive())
	if !client.beginRTTProbe(false) {
		t.Fatal("failed to claim RTT probe gate")
	}
	if _, _, ran := client.runRTTProbe(true); ran {
		t.Fatal("keepalive probe bypassed request probe gate")
	}
	client.endRTTProbe()
}

func waitForRTT(t *testing.T, session *Session, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if session.RTT() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("RTT sample did not arrive within %v", timeout)
}

func waitForProbeState(t *testing.T, session *Session, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		session.rttMu.Lock()
		got := session.rttProbe
		session.rttMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("RTT probe state did not become %t within %v", want, timeout)
}
