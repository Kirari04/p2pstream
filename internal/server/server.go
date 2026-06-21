package server

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/managementui"
	"p2pstream/internal/tunnel"
	"p2pstream/stats"
)

type AgentConn struct {
	AgentID        int64
	PublicID       string
	Name           string
	Session        *yamux.Session
	Done           chan struct{}
	ActiveRequests atomic.Int64
	ConnectedAt    time.Time
	ConnectionDBID int64
}

type App struct {
	Config             *config.Config
	DB                 *db.DB
	StartedAt          time.Time
	LatestAgentStats   atomic.Pointer[stats.AgentStats]
	latestAgentStatsMu sync.RWMutex
	latestAgentStats   map[int64]stats.AgentStats

	// These service fields remain public for package tests during the extraction stack.
	// New construction should go through appServices so they can become private later.
	AgentHub              *agentHub
	LoadBalancers         *loadBalancerRegistry
	TargetHealth          *publicRouteTargetHealthMonitor
	TrafficTracer         *trafficTracer
	RateLimiter           *publicRateLimiter
	TrafficShaper         *publicTrafficShaper
	PublicWAF             *publicWAF
	PublicCache           *publicProxyCache
	PublicACME            *publicACMEManager
	publicConfig          *publicConfigService
	proxyRuntime          *proxyRuntime
	observabilityRecorder *observabilityRecorder
	auth                  *authService
	AgentTransports       *agentTransportPool
	DashboardCache        *dashboardResponseCache
	LoginThrottle         *loginThrottle
	agentAuthLocks        *agentAuthLockMap

	ProxyIsRunning atomic.Bool
	ProxyLastError atomic.Pointer[string]

	setupMu             sync.Mutex
	setupTokenHash      string
	generatedSetupToken string
	setupTokenLogOnce   sync.Once

	proxyMu             sync.Mutex
	proxyServiceActive  bool
	proxyState          p2pstreamv1.ProxyState
	proxyLastError      string
	publicSnapshot      *publicProxySnapshot
	publicListenerState map[int64]*publicListenerRuntime

	publicConfigCacheMu sync.RWMutex
	publicConfigCache   cachedPublicConfig

	observabilityMu          sync.Mutex
	observabilityLastCleanup time.Time

	agentTunnelBeforeFinalAuth func(db.Agent)
}

type agentAuthLockMap struct {
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

func newAgentAuthLockMap() *agentAuthLockMap {
	return &agentAuthLockMap{locks: make(map[int64]*sync.Mutex)}
}

func (m *agentAuthLockMap) lock(agentID int64) func() {
	if m == nil {
		return func() {}
	}
	m.mu.Lock()
	lock := m.locks[agentID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[agentID] = lock
	}
	m.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}

func (a *App) lockAgentAuth(agentID int64) func() {
	if a == nil || a.agentAuthLocks == nil {
		return func() {}
	}
	return a.agentAuthLocks.lock(agentID)
}

func NewApp(cfg *config.Config, database *db.DB) *App {
	if cfg == nil {
		cfg = &config.Config{}
	}
	app := &App{
		Config:              cfg,
		DB:                  database,
		StartedAt:           time.Now(),
		latestAgentStats:    make(map[int64]stats.AgentStats),
		proxyState:          p2pstreamv1.ProxyState_PROXY_STATE_STOPPED,
		publicListenerState: make(map[int64]*publicListenerRuntime),
	}
	app.applyServices(newAppServices(cfg, app))
	if database != nil {
		app.closeStaleAgentConnections(context.Background(), time.Now().UTC())
		app.initializeSetupToken(context.Background())
		app.ensureBootstrapAgent(context.Background())
	}
	return app
}

func (a *App) closeStaleAgentConnections(ctx context.Context, now time.Time) {
	if a == nil || a.DB == nil {
		return
	}
	disconnectedAt := sql.NullTime{Time: now.UTC(), Valid: true}
	if err := a.DB.MarkAgentsWithOpenConnectionsDisconnectedAt(ctx, db.MarkAgentsWithOpenConnectionsDisconnectedAtParams{
		LastDisconnectedAt: disconnectedAt,
		UpdatedAt:          disconnectedAt.Time,
	}); err != nil {
		log.Warn().Err(err).Msg("Failed to mark stale agent connections disconnected")
	}
	if err := a.DB.CloseOpenConnectionsAt(ctx, disconnectedAt); err != nil {
		log.Warn().Err(err).Msg("Failed to close stale agent connection rows")
	}
}

// RegisterManagementRoutes attaches the agent tunnel and ConnectRPC APIs (Port 8081).
func (a *App) RegisterManagementRoutes(mux *http.ServeMux) {
	mux.HandleFunc(sourceOfferPath, sourceOfferHandler)
	mux.HandleFunc(tunnel.BootstrapPath, a.agentTunnelHandler)
	mux.Handle(environmentProxyPrefix, a.environmentProxyHandler())
	path, handler := p2pstreamv1connect.NewAgentManagementServiceHandler(a,
		connect.WithCodec(strictProtoJSONCodec{name: "json"}),
		connect.WithCodec(strictProtoJSONCodec{name: "json; charset=utf-8"}),
	)
	mux.Handle(path, handler)
	if !a.Config.ManagementUIDisabled {
		mux.Handle("/", managementui.NewHandler(a.Config.ManagementUIDevProxy, a.Config.ManagementUIDistDir))
	}
}

// ReportStats implements the ConnectRPC AgentManagementService.
func (a *App) ReportStats(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.AgentStatsRequest],
) (*connect.Response[p2pstreamv1.AgentStatsResponse], error) {
	agentRow, err := a.authenticateAgent(ctx, req.Msg.AgentPublicId, req.Header().Get("Authorization"))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	payload := req.Msg

	s := stats.AgentStats{
		Timestamp:        time.Now(),
		NumGoroutine:     int(payload.NumGoroutine),
		AllocAllocated:   uint64(payload.MemorySysMb),
		ActiveRequests:   payload.ActiveRequests,
		CPUPercent:       payload.CpuPercent,
		ReqSuccess:       int32(payload.ReqSuccess),
		ReqClientError:   int32(payload.ReqClientError),
		ReqServerError:   int32(payload.ReqServerError),
		ReqInternalError: int32(payload.ReqInternalError),
		BytesReceived:    payload.BytesReceived,
		BytesSent:        payload.BytesSent,
	}

	a.LatestAgentStats.Store(&s)
	a.storeLatestAgentStats(agentRow.ID, s)

	log.Debug().
		Str("agent", agentRow.PublicID).
		Int64("mem_mb", payload.MemorySysMb).
		Int64("goroutines", payload.NumGoroutine).
		Int64("req_success", payload.ReqSuccess).
		Int64("req_err", payload.ReqServerError).
		Msg("Agent Health")

	if a.DB != nil {
		reportedAt := time.Now().UTC()
		err := a.insertAgentStatWithRollup(ctx, db.InsertAgentStatAtParams{
			ReportedAt:       reportedAt,
			AgentID:          sql.NullInt64{Int64: agentRow.ID, Valid: true},
			MemoryMb:         payload.MemorySysMb,
			Goroutines:       payload.NumGoroutine,
			ReqSuccess:       payload.ReqSuccess,
			ReqClientError:   payload.ReqClientError,
			ReqServerError:   payload.ReqServerError,
			ReqInternalError: payload.ReqInternalError,
			BytesRx:          int64(payload.BytesReceived),
			BytesTx:          int64(payload.BytesSent),
			CpuPercent:       payload.CpuPercent,
		})
		if err != nil {
			log.Error().Err(err).Msg("Failed to insert agent stats into DB")
		}
	}

	return connect.NewResponse(&p2pstreamv1.AgentStatsResponse{}), nil
}

func (a *App) storeLatestAgentStats(agentID int64, stat stats.AgentStats) {
	a.latestAgentStatsMu.Lock()
	if a.latestAgentStats == nil {
		a.latestAgentStats = make(map[int64]stats.AgentStats)
	}
	a.latestAgentStats[agentID] = stat
	a.latestAgentStatsMu.Unlock()
}

func (a *App) latestAgentStatsSnapshot(agentID int64) (*p2pstreamv1.AgentStatsSnapshot, bool) {
	a.latestAgentStatsMu.RLock()
	stat, ok := a.latestAgentStats[agentID]
	a.latestAgentStatsMu.RUnlock()
	if !ok {
		return nil, false
	}
	return agentStatsSnapshotFromRuntime(stat), true
}

func agentStatsSnapshotFromRuntime(stat stats.AgentStats) *p2pstreamv1.AgentStatsSnapshot {
	return &p2pstreamv1.AgentStatsSnapshot{
		MemorySysMb:          int64(stat.AllocAllocated),
		NumGoroutine:         int64(stat.NumGoroutine),
		ReqSuccess:           int64(stat.ReqSuccess),
		ReqClientError:       int64(stat.ReqClientError),
		ReqServerError:       int64(stat.ReqServerError),
		ReqInternalError:     int64(stat.ReqInternalError),
		BytesReceived:        stat.BytesReceived,
		BytesSent:            stat.BytesSent,
		ActiveRequests:       stat.ActiveRequests,
		CpuPercent:           stat.CPUPercent,
		ReportedAtUnixMillis: stat.Timestamp.UnixMilli(),
	}
}

// GetStatus implements the ConnectRPC AgentManagementService status endpoint.
func (a *App) GetStatus(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetStatusRequest],
) (*connect.Response[p2pstreamv1.GetStatusResponse], error) {
	if _, err := a.requireUser(ctx, req.Header()); err != nil {
		return nil, err
	}

	return connect.NewResponse(a.statusResponse()), nil
}

func (a *App) statusResponse() *p2pstreamv1.GetStatusResponse {
	var proxyLastError string
	if errPtr := a.ProxyLastError.Load(); errPtr != nil {
		proxyLastError = *errPtr
	}

	resp := &p2pstreamv1.GetStatusResponse{
		ProxyRunning:   a.ProxyIsRunning.Load(),
		ProxyLastError: proxyLastError,
		AgentConnected: a.AgentHub.connectedCount() > 0,
		Proxy:          a.proxyStatus(),
	}

	if latest := a.LatestAgentStats.Load(); latest != nil {
		resp.LatestAgentStats = agentStatsSnapshotFromRuntime(*latest)
	}

	return resp
}

func (a *App) agentTunnelHandler(w http.ResponseWriter, r *http.Request) {
	publicID := r.Header.Get("X-P2PStream-Agent-ID")
	authorization := r.Header.Get("Authorization")
	agentRow, err := a.authenticateAgent(r.Context(), publicID, authorization)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), tunnel.UpgradeToken) {
		http.Error(w, "agent tunnel upgrade required", http.StatusBadRequest)
		return
	}
	version := strconv.Itoa(tunnel.ProtocolVersion)
	if r.Header.Get(tunnel.TunnelVersionHeader) != version {
		http.Error(w, "unsupported tunnel version", http.StatusUpgradeRequired)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "agent tunnel requires HTTP/1.1 hijack support", http.StatusInternalServerError)
		return
	}

	if a.agentTunnelBeforeFinalAuth != nil {
		a.agentTunnelBeforeFinalAuth(agentRow)
	}

	unlock := a.lockAgentAuth(agentRow.ID)
	locked := true
	unlockAgentAuth := func() {
		if locked {
			locked = false
			unlock()
		}
	}
	defer unlockAgentAuth()

	finalAgentRow, err := a.authenticateAgent(r.Context(), publicID, authorization)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if finalAgentRow.ID != agentRow.ID {
		http.Error(w, "agent identity changed", http.StatusUnauthorized)
		return
	}
	agentRow = finalAgentRow

	if existing := a.AgentHub.connectedByID(agentRow.ID); existing != nil {
		log.Warn().Str("agent", agentRow.PublicID).Msg("Rejecting duplicate agent connection")
		http.Error(w, "agent is already connected", http.StatusConflict)
		return
	}

	agent := &AgentConn{
		AgentID:     agentRow.ID,
		PublicID:    agentRow.PublicID,
		Name:        agentRow.Name,
		Done:        make(chan struct{}),
		ConnectedAt: time.Now(),
	}

	rawConn, rw, err := hijacker.Hijack()
	if err != nil {
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Failed to hijack agent tunnel")
		return
	}
	if rw.Reader.Buffered() > 0 {
		_, _ = rw.WriteString("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\nunexpected buffered tunnel data\n")
		_ = rw.Flush()
		_ = rawConn.Close()
		return
	}
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.WriteString("Upgrade: " + tunnel.UpgradeToken + "\r\n")
	_, _ = rw.WriteString(tunnel.TunnelVersionHeader + ": " + version + "\r\n")
	_, _ = rw.WriteString("\r\n")
	if err := rw.Flush(); err != nil {
		_ = rawConn.Close()
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Failed to write agent tunnel upgrade response")
		return
	}

	session, err := yamux.Server(rawConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		_ = rawConn.Close()
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Failed to initialize agent tunnel session")
		return
	}
	agent.Session = session

	if a.DB != nil {
		id, err := a.DB.InsertConnection(r.Context(), sql.NullInt64{Int64: agentRow.ID, Valid: true})
		if err == nil {
			agent.ConnectionDBID = id
			if err := a.DB.MarkAgentConnected(r.Context(), agentRow.ID); err != nil {
				log.Warn().Err(err).Str("agent", agentRow.PublicID).Msg("Failed to update agent connected timestamp")
			}
		} else {
			log.Warn().Err(err).Msg("Failed to insert connection into DB")
		}
	}
	if err := a.AgentHub.connect(agent); err != nil {
		_ = session.Close()
		if a.DB != nil && agent.ConnectionDBID > 0 {
			if err := a.DB.UpdateConnectionDisconnected(context.Background(), agent.ConnectionDBID); err != nil {
				log.Warn().Err(err).Msg("Failed to update rejected connection disconnection time")
			}
			if err := a.DB.MarkAgentDisconnected(context.Background(), agent.AgentID); err != nil {
				log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Failed to update rejected agent disconnected timestamp")
			}
		}
		log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Rejecting duplicate agent connection")
		return
	}
	unlockAgentAuth()

	cleanupAgent := func() {
		a.AgentHub.disconnect(agent)
		if a.TargetHealth != nil {
			a.TargetHealth.recordAgentDisconnectedForAll(agent.AgentID)
		}
	}
	if a.TargetHealth != nil {
		a.TargetHealth.recordAgentConnectedForAll(agent.AgentID, agent.PublicID)
	}

	log.Info().
		Str("remote_addr", r.RemoteAddr).
		Str("agent", agent.PublicID).
		Int("tunnel_version", tunnel.ProtocolVersion).
		Msg("Agent tunnel connected successfully")

	go func() {
		select {
		case <-agent.Done:
			_ = session.Close()
		case <-session.CloseChan():
		}
		cleanupAgent()
		log.Info().
			Str("agent", agent.PublicID).
			Int64("duration_ms", time.Since(agent.ConnectedAt).Milliseconds()).
			Int64("active_requests", agent.ActiveRequests.Load()).
			Msg("Agent tunnel disconnected")
		if a.DB != nil && agent.ConnectionDBID > 0 {
			if err := a.DB.UpdateConnectionDisconnected(context.Background(), agent.ConnectionDBID); err != nil {
				log.Warn().Err(err).Msg("Failed to update disconnection time")
			}
			if err := a.DB.MarkAgentDisconnected(context.Background(), agent.AgentID); err != nil {
				log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Failed to update agent disconnected timestamp")
			}
		}
	}()
}

func headerHasToken(header http.Header, name string, want string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
