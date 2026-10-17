//go:build linux && p2pstream_transport_ceiling && p2pstream_http_stage

package server

// Test-build-only HTTP/1 stage. Physical setup/reporting is fixture code. Each
// request uses the production agent transport pool, dialViaAgentWithCapacity,
// capacity-managed connection, OpenRequest and peer relay implementation.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/config"
	"p2pstream/internal/tunnel"
)

var (
	httpStageServerListen = flag.String("http-stage-listen", "", "test-only HTTP stage physical listen address")
	httpStageServerAdmin  = flag.String("http-stage-admin", "", "test-only HTTP stage admin address")
	httpStageServerOrigin = flag.String("http-stage-origin", "", "test-only agent-local HTTP origin address")
	httpStageServerCert   = flag.String("http-stage-cert", "", "test-only TLS certificate")
	httpStageServerKey    = flag.String("http-stage-key", "", "test-only TLS key")
	httpStageServerAuth   = flag.String("http-stage-auth-key-file", "", "test-only tunnel auth key")
	httpStageServerOutput = flag.String("http-stage-output", "", "test-only terminal report")
)

const httpStageServerTimeout = tunnel.HTTPStageResponseTimeout

type httpStageServerLane struct {
	lane            int
	tlsConn         *tls.Conn
	tcpConn         *net.TCPConn
	session         *yamux.Session
	growth          *tunnel.AggregateReceiveWindowGrowth
	releasePool     func()
	releaseMetadata func()
}

type httpStageServerFixture struct {
	ctx      context.Context
	cancel   context.CancelFunc
	endpoint *tunnel.HTTPStageEndpoint
	pool     *tunnel.TransportCeilingLedger
	working  *tunnel.TransportCeilingLedger
	app      *App
	agent    *AgentConn

	mu              sync.Mutex
	lanes           [tunnel.TransportCeilingPhysicalLanes]*httpStageServerLane
	workingReleases []func()
	nextPhase       int
	logicalOpens    atomic.Uint64
	logicalCloses   atomic.Uint64
	connectionReuse atomic.Uint64
	listener        net.Listener
	admin           *http.Server
	shutdownOnce    sync.Once
}

func TestHTTPStageExternalServerRole(t *testing.T) {
	if os.Getenv("P2PSTREAM_HTTP_STAGE_EXTERNAL") != "1" {
		t.Skip("external HTTP stage role")
	}
	fixture, err := newHTTPStageServerFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run(); err != nil {
		t.Fatal(err)
	}
}

func newHTTPStageServerFixture() (*httpStageServerFixture, error) {
	for _, value := range []string{*httpStageServerListen, *httpStageServerAdmin, *httpStageServerOrigin, *httpStageServerOutput} {
		if value == "" {
			return nil, errors.New("HTTP stage server listen/admin/origin/output are required")
		}
	}
	for _, address := range []string{*httpStageServerListen, *httpStageServerAdmin, *httpStageServerOrigin} {
		_, port, err := net.SplitHostPort(address)
		if err != nil || port == "0" {
			return nil, fmt.Errorf("HTTP stage server address %q must have a concrete nonzero port", address)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := tunnel.NewTransportCeilingLedger(tunnel.TransportCeilingPoolLimitBytes)
	working := tunnel.NewTransportCeilingLedger(tunnel.TransportCeilingWorkingLimitBytes)
	endpoint, err := tunnel.NewHTTPStageEndpoint("server", os.Getenv("P2PSTREAM_HTTP_STAGE_SOURCE_REF"), pool, working)
	if err != nil {
		cancel()
		return nil, err
	}
	cfg := &config.Config{ServerTunnelMaxConcurrentStreams: tunnel.HTTPStageFixedCapacity, TunnelMaxStreamWindowBytes: tunnel.DefaultMaxStreamWindowSizeBytes}
	app := NewApp(cfg, nil)
	app.AgentTransports = newAgentTransportPool()
	app.agentStreamCapacity = mustNewDefaultAgentStreamCapacityManager(tunnel.HTTPStageFixedCapacity)
	if pooled := app.agentStreamCapacity.snapshot().Pooled.Capacity; pooled != tunnel.HTTPStagePooledCapacity {
		cancel()
		return nil, fmt.Errorf("HTTP stage pooled capacity=%d, want %d", pooled, tunnel.HTTPStagePooledCapacity)
	}
	agent := &AgentConn{
		AgentID: 1, PublicID: "http-stage-agent", Name: "http-stage-agent", Done: make(chan struct{}),
		AggregateFlowControl: true, AdvertisedMaxConcurrentStreams: tunnel.HTTPStageFixedCapacity, NegotiatedMaxConcurrentStreams: tunnel.HTTPStageFixedCapacity,
	}
	fixture := &httpStageServerFixture{
		ctx: ctx, cancel: cancel, endpoint: endpoint, pool: pool, working: working, app: app, agent: agent,
	}
	endpoint.SetCallbacks(fixture.transfer, fixture.shutdown, fixture.ownership)
	return fixture, nil
}

func (f *httpStageServerFixture) reserveWorking(category string, bytes int64) (func(), error) {
	release, ok := f.working.Reserve(category, bytes)
	if !ok {
		return nil, fmt.Errorf("reserve HTTP stage server %s bytes=%d", category, bytes)
	}
	f.workingReleases = append(f.workingReleases, release)
	return release, nil
}

func (f *httpStageServerFixture) run() error {
	envelopeReleases, err := tunnel.ReserveHTTPStageWorkingEnvelope(f.working)
	if err != nil {
		return err
	}
	f.workingReleases = append(f.workingReleases, envelopeReleases...)
	if err := f.acceptPhysical(); err != nil {
		return err
	}
	rows, err := f.liveSocketRows()
	if err != nil {
		return err
	}
	f.endpoint.SetReady(rows)
	adminListener, err := net.Listen("tcp", *httpStageServerAdmin)
	if err != nil {
		return err
	}
	f.admin = &http.Server{Handler: f.endpoint, ReadHeaderTimeout: 2 * time.Second}
	adminDone := make(chan error, 1)
	go func() { adminDone <- f.admin.Serve(adminListener) }()
	<-f.ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	adminShutdownErr := f.admin.Shutdown(shutdownCtx)
	shutdownCancel()
	if adminShutdownErr != nil && !errors.Is(adminShutdownErr, http.ErrServerClosed) {
		return fmt.Errorf("shutdown HTTP stage server admin: %w", adminShutdownErr)
	}
	if err := f.closeTransportsAndWait(context.Background()); err != nil {
		return err
	}
	if err := f.closePhysical(); err != nil {
		return err
	}
	for index := len(f.workingReleases) - 1; index >= 0; index-- {
		f.workingReleases[index]()
	}
	report := f.endpoint.Terminal(f.socketRows(false))
	if err := tunnel.WriteHTTPStageReport(*httpStageServerOutput, report); err != nil {
		return err
	}
	select {
	case serveErr := <-adminDone:
		if serveErr != nil && serveErr != http.ErrServerClosed {
			return serveErr
		}
	case <-time.After(5 * time.Second):
		return errors.New("HTTP stage server admin did not stop")
	}
	if report.InvalidReason != "" || report.PoolLedger.FinalBytes != 0 || report.WorkingLedger.FinalBytes != 0 {
		return fmt.Errorf("invalid HTTP stage server terminal report: %s", report.InvalidReason)
	}
	return nil
}

func (f *httpStageServerFixture) acceptPhysical() error {
	certificate, err := tls.LoadX509KeyPair(*httpStageServerCert, *httpStageServerKey)
	if err != nil {
		return err
	}
	authKey, err := tunnel.ReadTransportCeilingAuthKey(*httpStageServerAuth)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *httpStageServerListen)
	if err != nil {
		return err
	}
	f.listener = listener
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	for accepted := 0; accepted < tunnel.TransportCeilingPhysicalLanes; accepted++ {
		raw, err := listener.Accept()
		if err != nil {
			return err
		}
		_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
		tlsConn := tls.Server(raw, tlsConfig.Clone())
		if err := tlsConn.HandshakeContext(f.ctx); err != nil {
			_ = raw.Close()
			return err
		}
		_ = raw.SetDeadline(time.Time{})
		laneID, err := tunnel.AuthenticateTransportCeilingServer(tlsConn, authKey, tunnel.TransportCeilingVariantYamux, 10*time.Second)
		if err != nil {
			_ = tlsConn.Close()
			return err
		}
		if f.lanes[laneID] != nil {
			_ = tlsConn.Close()
			return fmt.Errorf("duplicate HTTP stage lane %d", laneID)
		}
		tcpConn, ok := tlsConn.NetConn().(*net.TCPConn)
		if !ok {
			return fmt.Errorf("HTTP stage server TLS underlying connection is %T", tlsConn.NetConn())
		}
		lane := &httpStageServerLane{lane: laneID, tlsConn: tlsConn, tcpConn: tcpConn}
		f.lanes[laneID] = lane
		if err := f.initializeLane(lane); err != nil {
			return err
		}
	}
	_ = listener.Close()
	f.agent.Session = f.lanes[0].session
	sessionKey := agentStreamCapacitySessionKey(f.agent, f.agent.Session)
	f.app.agentStreamCapacity.registerSessionWithLimit(sessionKey, tunnel.HTTPStageFixedCapacity)
	return nil
}

func (f *httpStageServerFixture) initializeLane(lane *httpStageServerLane) error {
	var err error
	var yamuxConfig *yamux.Config
	if lane.lane == 0 {
		yamuxConfig, err = tunnel.NewNegotiatedControlYamuxConfig(nil, tunnel.DefaultAdaptiveReceiveWindowBytes, -1)
	} else {
		wire := tunnel.DefaultAggregateFlowControlConfig()
		initialBytes := int64(yamux.AggregateReceiveWindowMemory(wire.InitialSessionWindow))
		lane.releasePool, _ = f.pool.Reserve("initial_pool", initialBytes)
		if lane.releasePool == nil {
			return fmt.Errorf("reserve HTTP stage server lane %d initial pool", lane.lane)
		}
		yamuxConfig, err = tunnel.NewAggregateYamuxConfig(nil, tunnel.DefaultMaxStreamWindowSizeBytes, -1)
	}
	if err != nil {
		return err
	}
	lane.session, err = yamux.Server(lane.tlsConn, yamuxConfig)
	if err != nil {
		return err
	}
	if err := lane.session.GoAway(); err != nil {
		return err
	}
	resource := agentTunnelOwnedResource{SessionMetadataBytes: tunnel.AggregateSessionMetadataBytes}
	if lane.lane > 0 {
		wire := tunnel.DefaultAggregateFlowControlConfig()
		resource.AggregateInitialPoolBytes = int64(yamux.AggregateReceiveWindowMemory(wire.InitialSessionWindow))
		lane.growth = tunnel.NewAggregateReceiveWindowGrowth(lane.session, func(bytes int64) (func(), bool) {
			return f.pool.Reserve("growth_pool", bytes)
		})
		resource.AggregateGrowth = lane.growth
	}
	f.agent.installTunnel(lane.lane, lane.session, resource)
	return nil
}

func (f *httpStageServerFixture) transfer(ctx context.Context, index int) (tunnel.HTTPStagePhaseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index != f.nextPhase {
		return tunnel.HTTPStagePhaseResult{}, fmt.Errorf("HTTP stage phase %d requested while waiting for %d", index, f.nextPhase)
	}
	phase, err := tunnel.TransportCeilingPhaseAt(index)
	if err != nil {
		return tunnel.HTTPStagePhaseResult{}, err
	}
	before := f.ownership()
	result := tunnel.HTTPStagePhaseResult{
		SchemaVersion: tunnel.HTTPStageSchemaVersion, PhaseIndex: phase.Index, PhaseName: phase.Name,
		Lane: phase.Lane, Direction: phase.Direction, Stage: phase.Stage, PlannedReuseKind: phase.ReuseKind,
		ConfiguredPayloadBytes: tunnel.TransportCeilingPayloadBytes, OwnershipBefore: before,
		ResponsePumpBufferBytes: tunnel.HTTPStageResponsePumpBytes, FinalConsumerReadBytes: tunnel.HTTPStageConsumerReadBytes,
	}
	key := agentTransportKey{
		Kind: agentTransportKindRouteTarget, AgentID: f.agent.AgentID, TunnelLane: phase.Lane,
		RouteTargetID: int64(phase.Lane), TargetOrigin: "http://" + *httpStageServerOrigin,
		ResponseHeaderTimeoutMillis: int64(httpStageServerTimeout / time.Millisecond),
	}
	roundTripper := f.app.AgentTransports.getOrCreate(f.app, f.agent, key, nil, httpStageServerTimeout)
	requestURL := "http://" + *httpStageServerOrigin + "/bulk?bytes=" + strconv.FormatInt(tunnel.TransportCeilingPayloadBytes, 10)
	method := http.MethodGet
	var requestBody io.Reader
	if phase.Direction == "upload" {
		method = http.MethodPost
		requestURL = "http://" + *httpStageServerOrigin + "/bulk-upload"
		requestBody = tunnel.NewHTTPStageUploadReader(tunnel.TransportCeilingPayloadBytes, tunnel.HTTPStageUploadSourceBytes)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, requestBody)
	if err != nil {
		return result, err
	}
	request.Header.Set("X-P2PStream-HTTP-Stage-Phase", strconv.Itoa(phase.Index))
	if phase.Direction == "upload" {
		request.ContentLength = tunnel.TransportCeilingPayloadBytes
	}
	var traceMu sync.Mutex
	trace := tunnel.HTTPStageConnectionTrace{}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			traceMu.Lock()
			trace.ConnectionIdentity = fmt.Sprintf("%p", info.Conn)
			trace.Reused, trace.WasIdle, trace.IdleNanoseconds = info.Reused, info.WasIdle, int64(info.IdleTime)
			trace.DialOpened = !info.Reused
			traceMu.Unlock()
			if info.Reused {
				f.connectionReuse.Add(1)
			} else {
				f.logicalOpens.Add(1)
			}
		},
		PutIdleConn: func(err error) {
			traceMu.Lock()
			defer traceMu.Unlock()
			trace.PutIdle = err == nil
			if err != nil {
				trace.PutIdleError = err.Error()
			} else {
				f.logicalCloses.Add(0)
			}
		},
	}))
	wallStart := time.Now()
	response, err := roundTripper.RoundTrip(request)
	if err != nil {
		return result, err
	}
	releasePump, ok := f.working.Reserve("relay_buffers", tunnel.HTTPStageResponsePumpBytes)
	if !ok {
		_ = response.Body.Close()
		return result, errors.New("HTTP stage response-pump reservation denied")
	}
	received, responseDigest, prefix, consumeErr := tunnel.ConsumeHTTPStageResponse(ctx, response.Body, releasePump)
	wallElapsed := time.Since(wallStart)
	if consumeErr != nil {
		return result, consumeErr
	}
	result.PhaseWallNanoseconds = wallElapsed.Nanoseconds()
	result.UsefulBytesPerSecond = float64(tunnel.TransportCeilingPayloadBytes) / wallElapsed.Seconds()
	result.HTTPStatus = response.StatusCode
	result.ResponseContentLength = response.ContentLength
	result.OriginHTTPMajor, _ = strconv.Atoi(response.Header.Get("X-P2PStream-Origin-HTTP-Major"))
	traceMu.Lock()
	trace.OriginConnectionIdentity = response.Header.Get("X-P2PStream-Origin-Connection")
	result.Connection = trace
	traceMu.Unlock()
	if response.StatusCode != http.StatusOK || result.OriginHTTPMajor != 1 || response.Header.Get("X-P2PStream-HTTP-Stage-Phase") != strconv.Itoa(phase.Index) {
		return result, fmt.Errorf("HTTP stage response status/protocol/phase status=%d origin=%d phase=%q", response.StatusCode, result.OriginHTTPMajor, response.Header.Get("X-P2PStream-HTTP-Stage-Phase"))
	}
	if phase.Direction == "download" {
		result.ReceivedPayloadBytes, result.ExpectedSHA256, result.ReceivedSHA256 = received, tunnel.HTTPStageDownloadSHA256, responseDigest
		if received != tunnel.TransportCeilingPayloadBytes || response.ContentLength != tunnel.TransportCeilingPayloadBytes || responseDigest != tunnel.HTTPStageDownloadSHA256 {
			return result, errors.New("HTTP stage download payload mismatch")
		}
	} else {
		result.ReceivedPayloadBytes, result.ExpectedSHA256, result.ReceivedSHA256 = tunnel.TransportCeilingPayloadBytes, tunnel.HTTPStageUploadSHA256, hex.EncodeToString(prefix)
		if received != sha256.Size || response.ContentLength != sha256.Size || len(prefix) != sha256.Size || !bytes.Equal(prefix, mustDecodeHTTPStageDigest(tunnel.HTTPStageUploadSHA256)) {
			return result, errors.New("HTTP stage upload digest response mismatch")
		}
	}
	if phase.Stage == "conditioning" {
		f.app.AgentTransports.closeAll()
		result.ConditioningPoolClosed = true
		if err := f.waitLogicalQuiescent(ctx); err != nil {
			return result, err
		}
		result.ConditioningQuiescent = true
	}
	result.OwnershipAfter = f.ownership()
	f.nextPhase++
	if err := f.endpoint.RecordResult(result); err != nil {
		return result, err
	}
	if index == tunnel.HTTPStageExpectedPhases-1 {
		rows, liveErr := f.liveSocketRows()
		if liveErr != nil {
			return result, liveErr
		}
		if liveErr := f.endpoint.SetLive(rows); liveErr != nil {
			return result, liveErr
		}
	}
	return result, nil
}

func mustDecodeHTTPStageDigest(value string) []byte {
	decoded, _ := hex.DecodeString(value)
	return decoded
}

func (f *httpStageServerFixture) waitLogicalQuiescent(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(35 * time.Second)
	defer deadline.Stop()
	for {
		snapshot := f.app.agentStreamCapacity.snapshot()
		f.app.AgentTransports.mu.Lock()
		entries, draining := len(f.app.AgentTransports.entries), len(f.app.AgentTransports.draining)
		f.app.AgentTransports.mu.Unlock()
		laneStreams := 0
		for laneID := 1; laneID < tunnel.TransportCeilingPhysicalLanes; laneID++ {
			if lane := f.lanes[laneID]; lane != nil && lane.session != nil {
				laneStreams += lane.session.NumStreams()
			}
		}
		if snapshot.Total.InUse == 0 && snapshot.States.Opening == 0 && snapshot.States.Live == 0 && snapshot.States.Closing == 0 && entries == 0 && draining == 0 && laneStreams == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("HTTP stage logical ownership did not quiesce")
		case <-ticker.C:
		}
	}
}

func (f *httpStageServerFixture) closeTransportsAndWait(ctx context.Context) error {
	f.app.AgentTransports.closeAll()
	return f.waitLogicalQuiescent(ctx)
}

func (f *httpStageServerFixture) shutdown() {
	f.shutdownOnce.Do(func() { f.cancel() })
}

func (f *httpStageServerFixture) ownership() tunnel.HTTPStageOwnershipSnapshot {
	snapshot := f.app.agentStreamCapacity.snapshot()
	f.app.AgentTransports.mu.Lock()
	entries, draining := len(f.app.AgentTransports.entries), len(f.app.AgentTransports.draining)
	f.app.AgentTransports.mu.Unlock()
	result := tunnel.HTTPStageOwnershipSnapshot{
		CapacityTotalInUse: snapshot.Total.InUse, CapacityPooledInUse: snapshot.Pooled.InUse,
		CapacityOpening: snapshot.States.Opening, CapacityLive: snapshot.States.Live, CapacityClosing: snapshot.States.Closing,
		CapacityRegisteredSessions: snapshot.RegisteredSessions,
		TransportEntries:           entries, TransportDraining: draining,
		LogicalOpens: snapshot.Granted, LogicalCloses: snapshot.Released, ConnectionReuses: f.connectionReuse.Load(),
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

func (f *httpStageServerFixture) closePhysical() error {
	if f.app != nil && f.app.agentStreamCapacity != nil && f.agent != nil && f.agent.Session != nil {
		f.app.agentStreamCapacity.unregisterSession(agentStreamCapacitySessionKey(f.agent, f.agent.Session))
	}
	if f.agent != nil {
		f.agent.signalDone()
	}
	var first error
	for _, lane := range f.lanes {
		if lane == nil {
			continue
		}
		if lane.session != nil {
			if err := lane.session.Close(); err != nil && first == nil {
				first = err
			}
		}
		if lane.tlsConn != nil {
			_ = lane.tlsConn.Close()
		}
	}
	for _, lane := range f.lanes {
		if lane == nil {
			continue
		}
		if lane.growth != nil {
			lane.growth.Release()
		}
		if lane.releasePool != nil {
			lane.releasePool()
		}
	}
	return first
}

func (f *httpStageServerFixture) socketRows(connected bool) []tunnel.TransportCeilingSocket {
	rows := make([]tunnel.TransportCeilingSocket, 0, tunnel.TransportCeilingPhysicalLanes)
	for laneID, lane := range f.lanes {
		if lane == nil || lane.tlsConn == nil {
			continue
		}
		state := lane.tlsConn.ConnectionState()
		tcpInfo := tunnel.CaptureTransportCeilingTCPInfo(lane.tcpConn)
		identity := sha256.Sum256([]byte(fmt.Sprintf("http-server|%d|%s|%s|%d|%d", laneID, lane.tlsConn.LocalAddr(), lane.tlsConn.RemoteAddr(), state.Version, state.CipherSuite)))
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

func (f *httpStageServerFixture) liveSocketRows() ([]tunnel.TransportCeilingSocket, error) {
	rows := f.socketRows(true)
	if len(rows) != tunnel.TransportCeilingPhysicalLanes {
		return nil, fmt.Errorf("HTTP stage server live socket count=%d", len(rows))
	}
	for _, row := range rows {
		if !row.Connected {
			return nil, fmt.Errorf("HTTP stage server lane %d is not established", row.Lane)
		}
	}
	return rows, nil
}
