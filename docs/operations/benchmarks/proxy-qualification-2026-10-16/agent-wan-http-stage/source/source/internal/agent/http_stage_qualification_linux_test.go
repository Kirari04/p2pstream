//go:build linux && p2pstream_transport_ceiling && p2pstream_http_stage

package agent

// Test-build-only native HTTP/1 stage. The fixture deliberately invokes the
// production tunnel-session accept/OpenRequest/destination/relay stack; only
// physical setup, origin, bounded ledgers and reporting are fixture code.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

var (
	httpStageAgentServer     = flag.String("http-stage-server", "", "test-only HTTP stage tunnel server")
	httpStageAgentAdmin      = flag.String("http-stage-admin", "", "test-only HTTP stage admin")
	httpStageAgentOrigin     = flag.String("http-stage-origin", "", "test-only native HTTP/1 origin")
	httpStageAgentCA         = flag.String("http-stage-ca", "", "test-only tunnel CA")
	httpStageAgentServerName = flag.String("http-stage-server-name", "", "test-only tunnel TLS name")
	httpStageAgentAuth       = flag.String("http-stage-auth-key-file", "", "test-only tunnel auth key")
	httpStageAgentOutput     = flag.String("http-stage-output", "", "test-only terminal report")
)

const httpStageAgentTimeout = 90 * time.Second

type httpStageAgentLane struct {
	lane            int
	tlsConn         *tls.Conn
	tcpConn         *net.TCPConn
	session         *yamux.Session
	growth          *tunnel.AggregateReceiveWindowGrowth
	releasePool     func()
	releaseCapacity func()
	releaseMetadata func()
}

type httpStageAgentFixture struct {
	ctx      context.Context
	cancel   context.CancelFunc
	endpoint *tunnel.HTTPStageEndpoint
	pool     *tunnel.TransportCeilingLedger
	working  *tunnel.TransportCeilingLedger
	capacity *agentTunnelCapacityRuntime
	policy   *agentDestinationPolicy

	mu              sync.Mutex
	lanes           [tunnel.TransportCeilingPhysicalLanes]*httpStageAgentLane
	workingReleases []func()
	physicalWG      sync.WaitGroup
	serveWG         sync.WaitGroup
	originWG        sync.WaitGroup
	originActive    atomic.Int64
	originCompleted atomic.Int64
	originSeenMu    sync.Mutex
	originSeen      map[string]struct{}
	admin           *http.Server
	origin          *http.Server
}

func TestHTTPStageExternalAgentRole(t *testing.T) {
	if os.Getenv("P2PSTREAM_HTTP_STAGE_EXTERNAL") != "1" {
		t.Skip("external HTTP stage role")
	}
	fixture, err := newHTTPStageAgentFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run(); err != nil {
		t.Fatal(err)
	}
}

func newHTTPStageAgentFixture() (*httpStageAgentFixture, error) {
	if stringsTrimmedEmpty(*httpStageAgentServer, *httpStageAgentAdmin, *httpStageAgentOrigin, *httpStageAgentOutput) {
		return nil, errors.New("HTTP stage agent tunnel/admin/origin/output are required")
	}
	for _, address := range []string{*httpStageAgentServer, *httpStageAgentAdmin, *httpStageAgentOrigin} {
		_, port, err := net.SplitHostPort(address)
		if err != nil || port == "0" {
			return nil, fmt.Errorf("HTTP stage agent address %q must have a concrete nonzero port", address)
		}
	}
	if *httpStageAgentServerName != tunnel.TransportCeilingTLSName {
		return nil, fmt.Errorf("HTTP stage TLS server name must be %q", tunnel.TransportCeilingTLSName)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := tunnel.NewTransportCeilingLedger(tunnel.TransportCeilingPoolLimitBytes)
	working := tunnel.NewTransportCeilingLedger(tunnel.TransportCeilingWorkingLimitBytes)
	endpoint, err := tunnel.NewHTTPStageEndpoint("agent", os.Getenv("P2PSTREAM_HTTP_STAGE_SOURCE_REF"), pool, working)
	if err != nil {
		cancel()
		return nil, err
	}
	policy, err := newAgentDestinationPolicy([]string{*httpStageAgentOrigin})
	if err != nil {
		cancel()
		return nil, err
	}
	fixture := &httpStageAgentFixture{
		ctx: ctx, cancel: cancel, endpoint: endpoint, pool: pool, working: working,
		capacity: newAgentTunnelCapacityRuntime(tunnel.HTTPStageFixedCapacity, false, nil), policy: policy,
		originSeen: make(map[string]struct{}),
	}
	endpoint.SetCallbacks(nil, nil, fixture.ownership)
	return fixture, nil
}

func stringsTrimmedEmpty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func (f *httpStageAgentFixture) reserveWorking(category string, bytes int64) (func(), error) {
	release, ok := f.working.Reserve(category, bytes)
	if !ok {
		return nil, fmt.Errorf("reserve HTTP stage agent %s bytes=%d", category, bytes)
	}
	f.mu.Lock()
	f.workingReleases = append(f.workingReleases, release)
	f.mu.Unlock()
	return release, nil
}

func (f *httpStageAgentFixture) run() error {
	envelopeReleases, err := tunnel.ReserveHTTPStageWorkingEnvelope(f.working)
	if err != nil {
		return err
	}
	f.workingReleases = append(f.workingReleases, envelopeReleases...)
	if err := f.startOrigin(); err != nil {
		return err
	}
	if err := f.connectPhysical(); err != nil {
		return err
	}
	rows, err := f.liveSocketRows()
	if err != nil {
		return err
	}
	f.endpoint.SetReady(rows)
	adminListener, err := net.Listen("tcp", *httpStageAgentAdmin)
	if err != nil {
		return err
	}
	f.admin = &http.Server{Handler: f.endpoint, ReadHeaderTimeout: 2 * time.Second}
	adminDone := make(chan error, 1)
	go func() { adminDone <- f.admin.Serve(adminListener) }()

	physicalDone := make(chan struct{})
	go func() { f.physicalWG.Wait(); close(physicalDone) }()
	select {
	case <-physicalDone:
	case <-f.ctx.Done():
		f.closeSockets()
		select {
		case <-physicalDone:
		case <-time.After(35 * time.Second):
			return errors.New("HTTP stage agent physical sessions did not join")
		}
	}
	f.cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	adminShutdownErr := f.admin.Shutdown(shutdownCtx)
	originShutdownErr := f.origin.Shutdown(shutdownCtx)
	shutdownCancel()
	if adminShutdownErr != nil && !errors.Is(adminShutdownErr, http.ErrServerClosed) {
		return fmt.Errorf("shutdown HTTP stage agent admin: %w", adminShutdownErr)
	}
	if originShutdownErr != nil && !errors.Is(originShutdownErr, http.ErrServerClosed) {
		return fmt.Errorf("shutdown HTTP stage agent origin: %w", originShutdownErr)
	}
	if err := waitHTTPStageAgentGroup(&f.serveWG, 35*time.Second, "production tunnel-session handlers"); err != nil {
		return err
	}
	if err := waitHTTPStageAgentGroup(&f.originWG, 10*time.Second, "native HTTP origin"); err != nil {
		return err
	}
	f.closeSockets()
	f.releaseResources()
	report := f.endpoint.Terminal(f.socketRows(false))
	if err := tunnel.WriteHTTPStageReport(*httpStageAgentOutput, report); err != nil {
		return err
	}
	select {
	case serveErr := <-adminDone:
		if serveErr != nil && serveErr != http.ErrServerClosed {
			return serveErr
		}
	case <-time.After(5 * time.Second):
		return errors.New("HTTP stage agent admin did not stop")
	}
	if report.InvalidReason != "" || report.PoolLedger.FinalBytes != 0 || report.WorkingLedger.FinalBytes != 0 {
		return fmt.Errorf("invalid HTTP stage agent terminal report: %s", report.InvalidReason)
	}
	return nil
}

func waitHTTPStageAgentGroup(group *sync.WaitGroup, timeout time.Duration, label string) error {
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("HTTP stage agent %s did not join", label)
	}
}

func (f *httpStageAgentFixture) startOrigin() error {
	listener, err := net.Listen("tcp", *httpStageAgentOrigin)
	if err != nil {
		return err
	}
	f.origin = &http.Server{Handler: http.HandlerFunc(f.handleOrigin), ReadHeaderTimeout: 5 * time.Second}
	f.originWG.Add(1)
	go func() {
		defer f.originWG.Done()
		if err := f.origin.Serve(listener); err != nil && err != http.ErrServerClosed {
			f.endpoint.MarkInvalid(err)
			f.cancel()
		}
	}()
	return nil
}

func (f *httpStageAgentFixture) handleOrigin(w http.ResponseWriter, r *http.Request) {
	f.originActive.Add(1)
	defer f.originActive.Add(-1)
	phaseIndex, err := strconv.Atoi(r.Header.Get("X-P2PStream-HTTP-Stage-Phase"))
	phase, phaseErr := tunnel.TransportCeilingPhaseAt(phaseIndex)
	if err != nil || phaseErr != nil || phaseIndex != int(f.originCompleted.Load()) {
		http.Error(w, "invalid HTTP stage phase", http.StatusBadRequest)
		return
	}
	connectionID := r.RemoteAddr
	f.originSeenMu.Lock()
	_, reused := f.originSeen[connectionID]
	f.originSeen[connectionID] = struct{}{}
	f.originSeenMu.Unlock()
	result := tunnel.HTTPStagePhaseResult{
		SchemaVersion: tunnel.HTTPStageSchemaVersion, PhaseIndex: phase.Index, PhaseName: phase.Name,
		Lane: phase.Lane, Direction: phase.Direction, Stage: phase.Stage, PlannedReuseKind: phase.ReuseKind,
		ConfiguredPayloadBytes: tunnel.TransportCeilingPayloadBytes, HTTPStatus: http.StatusOK, OriginHTTPMajor: r.ProtoMajor,
		ResponsePumpBufferBytes: tunnel.HTTPStageResponsePumpBytes, FinalConsumerReadBytes: tunnel.HTTPStageConsumerReadBytes,
		Connection: tunnel.HTTPStageConnectionTrace{ConnectionIdentity: connectionID, Reused: reused},
	}
	w.Header().Set("X-P2PStream-Origin-HTTP-Major", strconv.Itoa(r.ProtoMajor))
	w.Header().Set("X-P2PStream-Origin-Connection", connectionID)
	w.Header().Set("X-P2PStream-HTTP-Stage-Phase", strconv.Itoa(phase.Index))
	if phase.Direction == "download" {
		if r.Method != http.MethodGet || r.URL.Path != "/bulk" || r.URL.Query().Get("bytes") != strconv.FormatInt(tunnel.TransportCeilingPayloadBytes, 10) {
			http.Error(w, "download must use GET", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(tunnel.TransportCeilingPayloadBytes, 10))
		digest, writeErr := tunnel.WriteHTTPStagePayload(w, tunnel.TransportCeilingPayloadBytes)
		if writeErr != nil {
			f.endpoint.MarkInvalid(writeErr)
			return
		}
		result.ReceivedPayloadBytes = tunnel.TransportCeilingPayloadBytes
		result.ExpectedSHA256, result.ReceivedSHA256 = tunnel.HTTPStageDownloadSHA256, digest
		result.ResponseContentLength = tunnel.TransportCeilingPayloadBytes
	} else {
		if r.Method != http.MethodPost || r.URL.Path != "/bulk-upload" || r.ContentLength != tunnel.TransportCeilingPayloadBytes {
			http.Error(w, "upload must use exact Content-Length POST", http.StatusBadRequest)
			return
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, r.Body)
		if copyErr != nil || n != tunnel.TransportCeilingPayloadBytes {
			http.Error(w, "upload body read failed", http.StatusBadRequest)
			return
		}
		digest := hex.EncodeToString(h.Sum(nil))
		if digest != tunnel.HTTPStageUploadSHA256 {
			http.Error(w, "upload digest mismatch", http.StatusBadRequest)
			return
		}
		result.ReceivedPayloadBytes = n
		result.ExpectedSHA256, result.ReceivedSHA256 = tunnel.HTTPStageUploadSHA256, digest
		if _, writeErr := w.Write(h.Sum(nil)); writeErr != nil {
			f.endpoint.MarkInvalid(writeErr)
			return
		}
		result.ResponseContentLength = sha256.Size
	}
	if err := f.endpoint.RecordResult(result); err != nil {
		f.endpoint.MarkInvalid(err)
		return
	}
	completed := f.originCompleted.Add(1)
	if completed == tunnel.HTTPStageExpectedPhases {
		rows, liveErr := f.liveSocketRows()
		if liveErr == nil {
			liveErr = f.endpoint.SetLive(rows)
		}
		if liveErr != nil {
			f.endpoint.MarkInvalid(liveErr)
			f.cancel()
		}
	}
}

func (f *httpStageAgentFixture) connectPhysical() error {
	caPEM, err := os.ReadFile(*httpStageAgentCA)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return errors.New("HTTP stage CA contains no certificate")
	}
	authKey, err := tunnel.ReadTransportCeilingAuthKey(*httpStageAgentAuth)
	if err != nil {
		return err
	}
	tlsConfig := &tls.Config{RootCAs: roots, ServerName: *httpStageAgentServerName, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	for laneID := 0; laneID < tunnel.TransportCeilingPhysicalLanes; laneID++ {
		raw, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(f.ctx, "tcp", *httpStageAgentServer)
		if err != nil {
			return err
		}
		tlsConn := tls.Client(raw, tlsConfig.Clone())
		handshakeCtx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
		err = tlsConn.HandshakeContext(handshakeCtx)
		cancel()
		if err != nil {
			_ = raw.Close()
			return err
		}
		if err := tunnel.AuthenticateTransportCeilingClient(tlsConn, authKey, tunnel.TransportCeilingVariantYamux, laneID, 10*time.Second); err != nil {
			_ = tlsConn.Close()
			return err
		}
		tcpConn, ok := tlsConn.NetConn().(*net.TCPConn)
		if !ok {
			return fmt.Errorf("HTTP stage agent TLS underlying connection is %T", tlsConn.NetConn())
		}
		lane := &httpStageAgentLane{lane: laneID, tlsConn: tlsConn, tcpConn: tcpConn}
		f.lanes[laneID] = lane
		if err := f.initializeLane(lane); err != nil {
			return err
		}
	}
	return nil
}

func (f *httpStageAgentFixture) initializeLane(lane *httpStageAgentLane) error {
	var err error
	var config *yamux.Config
	if lane.lane == 0 {
		config, err = tunnel.NewNegotiatedControlYamuxConfig(nil, tunnel.DefaultAdaptiveReceiveWindowBytes, tunnel.NegotiatedControlStreamLimit)
	} else {
		wire := tunnel.DefaultAggregateFlowControlConfig()
		initialBytes := int64(yamux.AggregateReceiveWindowMemory(wire.InitialSessionWindow))
		lane.releasePool, _ = f.pool.Reserve("initial_pool", initialBytes)
		lane.releaseCapacity, _ = f.capacity.tryReserveAggregateSessionResource(initialBytes, tunnel.AggregateSessionMetadataBytes)
		if lane.releasePool == nil || lane.releaseCapacity == nil {
			return fmt.Errorf("reserve HTTP stage agent lane %d initial pool", lane.lane)
		}
		config, err = tunnel.NewAggregateYamuxConfig(nil, tunnel.DefaultMaxStreamWindowSizeBytes, tunnel.HTTPStageFixedCapacity)
	}
	if err != nil {
		return err
	}
	lane.session, err = yamux.Client(lane.tlsConn, config)
	if err != nil {
		return err
	}
	if lane.lane > 0 {
		lane.growth = tunnel.NewAggregateReceiveWindowGrowth(lane.session, func(bytes int64) (func(), bool) {
			releasePool, okPool := f.pool.Reserve("growth_pool", bytes)
			releaseCapacity, okCapacity := f.capacity.tryReserveAggregateSessionGrowth(bytes)
			if !okPool || !okCapacity {
				if okPool {
					releasePool()
				}
				if okCapacity {
					releaseCapacity()
				}
				return nil, false
			}
			return func() { releaseCapacity(); releasePool() }, true
		})
		serveCtx := context.WithValue(f.ctx, receiveWindowContextKey{}, int64(tunnel.DefaultMaxStreamWindowSizeBytes))
		serveCtx = context.WithValue(serveCtx, upstreamSocketBufferContextKey{}, int64(0))
		f.serveWG.Add(1)
		go func() {
			defer f.serveWG.Done()
			err := serveTunnelSessionWithPolicyCapacityAndResources(serveCtx, lane.session, f.policy, f.capacity, lane.growth, false, 0, nil)
			expectedTerminal := f.endpoint.IsLive() && (errors.Is(err, io.EOF) || errors.Is(err, yamux.ErrSessionShutdown))
			if err != nil && !expectedTerminal {
				f.endpoint.MarkInvalid(err)
				f.cancel()
			}
		}()
	}
	f.physicalWG.Add(1)
	go func() { defer f.physicalWG.Done(); <-lane.session.CloseChan() }()
	return nil
}

func (f *httpStageAgentFixture) ownership() tunnel.HTTPStageOwnershipSnapshot {
	snapshot := f.capacity.snapshot()
	result := tunnel.HTTPStageOwnershipSnapshot{
		CapacityTotalInUse: snapshot.InUse, CapacityPooledInUse: snapshot.InUse,
		CapacityLive: snapshot.InUse, OriginActiveRequests: int(f.originActive.Load()),
	}
	for laneID := 1; laneID < tunnel.TransportCeilingPhysicalLanes; laneID++ {
		lane := f.lanes[laneID]
		if lane == nil || lane.session == nil {
			continue
		}
		result.LaneStreams[laneID-1] = lane.session.NumStreams()
		result.LaneReceiveLimits[laneID-1] = lane.session.AggregateReceiveWindowStats().ReceiveLimit
	}
	return result
}

func (f *httpStageAgentFixture) closeSockets() {
	for _, lane := range f.lanes {
		if lane == nil {
			continue
		}
		if lane.session != nil {
			_ = lane.session.Close()
		}
		if lane.tlsConn != nil {
			_ = lane.tlsConn.Close()
		}
	}
}

func (f *httpStageAgentFixture) releaseResources() {
	for _, lane := range f.lanes {
		if lane == nil {
			continue
		}
		if lane.growth != nil {
			lane.growth.Release()
		}
		if lane.releaseCapacity != nil {
			lane.releaseCapacity()
		}
		if lane.releasePool != nil {
			lane.releasePool()
		}
	}
	f.mu.Lock()
	releases := append([]func(){}, f.workingReleases...)
	f.mu.Unlock()
	for index := len(releases) - 1; index >= 0; index-- {
		releases[index]()
	}
}

func (f *httpStageAgentFixture) socketRows(connected bool) []tunnel.TransportCeilingSocket {
	rows := make([]tunnel.TransportCeilingSocket, 0, tunnel.TransportCeilingPhysicalLanes)
	for laneID, lane := range f.lanes {
		if lane == nil || lane.tlsConn == nil {
			continue
		}
		state := lane.tlsConn.ConnectionState()
		tcpInfo := tunnel.CaptureTransportCeilingTCPInfo(lane.tcpConn)
		identity := sha256.Sum256([]byte(fmt.Sprintf("http-agent|%d|%s|%s|%d|%d", laneID, lane.tlsConn.LocalAddr(), lane.tlsConn.RemoteAddr(), state.Version, state.CipherSuite)))
		role := "data"
		if laneID == 0 {
			role = "control"
		}
		rows = append(rows, tunnel.TransportCeilingSocket{
			Lane: laneID, LaneRole: role, LocalAddress: lane.tlsConn.LocalAddr().String(), RemoteAddress: lane.tlsConn.RemoteAddr().String(),
			ConnectionIdentity: fmt.Sprintf("%x", identity[:16]), TLSVersion: state.Version, TLSCipherSuite: state.CipherSuite,
			TCPInfoCaptured: tcpInfo.Captured, TCPInfoError: tcpInfo.Error, TCPState: tcpInfo.State,
			Connected: connected && tcpInfo.Captured && tcpInfo.Error == "" && tcpInfo.State == tunnel.TransportCeilingTCPEstablished,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Lane < rows[j].Lane })
	return rows
}

func (f *httpStageAgentFixture) liveSocketRows() ([]tunnel.TransportCeilingSocket, error) {
	rows := f.socketRows(true)
	if len(rows) != tunnel.TransportCeilingPhysicalLanes {
		return nil, fmt.Errorf("HTTP stage agent live socket count=%d", len(rows))
	}
	for _, row := range rows {
		if !row.Connected {
			return nil, fmt.Errorf("HTTP stage agent lane %d is not established", row.Lane)
		}
	}
	return rows, nil
}
