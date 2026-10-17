//go:build linux && p2pstream_qualification

package agent

import (
	"crypto/tls"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

// This fixture controls scheduling only. Its TCP snapshots are simulated using
// the existing threshold-history fixtures; it does not reproduce a kernel
// RawConn.Control failure or identify the plaintext of the preserved WAN run.
type qualificationCancellationPipe struct {
	net.Conn
	armed        bool // Set before starting Yamux, after both handshakes join.
	blockWrite   bool
	writeEntered chan struct{}
	closed       chan struct{}
	writeOnce    sync.Once
	closeOnce    sync.Once
}

func (conn *qualificationCancellationPipe) Write(p []byte) (int, error) {
	if !conn.armed {
		return conn.Conn.Write(p)
	}
	conn.writeOnce.Do(func() { close(conn.writeEntered) })
	if conn.blockWrite {
		<-conn.closed
		return 0, net.ErrClosed
	}
	return conn.Conn.Write(p)
}

func (conn *qualificationCancellationPipe) Close() error {
	err := conn.Conn.Close()
	conn.closeOnce.Do(func() { close(conn.closed) })
	return err
}

// The embedded body preserves the real qualification observer interfaces.
// closed marks body/physical Close completion, NOT Session.Close completion:
// Session.Close is still joining the send loop while after-capture is held.
type qualificationCancellationBody struct {
	*qualificationObservedTunnelBody
	closed chan struct{}
	once   sync.Once
}

func (body *qualificationCancellationBody) Close() error {
	err := body.qualificationObservedTunnelBody.Close()
	body.once.Do(func() { close(body.closed) })
	return err
}

func TestQualificationTCPThresholdHistorySessionCloseCancellation(t *testing.T) {
	for _, blockWrite := range []bool{true, false} {
		name := "completed_raw_write_before_after_capture"
		if blockWrite {
			name = "blocked_raw_write_interrupted_by_close"
		}
		t.Run(name, func(t *testing.T) {
			// Reuse the existing trusted TLS certificate fixture. All tested
			// transport bytes travel through net.Pipe, not this HTTP listener.
			certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			t.Cleanup(certificateServer.Close)
			serverConfig := certificateServer.TLS.Clone()
			clientConfig := certificateServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			clientConfig.ServerName = "example.com"
			clientPipe, serverPipe := net.Pipe()
			raw := &qualificationCancellationPipe{
				Conn: clientPipe, blockWrite: blockWrite,
				writeEntered: make(chan struct{}), closed: make(chan struct{}),
			}
			provider := &qualificationTCPThresholdTestProvider{}
			owner := qualificationTCPThresholdTestOwner(t, 1, math.MaxInt32, provider)
			observed := &qualificationObservedRawConn{Conn: raw, owner: owner}
			clientTLS := tls.Client(observed, clientConfig)
			serverTLS := tls.Server(serverPipe, serverConfig)
			afterEntered, releaseAfter := make(chan struct{}), make(chan struct{})
			var releaseOnce, closeSessionOnce sync.Once
			var session *yamux.Session
			var workers []<-chan struct{}
			launch := func(fn func()) <-chan struct{} {
				done := make(chan struct{})
				workers = append(workers, done)
				go func() { defer close(done); fn() }()
				return done
			}
			closeResult := make(chan error, 1)
			var closeDone <-chan struct{}
			startClose := func() {
				closeSessionOnce.Do(func() {
					closeDone = launch(func() { closeResult <- session.Close() })
				})
			}
			await := func(done <-chan struct{}, label string) {
				t.Helper()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s did not complete", label)
				}
			}
			t.Cleanup(func() {
				// Failure-only escape from fixture barriers; successful assertions
				// below require all workers to join before cleanup runs.
				releaseOnce.Do(func() { close(releaseAfter) })
				_ = raw.Close()
				_ = serverPipe.Close()
				if session != nil {
					startClose()
				}
				for _, done := range workers {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("fixture worker failed to join during failure cleanup")
					}
				}
			})

			// Deadlines are failure bounds only; the tested schedules use channels.
			deadline := time.Now().Add(5 * time.Second)
			if err := clientPipe.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if err := serverPipe.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			handshakeResult := make(chan error, 1)
			handshakeDone := launch(func() { handshakeResult <- serverTLS.Handshake() })
			if err := clientTLS.Handshake(); err != nil {
				t.Fatal(err)
			}
			await(handshakeDone, "server handshake")
			if err := <-handshakeResult; err != nil {
				t.Fatal(err)
			}
			if err := clientPipe.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := serverPipe.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}

			// No session or TLS write is active at this boundary. Use a fresh
			// owner so handshake records cannot obscure the single control write.
			owner = qualificationTCPThresholdTestOwner(t, 1, math.MaxInt32, provider)
			observed.owner = owner
			captures := 0 // Only the session's single send worker calls capture.
			owner.tcp.thresholdHistory.capture = func(_ syscall.RawConn) (qualificationTCPInfoSnapshot, error) {
				captures++
				if captures == 1 {
					return qualificationTCPThresholdTestSnapshot(math.MaxInt32), nil
				}
				if captures == 2 {
					close(afterEntered)
					<-releaseAfter
				}
				// Explicitly simulated failed after-capture: no getsockopt and
				// no algorithm value. This is not kernel failure evidence.
				now := time.Now()
				return qualificationTCPInfoSnapshot{
					SchemaVersion: qualificationTCPInfoSnapshotSchemaVersion,
					FailureStage:  "raw_control", captureStarted: now, captureFinished: now,
					CaptureStartedUnixNS: now.UnixNano(), CaptureFinishedUnixNS: now.UnixNano(),
				}, net.ErrClosed
			}
			raw.armed = true
			body := &qualificationCancellationBody{
				qualificationObservedTunnelBody: &qualificationObservedTunnelBody{ReadWriteCloser: clientTLS, owner: owner},
				closed:                          make(chan struct{}),
			}
			config, err := tunnel.NewAggregateYamuxConfig(nil, 1<<20, 1)
			if err != nil {
				t.Fatal(err)
			}
			config.EnableKeepAlive = false // Isolate the one explicit header-only frame.
			config.LogOutput = io.Discard
			session, err = yamux.Client(body, config)
			if err != nil {
				t.Fatal(err)
			}
			peerResult := make(chan struct {
				n   int
				err error
			}, 1)
			peerDone := launch(func() {
				var header [12]byte
				n, err := io.ReadFull(serverTLS, header[:])
				peerResult <- struct {
					n   int
					err error
				}{n, err}
			})
			goAwayResult := make(chan error, 1)
			goAwayDone := launch(func() { goAwayResult <- session.GoAway() })
			await(raw.writeEntered, "raw control-write entry")
			if !blockWrite {
				await(afterEntered, "completed raw write before after-capture")
				await(peerDone, "peer control-frame read")
				result := <-peerResult
				if result.n != 12 || result.err != nil {
					t.Fatalf("peer read = %d, %v", result.n, result.err)
				}
			}
			startClose()
			await(body.closed, "body/physical Close")
			await(afterEntered, "after-capture entry")
			select {
			case <-closeDone:
				t.Fatal("Session.Close returned before its held send worker joined")
			default:
			}
			releaseOnce.Do(func() { close(releaseAfter) })
			await(closeDone, "Session.Close send/recv/ping-reply/aggregate joins")
			if err := <-closeResult; err != nil {
				t.Fatal(err)
			}
			await(goAwayDone, "GoAway caller")
			goAwayErr := <-goAwayResult
			if blockWrite && goAwayErr == nil {
				t.Fatal("interrupted GoAway unexpectedly succeeded")
			}
			// A completed write's caller may observe shutdown before its success
			// notification. The raw ledger, not this select race, owns the result.
			await(peerDone, "peer reader")
			if blockWrite {
				result := <-peerResult
				if result.n != 0 || result.err == nil {
					t.Fatalf("interrupted peer read = %d, %v", result.n, result.err)
				}
			}
			if err := qualificationRetireRawWriteSession(session); err != nil {
				t.Fatal(err)
			}
			activity := session.DrainQualificationRawWriteActivity()
			history := activity.TCPState.ThresholdHistory
			failure := history.Failure
			if !activity.CloseObserved || !activity.SessionJoined || activity.ActiveRawWrites != 0 || activity.ActiveEnvelopes != 0 {
				t.Fatalf("unjoined raw ownership: %+v", activity)
			}
			if !history.Closed || history.ActiveSample || !history.Inconclusive || history.FailureReason != "close_race" ||
				history.TransitionObserved || captures != 2 || history.ProviderCaptureErrors != 1 || !failure.Present {
				t.Fatalf("cancellation history = %+v; captures = %d", history, captures)
			}
			if failure.After.Captured || failure.After.CongestionControlAttempted || failure.After.CongestionControl != "" ||
				failure.After.TCPInfoAttempted || failure.After.FailureStage != "raw_control" || failure.BracketValid {
				t.Fatalf("failed after-capture became evidence: %+v", failure)
			}
			if !failure.Before.Captured || failure.Before.CongestionControl != "cubic" ||
				activity.TCPState.ActiveSamples != 0 || activity.TCPState.PendingRawClaims != 0 || activity.TCPState.ActiveRawPairs != 0 {
				t.Fatalf("capture ownership or successful before-sample lost: %+v", activity.TCPState)
			}
			outside := activity.Outside
			if outside.Calls != 1 || outside.AttemptBytes == 0 || failure.AttemptedBytes != outside.AttemptBytes ||
				failure.WrittenBytes != outside.WrittenBytes || failure.EnvelopeAttributed {
				t.Fatalf("single header-only raw write lost: outside=%+v failure=%+v", outside, failure)
			}
			if blockWrite {
				if outside.ErrorCalls != 1 || outside.WrittenBytes != 0 || !failure.WriteError {
					t.Fatalf("interrupted raw error erased: outside=%+v failure=%+v", outside, failure)
				}
			} else if outside.ErrorCalls != 0 || outside.SuccessCalls != 1 || outside.WrittenBytes != outside.AttemptBytes || failure.WriteError {
				t.Fatalf("successful raw write changed by after-capture failure: outside=%+v failure=%+v", outside, failure)
			}
			// This forced overlap closes while the observer is active. Preserve
			// its existing conservative invalidation; do not demand WAN flags.
			if !activity.Ambiguous || !activity.TCPState.Invalid || activity.Complete {
				t.Fatalf("close-overlap evidence incorrectly healthy: %+v", activity)
			}
			for _, done := range workers {
				await(done, "natural fixture worker join")
			}
		})
	}
}
