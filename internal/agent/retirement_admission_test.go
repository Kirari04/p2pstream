package agent

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

// Hold the physical close before it can unblock Yamux receive cleanup. The
// connector, not this test, must close/join the session and refund its owners.
type retirementAdmissionCloseGate struct {
	io.ReadWriteCloser
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
	err     error
}

func (c *retirementAdmissionCloseGate) Close() error {
	c.once.Do(func() {
		close(c.entered)
		<-c.proceed
		c.err = c.ReadWriteCloser.Close()
	})
	return c.err
}

type retirementAdmissionTransport struct {
	gate  *retirementAdmissionCloseGate
	peers chan *yamux.Session
}

func (p *retirementAdmissionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := rollbackUpgradeResponse(r, true, tunnel.TunnelLaneRoleBulk, true, p.peers)
	if err != nil {
		return nil, err
	}
	p.gate.ReadWriteCloser = response.Body.(io.ReadWriteCloser)
	response.Body = p.gate
	return response, nil
}

func TestAggregateConnectorRetirementFencesCompetingSessionAdmission(t *testing.T) {
	flow := tunnel.DefaultAggregateFlowControlConfig()
	poolBytes := int64(yamux.AggregateReceiveWindowMemory(flow.InitialSessionWindow))
	sessionBytes, err := tunnel.AggregateSessionResourceBytes(flow.InitialSessionWindow,
		tunnel.AggregateAcceptBacklogStreams+tunnel.AggregateRejectedStreamLimit)
	if err != nil {
		t.Fatal(err)
	}
	metadataBytes := sessionBytes - poolBytes
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	// Keep the sample constant. Round to the controller's admission unit so
	// exactly one complete session promise fits, without attributing any RSS.
	charge := config.EstimatedBytesPerAdmission
	headroom := ((sessionBytes + charge - 1) / charge) * charge
	const limit = int64(4 << 30)
	hardBytes := (limit/100)*config.HardLimitPercent + (limit%100)*config.HardLimitPercent/100
	usage := sysmetrics.MemoryUsage{UsedBytes: hardBytes - headroom, LimitBytes: limit, Source: "test-fixed-retirement-budget"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.freeOSMemory = func() {}
	capacity.forceRefresh()

	ctx, cancel := context.WithCancel(t.Context())
	gate := &retirementAdmissionCloseGate{entered: make(chan struct{}), proceed: make(chan struct{})}
	transport := &retirementAdmissionTransport{gate: gate, peers: make(chan *yamux.Session, 1)}
	observations := make(chan tunnelSessionObservation, 2)
	negotiation := &tunnelGroupNegotiation{}
	negotiation.aggregate.Store(true)
	negotiation.known.Store(true)
	registration := tunnelLaneRegistration{group: "retirement-admission", lane: 1, negotiation: negotiation,
		sessionObserver: func(event tunnelSessionObservation) { observations <- event }}
	done := make(chan struct{})
	var unblock sync.Once
	releaseClose := func() { unblock.Do(func() { close(gate.proceed) }) }
	t.Cleanup(func() {
		cancel()
		releaseClose()
		select {
		case <-done:
		case <-time.After(reconnectWorkloadWait):
			t.Error("connector cleanup did not join")
		}
		select {
		case peer := <-transport.peers:
			_ = peer.Close()
		default:
		}
	})
	go func() {
		defer close(done)
		_ = connectAndServe(ctx, &http.Client{Transport: transport}, "https://management.test/agent/tunnel",
			"retirement-agent", "retirement-agent", "token", nil, 2<<20, 64, false, capacity, registration)
	}()
	var connected tunnelSessionObservation
	select {
	case connected = <-observations:
	case <-done:
		t.Fatal("connector stopped before session publication")
	case <-time.After(reconnectWorkloadWait):
		t.Fatal("connector did not publish its aggregate session")
	}
	if !connected.Connected || !connected.Aggregate || connected.Lane != 1 {
		t.Fatalf("unexpected connection boundary: %+v", connected)
	}
	assertOwned := func() {
		t.Helper()
		s := capacity.snapshot()
		if s.SessionResourceBytes != sessionBytes || s.AggregateInitialPoolBytes != poolBytes ||
			s.AggregateSessionMetadataBytes != metadataBytes || s.AggregateGrowthPoolBytes != 0 ||
			s.ReceiveWindowBytes != 0 || s.InUse != 0 || s.ControlReservedSlots != 0 || s.MemoryUsedBytes != usage.UsedBytes {
			t.Fatalf("closing session lost or changed ownership: %+v", s)
		}
	}
	assertDenied := func() {
		t.Helper()
		if release, ok := capacity.tryReserveAggregateSessionResource(poolBytes, metadataBytes); ok {
			release()
			t.Fatal("replacement session spent the closing session's receive promise")
		}
		assertOwned()
	}
	assertOwned()
	assertDenied()
	cancel()
	select {
	case <-gate.entered:
	case <-time.After(reconnectWorkloadWait):
		t.Fatal("retirement did not enter the physical close barrier")
	}
	if !connected.Session.IsClosed() {
		t.Fatal("close barrier did not follow Yamux shutdown initiation")
	}
	select {
	case <-done:
		t.Fatal("connector returned before physical close completed")
	default:
	}
	// This call races an actual production connector retirement that is known
	// to be inside Close; topology's IsClosed flag must not become fresh credit.
	assertDenied()
	releaseClose()
	select {
	case disconnected := <-observations:
		if disconnected.Connected || disconnected.Session != connected.Session {
			t.Fatalf("unexpected disconnect boundary: %+v", disconnected)
		}
	case <-time.After(reconnectWorkloadWait):
		t.Fatal("connector did not publish its post-release disconnect")
	}
	select {
	case <-done:
	case <-time.After(reconnectWorkloadWait):
		t.Fatal("connector did not finish after physical close")
	}
	if stats := connected.Session.AggregateReceiveWindowStats(); !stats.Closed || stats.TotalUnits != 0 || stats.BufferedUnits != 0 || stats.PendingRefund != 0 {
		t.Fatalf("closed aggregate pool retained ownership: %+v", stats)
	}
	assertClean := func() {
		t.Helper()
		s := capacity.snapshot()
		if s.SessionResourceBytes != 0 || s.AggregateInitialPoolBytes != 0 || s.AggregateGrowthPoolBytes != 0 ||
			s.AggregateSessionMetadataBytes != 0 || s.ReceiveWindowBytes != 0 || s.InUse != 0 || s.ControlReservedSlots != 0 || s.MemoryUsedBytes != usage.UsedBytes {
			t.Fatalf("terminal owner ledger did not conserve credit: %+v", s)
		}
	}
	assertClean()
	releaseReplacement, ok := capacity.tryReserveAggregateSessionResource(poolBytes, metadataBytes)
	if !ok {
		t.Fatal("same sampled headroom did not recover after retirement")
	}
	t.Cleanup(releaseReplacement)
	assertOwned()
	releaseReplacement()
	releaseReplacement()
	assertClean()
}
