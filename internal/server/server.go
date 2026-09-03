package server

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/managementui"
	"p2pstream/internal/tunnel"
	"p2pstream/stats"
)

type AgentConn struct {
	AgentID                        int64
	PublicID                       string
	Name                           string
	Session                        *yamux.Session
	Done                           chan struct{}
	doneOnce                       sync.Once
	streamOpenMu                   sync.Mutex
	streamOpenGate                 chan struct{}
	streamOpenAdmissionLimit       int
	AdvertisedMaxConcurrentStreams int64
	NegotiatedMaxConcurrentStreams int64
	AdaptiveCapacity               bool
	CurrentAdmissionLimit          atomic.Int64
	ActiveRequests                 atomic.Int64
	ConnectedAt                    time.Time
	ConnectionDBID                 int64
	BuildVersion                   string
	BuildCommit                    string
}

type agentTunnelReadGateConn struct {
	net.Conn
	ready       chan struct{}
	readOnce    sync.Once
	releaseOnce sync.Once
}

func (c *agentTunnelReadGateConn) Read(p []byte) (int, error) {
	c.readOnce.Do(func() { <-c.ready })
	return c.Conn.Read(p)
}

func (c *agentTunnelReadGateConn) Close() error {
	c.releaseReads()
	return c.Conn.Close()
}

func (c *agentTunnelReadGateConn) releaseReads() {
	c.releaseOnce.Do(func() { close(c.ready) })
}

func (c *AgentConn) signalDone() {
	if c == nil || c.Done == nil {
		return
	}
	c.doneOnce.Do(func() {
		select {
		case <-c.Done:
			// Keep compatibility with test and embedded callers that supplied an
			// already-closed channel without going through signalDone.
		default:
			close(c.Done)
		}
	})
}

func (c *AgentConn) streamOpenAdmissionGate() chan struct{} {
	if c == nil {
		return nil
	}
	c.streamOpenMu.Lock()
	defer c.streamOpenMu.Unlock()
	if c.streamOpenGate != nil {
		return c.streamOpenGate
	}
	maxAdmissions := tunnel.DefaultYamuxConfig(nil).AcceptBacklog
	if maxAdmissions < 1 {
		maxAdmissions = 1
	}
	limit := c.streamOpenAdmissionLimit
	if limit < 1 || limit > maxAdmissions {
		limit = maxAdmissions
	}
	c.streamOpenGate = make(chan struct{}, limit)
	return c.streamOpenGate
}

func (c *AgentConn) acquireStreamOpenAdmission(ctx context.Context) (func(), bool) {
	if c == nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	gate := c.streamOpenAdmissionGate()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, false
	case <-c.Done:
		return nil, false
	}

	// A ready gate and cancellation can race in the select above. Check again
	// before the caller is allowed to create an Open goroutine.
	select {
	case <-ctx.Done():
		<-gate
		return nil, false
	default:
	}
	select {
	case <-c.Done:
		<-gate
		return nil, false
	default:
	}

	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() { <-gate })
	}, true
}

type App struct {
	Config              *config.Config
	DB                  *db.DB
	StartedAt           time.Time
	LatestAgentStats    atomic.Pointer[stats.AgentStats]
	latestAgentStatsMu  sync.RWMutex
	latestAgentStats    map[int64]stats.AgentStats
	latestAgentBuildsMu sync.RWMutex
	latestAgentBuilds   map[int64]agentBuildIdentity

	// These service fields remain public for package tests during the extraction stack.
	// New construction should go through appServices so they can become private later.
	AgentHub                        *agentHub
	LoadBalancers                   *loadBalancerRegistry
	TargetHealth                    *publicRouteTargetHealthMonitor
	TrafficTracer                   *trafficTracer
	RateLimiter                     *publicRateLimiter
	TrafficShaper                   *publicTrafficShaper
	PublicWAF                       *publicWAF
	PublicCache                     *publicProxyCache
	PublicACME                      *publicACMEManager
	GeoConfigRefresher              PublicGeoConfigRefresher
	publicConfig                    *publicConfigService
	proxyRuntime                    *proxyRuntime
	observabilityRecorder           *observabilityRecorder
	auth                            *authService
	AgentTransports                 *agentTransportPool
	DirectTransports                *directTransportPool
	reverseProxyBuffers             httputil.BufferPool
	DashboardCache                  *dashboardResponseCache
	LoginThrottle                   *loginThrottle
	clientLoginThrottle             *loginThrottle
	publicAccessLoginThrottle       *loginThrottle
	publicAccessClientLoginThrottle *loginThrottle
	publicAccessLoginNonces         *publicAccessLoginNonceStore
	agentAuthLocks                  *agentAuthLockMap
	agentStreamCapacity             *agentStreamCapacityManager
	publicProxyRequests             *requestCapacityLimiter
	publicClientRequests            *keyedStringRequestCapacityLimiter
	publicConnections               *publicConnectionCapacityLimiter
	publicTargetRequests            *keyedRequestCapacityLimiter
	retryReplayBudget               *retryReplayBudget
	agentResourcePressureUntil      sync.Map // agent ID -> UnixNano routing cooldown
	agentCapacityLogLastUnixNano    atomic.Int64
	agentCapacityLogSuppressed      atomic.Uint64
	agentCapacityLogger             *zerolog.Logger
	publicConnectionLimitRejected   atomic.Uint64
	publicConnectionResourceReject  atomic.Uint64
	publicClientRequestRejected     atomic.Uint64
	managementClientIdentity        *ClientIdentityResolver
	managementClientIdentityErr     error
	ManagementTLS                   *ManagementTLSRuntime
	// TrustedAgentUpdates resolves only manifests already verified against the
	// configured release trust root. Managed campaigns fail closed when unset.
	TrustedAgentUpdates           TrustedAgentUpdateCatalog
	AgentUpdateBootstrap          AgentUpdateBootstrapProvider
	AgentUpdateAuthority          AgentUpdateManagementAuthority
	AgentUpdateAuthorityWarning   string
	agentUpdatesMu                sync.Mutex
	agentUpdateTrafficMu          sync.RWMutex
	agentUpdateCordoned           atomic.Pointer[map[int64]struct{}] // immutable agent ID set
	agentUpdateFreshTunnelWrite   func(context.Context, string, ...any) (sql.Result, error)
	agentUpdateCatalogWarningOnce sync.Once
	agentUpdateRequestGate        chan struct{}
	agentUpdatePeerRequests       *keyedStringRequestCapacityLimiter
	agentUpdateIdentityRequests   *keyedRequestCapacityLimiter
	agentUpdateGlobalRate         *agentUpdateRateLimiter
	agentUpdatePeerRate           *agentUpdateRateLimiter
	agentUpdateIdentityRate       *agentUpdateRateLimiter
	agentUpdateDrainReady         func(int64) bool
	agentUpdateRouteBlockersHook  func(int64, int64) []string
	agentUpdateBeforeSuccessCAS   func()
	agentUpdateMaintenanceStarted atomic.Bool

	ProxyIsRunning atomic.Bool
	ProxyLastError atomic.Pointer[string]

	setupMu             sync.Mutex
	setupTokenHash      string
	generatedSetupToken string
	setupTokenLogOnce   sync.Once

	proxyMu                  sync.Mutex
	proxyServiceActive       bool
	proxyState               p2pstreamv1.ProxyState
	proxyLastError           string
	publicSnapshot           *publicProxySnapshot
	publicSnapshotPtr        atomic.Pointer[publicProxySnapshot]
	publicSnapshotGeneration uint64
	publicListenerState      map[int64]*publicListenerRuntime

	publicConfigCacheMu sync.RWMutex
	publicConfigCache   cachedPublicConfig
	publicGeoConfigMu   sync.Mutex

	publicGeoMaintenanceMu      sync.Mutex
	publicGeoMaintenanceStarted bool
	publicGeoMaintenanceCancel  context.CancelFunc
	publicGeoMaintenanceWG      sync.WaitGroup

	observabilityMu          sync.Mutex
	observabilityLastCleanup time.Time

	agentTunnelBeforeFinalAuth            func(db.Agent)
	agentDisconnectAfterHubRemoval        func(*AgentConn, bool)
	publicTLSSelectorRefreshBeforePublish func(uint64)
}

const managementRPCMaxMessageBytes = 8 << 20

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
	app, err := NewAppWithError(cfg, database)
	if err != nil {
		panic(fmt.Sprintf("initialize server application: %v", err))
	}
	return app
}

// NewAppWithError constructs the production application and reports failures
// that would make persisted routing safety state unavailable.
func NewAppWithError(cfg *config.Config, database *db.DB) (*App, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	managementIdentity, managementIdentityErr := newManagementClientIdentityResolver(cfg)
	publicMaxRequests, publicMaxRequestsPerTarget := effectivePublicRequestCapacities(
		cfg.PublicMaxConcurrentRequests,
		cfg.PublicMaxConcurrentPerTarget,
	)
	app := &App{
		Config:                      cfg,
		DB:                          database,
		StartedAt:                   time.Now(),
		latestAgentStats:            make(map[int64]stats.AgentStats),
		latestAgentBuilds:           make(map[int64]agentBuildIdentity),
		proxyState:                  p2pstreamv1.ProxyState_PROXY_STATE_STOPPED,
		publicListenerState:         make(map[int64]*publicListenerRuntime),
		agentStreamCapacity:         mustNewDefaultAgentStreamCapacityManager(cfg.ServerTunnelMaxConcurrentStreams),
		publicProxyRequests:         newRequestCapacityLimiter(publicMaxRequests, defaultPublicMaxConcurrentRequests),
		publicClientRequests:        newKeyedStringRequestCapacityLimiter(cfg.PublicMaxConcurrentPerClient),
		publicConnections:           newPublicConnectionCapacityLimiter(cfg.PublicMaxConcurrentConnections, cfg.PublicMaxConnectionsPerPeer),
		publicTargetRequests:        newKeyedRequestCapacityLimiter(publicMaxRequestsPerTarget, publicMaxRequests),
		retryReplayBudget:           newRetryReplayBudget(defaultPublicRetryReplayBudgetBytes),
		agentUpdateRequestGate:      make(chan struct{}, 64),
		agentUpdatePeerRequests:     newKeyedStringRequestCapacityLimiter(8),
		agentUpdateIdentityRequests: newKeyedRequestCapacityLimiter(1, 1),
		// A 1,000-agent fleet polling every 30 seconds generates roughly 2,000
		// checks/minute before reports. These guards absorb abuse without turning
		// normal shared-egress fleets into an update outage.
		agentUpdateGlobalRate:       newAgentUpdateRateLimiter(60_000, time.Minute, 1024, 1),
		agentUpdatePeerRate:         newAgentUpdateRateLimiter(6_000, time.Minute, 128, 4096),
		agentUpdateIdentityRate:     newAgentUpdateRateLimiter(30, time.Minute, 8, agentUpdateMaxAgents),
		managementClientIdentity:    managementIdentity,
		managementClientIdentityErr: managementIdentityErr,
	}
	if cfg.ServerTunnelCapacityAuto || cfg.ServerTunnelMaxConcurrentStreams > 0 {
		app.agentStreamCapacity.enableAdaptiveMemory(newServerAdaptiveMemoryController(cfg))
	}
	configDir := strings.TrimSpace(cfg.ConfigDir)
	if configDir == "" {
		configDir = config.DefaultConfigDir
	}
	if geoRuntime, err := NewPublicGeoRuntime(GeoIPCountryDatabasePath(configDir), nil); err == nil {
		app.GeoConfigRefresher = geoRuntime
	}
	app.applyServices(newAppServices(cfg, app))
	if database != nil {
		app.closeStaleAgentConnections(context.Background(), time.Now().UTC())
		app.initializeSetupToken(context.Background())
		app.ensureBootstrapAgent(context.Background())
		if err := app.loadAgentUpdateCordons(context.Background()); err != nil {
			return nil, fmt.Errorf("load managed-update cordons: %w", err)
		}
	}
	return app, nil
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
		connect.WithReadMaxBytes(managementRPCMaxMessageBytes),
		connect.WithCodec(strictProtoJSONCodec{name: "json"}),
		connect.WithCodec(strictProtoJSONCodec{name: "json; charset=utf-8"}),
	)
	mux.Handle(path, a.agentUpdateHTTPAdmission(handler))
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
	admissionLimit := payload.TunnelAdmissionLimit
	if admissionLimit < 0 {
		admissionLimit = 0
	}
	if admissionLimit > tunnel.MaxAdaptiveConcurrentStreamsLimit {
		admissionLimit = tunnel.MaxAdaptiveConcurrentStreamsLimit
	}
	streamsInUse := payload.TunnelStreamsInUse
	if streamsInUse < 0 {
		streamsInUse = 0
	}
	if streamsInUse > tunnel.MaxAdaptiveConcurrentStreamsLimit {
		streamsInUse = tunnel.MaxAdaptiveConcurrentStreamsLimit
	}
	memoryUsageBytes := payload.MemoryUsageBytes
	if memoryUsageBytes < 0 {
		memoryUsageBytes = 0
	}
	memoryLimitBytes := payload.MemoryLimitBytes
	if memoryLimitBytes < 0 {
		memoryLimitBytes = 0
	}
	memoryPressure := normalizedAgentMemoryPressure(payload.MemoryPressure)
	memorySource := normalizedAgentMemorySource(payload.MemorySource)
	fileDescriptorsUsed := nonNegativeAgentResourceMetric(payload.FileDescriptorsUsed)
	fileDescriptorsLimit := nonNegativeAgentResourceMetric(payload.FileDescriptorsLimit)
	resourcePressureReason := normalizedAgentResourcePressureReason(payload.ResourcePressureReason)
	resourceSampleError := normalizedAgentResourceSampleError(payload.ResourceSampleError)
	reportedAt := time.Now().UTC()
	resourceLastGoodAt := normalizedAgentResourceTimestamp(payload.ResourceLastGoodUnixMillis, reportedAt)
	build := agentBuildIdentityFromStats(payload)
	a.storeLatestAgentBuild(agentRow.ID, build)
	a.recordAgentUpdateObservedBuild(agentRow.ID, build)

	s := stats.AgentStats{
		Timestamp:                reportedAt,
		NumGoroutine:             int(payload.NumGoroutine),
		MemorySysMB:              uint64(payload.MemorySysMb),
		ActiveRequests:           payload.ActiveRequests,
		CPUPercent:               payload.CpuPercent,
		TunnelCapacityAdaptive:   payload.TunnelCapacityAdaptive,
		TunnelAdmissionLimit:     admissionLimit,
		TunnelStreamsInUse:       streamsInUse,
		MemoryPressure:           memoryPressure,
		MemoryUsageBytes:         memoryUsageBytes,
		MemoryLimitBytes:         memoryLimitBytes,
		MemorySource:             memorySource,
		TunnelPressureRejections: payload.TunnelPressureRejections,
		TunnelLimitRejections:    payload.TunnelLimitRejections,
		FileDescriptorsUsed:      fileDescriptorsUsed,
		FileDescriptorsLimit:     fileDescriptorsLimit,
		ResourcePressureReason:   resourcePressureReason,
		ResourceSampleError:      resourceSampleError,
		ResourceLastGoodAt:       resourceLastGoodAt,
		ReqSuccess:               int32(payload.ReqSuccess),
		ReqClientError:           int32(payload.ReqClientError),
		ReqServerError:           int32(payload.ReqServerError),
		ReqInternalError:         int32(payload.ReqInternalError),
		BytesReceived:            payload.BytesReceived,
		BytesSent:                payload.BytesSent,
	}

	a.LatestAgentStats.Store(&s)
	a.storeLatestAgentStats(agentRow.ID, s)
	if conn := a.AgentHub.connectedByID(agentRow.ID); conn != nil && conn.AdaptiveCapacity && payload.TunnelCapacityAdaptive {
		remoteLimit := admissionLimit
		if memoryPressure == "critical" {
			remoteLimit = 0
		}
		if remoteLimit > conn.NegotiatedMaxConcurrentStreams {
			remoteLimit = conn.NegotiatedMaxConcurrentStreams
		}
		conn.CurrentAdmissionLimit.Store(remoteLimit)
	}

	log.Debug().
		Str("agent", agentRow.PublicID).
		Int64("mem_mb", payload.MemorySysMb).
		Int64("goroutines", payload.NumGoroutine).
		Int64("req_success", payload.ReqSuccess).
		Int64("req_err", payload.ReqServerError).
		Msg("Agent Health")

	if a.DB != nil {
		if err := a.DB.UpdateAgentBuild(ctx, db.UpdateAgentBuildParams{
			AgentVersion: build.Version,
			AgentCommit:  build.Commit,
			ID:           agentRow.ID,
		}); err != nil {
			log.Error().Err(err).Str("agent", agentRow.PublicID).Msg("Failed to record agent build identity")
		}
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

	response := &p2pstreamv1.AgentStatsResponse{}
	if a.ManagementTLS != nil {
		trustReportRecorded := true
		if err := a.ManagementTLS.recordTrustReport(ctx, agentRow.ID, payload.ManagementTrustStatus); err != nil {
			trustReportRecorded = false
			log.Error().Err(err).Str("agent", agentRow.PublicID).Msg("Failed to record agent management trust status")
		}
		if trustReportRecorded {
			response.ManagementTrustUpdate = a.ManagementTLS.trustUpdate(payload.ManagementTrustStatus)
		}
	}
	return connect.NewResponse(response), nil
}

func normalizedAgentMemoryPressure(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "healthy", "soft", "critical", "unknown":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func normalizedAgentMemorySource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cgroup_v1", "cgroup_v2", "go", "host", "test", "benchmark":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizedAgentResourcePressureReason(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "memory", "file_descriptors", "sensor":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

// Resource sample errors originate on a remotely authenticated but still
// untrusted agent. Preserve the state without storing arbitrary error text or
// host paths in server logs/API responses.
func normalizedAgentResourceSampleError(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "unavailable"
}

func nonNegativeAgentResourceMetric(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normalizedAgentResourceTimestamp(value int64, now time.Time) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	parsed := time.UnixMilli(value).UTC()
	if parsed.After(now) {
		return now
	}
	return parsed
}

func (a *App) storeLatestAgentStats(agentID int64, stat stats.AgentStats) {
	a.latestAgentStatsMu.Lock()
	if a.latestAgentStats == nil {
		a.latestAgentStats = make(map[int64]stats.AgentStats)
	}
	a.latestAgentStats[agentID] = stat
	a.latestAgentStatsMu.Unlock()
}

type agentBuildIdentity struct {
	Version string
	Commit  string
}

func agentBuildIdentityFromStats(payload *p2pstreamv1.AgentStatsRequest) agentBuildIdentity {
	if payload == nil {
		return agentBuildIdentity{}
	}
	version := payload.AgentVersion
	if strings.TrimSpace(version) == "" && payload.ManagementTrustStatus != nil {
		// Compatibility with agents released before build identity was promoted
		// to a top-level heartbeat field.
		version = payload.ManagementTrustStatus.AgentVersion
	}
	return agentBuildIdentity{
		Version: truncateProxyRequestContextValue(version, 128),
		Commit:  truncateProxyRequestContextValue(payload.AgentCommit, 128),
	}
}

func (a *App) storeLatestAgentBuild(agentID int64, build agentBuildIdentity) {
	if a == nil || agentID <= 0 {
		return
	}
	a.latestAgentBuildsMu.Lock()
	if a.latestAgentBuilds == nil {
		a.latestAgentBuilds = make(map[int64]agentBuildIdentity)
	}
	a.latestAgentBuilds[agentID] = build
	a.latestAgentBuildsMu.Unlock()
}

func (a *App) latestAgentBuildSnapshot(agentID int64) (agentBuildIdentity, bool) {
	if a == nil || agentID <= 0 {
		return agentBuildIdentity{}, false
	}
	a.latestAgentBuildsMu.RLock()
	build, ok := a.latestAgentBuilds[agentID]
	a.latestAgentBuildsMu.RUnlock()
	return build, ok
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

func (a *App) agentHasFreshCriticalMemoryPressure(agentID int64, now time.Time) bool {
	if a == nil {
		return false
	}
	a.latestAgentStatsMu.RLock()
	stat, ok := a.latestAgentStats[agentID]
	a.latestAgentStatsMu.RUnlock()
	if !ok || stat.MemoryPressure != "critical" || stat.Timestamp.IsZero() {
		return false
	}
	if conn := a.AgentHub.connectedByID(agentID); conn != nil && !conn.ConnectedAt.IsZero() && stat.Timestamp.Before(conn.ConnectedAt) {
		return false
	}
	return now.Sub(stat.Timestamp) >= 0 && now.Sub(stat.Timestamp) <= 15*time.Second
}

func agentStatsSnapshotFromRuntime(stat stats.AgentStats) *p2pstreamv1.AgentStatsSnapshot {
	return &p2pstreamv1.AgentStatsSnapshot{
		MemorySysMb:                int64(stat.MemorySysMB),
		NumGoroutine:               int64(stat.NumGoroutine),
		ReqSuccess:                 int64(stat.ReqSuccess),
		ReqClientError:             int64(stat.ReqClientError),
		ReqServerError:             int64(stat.ReqServerError),
		ReqInternalError:           int64(stat.ReqInternalError),
		BytesReceived:              stat.BytesReceived,
		BytesSent:                  stat.BytesSent,
		ActiveRequests:             stat.ActiveRequests,
		CpuPercent:                 stat.CPUPercent,
		TunnelCapacityAdaptive:     stat.TunnelCapacityAdaptive,
		TunnelAdmissionLimit:       stat.TunnelAdmissionLimit,
		TunnelStreamsInUse:         stat.TunnelStreamsInUse,
		MemoryPressure:             stat.MemoryPressure,
		MemoryUsageBytes:           stat.MemoryUsageBytes,
		MemoryLimitBytes:           stat.MemoryLimitBytes,
		MemorySource:               stat.MemorySource,
		TunnelPressureRejections:   stat.TunnelPressureRejections,
		TunnelLimitRejections:      stat.TunnelLimitRejections,
		FileDescriptorsUsed:        stat.FileDescriptorsUsed,
		FileDescriptorsLimit:       stat.FileDescriptorsLimit,
		ResourcePressureReason:     stat.ResourcePressureReason,
		ResourceSampleError:        stat.ResourceSampleError,
		ResourceLastGoodUnixMillis: unixMillisOrZero(stat.ResourceLastGoodAt),
		ReportedAtUnixMillis:       stat.Timestamp.UnixMilli(),
	}
}

func unixMillisOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
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
		Version:        buildinfo.Version,
		Commit:         buildinfo.Commit,
		ReleaseChannel: buildinfo.Channel,
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
	advertisedStreams, advertised, err := tunnel.ParseOptionalMaxConcurrentStreams(
		r.Header.Get(tunnel.TunnelMaxConcurrentStreamsHeader),
		tunnel.MaxConcurrentAgentRequestsLimit,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !advertised {
		advertisedStreams = tunnel.DefaultMaxConcurrentAgentRequests
	}
	capacityMode, _, err := tunnel.ParseOptionalCapacityMode(r.Header.Get(tunnel.TunnelCapacityModeHeader))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	adaptiveCapacity := capacityMode == tunnel.TunnelCapacityModeAdaptive
	serverStreams := tunnel.DefaultServerMaxConcurrentStreams
	if a.agentStreamCapacity != nil {
		serverStreams = int64(a.agentStreamCapacity.snapshot().Total.Capacity)
	}
	negotiatedStreams := advertisedStreams
	if adaptiveCapacity {
		negotiatedStreams = serverStreams
	} else if negotiatedStreams > serverStreams {
		negotiatedStreams = serverStreams
	}
	// A tunnel session is capped far below MaxInt32, but keep the conversion
	// locally and mechanically safe on every architecture.
	if negotiatedStreams < 1 || negotiatedStreams > math.MaxInt32 {
		http.Error(w, "negotiated tunnel capacity is unsupported on this architecture", http.StatusInternalServerError)
		return
	}
	negotiatedStreamLimit := int(negotiatedStreams)
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

	agent := &AgentConn{
		AgentID:                        agentRow.ID,
		PublicID:                       agentRow.PublicID,
		Name:                           agentRow.Name,
		Done:                           make(chan struct{}),
		AdvertisedMaxConcurrentStreams: advertisedStreams,
		NegotiatedMaxConcurrentStreams: negotiatedStreams,
		AdaptiveCapacity:               adaptiveCapacity,
		ConnectedAt:                    time.Now(),
		BuildVersion:                   truncateProxyRequestContextValue(r.Header.Get(tunnel.TunnelAgentVersionHeader), 128),
		BuildCommit:                    truncateProxyRequestContextValue(r.Header.Get(tunnel.TunnelAgentCommitHeader), 128),
	}
	if adaptiveCapacity {
		// The negotiated value is only a protocol guard. Do not present it as a
		// live allowance until the newly connected agent reports a resource
		// snapshot for this connection generation.
		agent.CurrentAdmissionLimit.Store(0)
	} else {
		agent.CurrentAdmissionLimit.Store(negotiatedStreams)
	}
	effectiveStreamWindowBytes, err := effectiveServerTunnelStreamWindowBytes(a.Config)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Invalid resource-accounted agent tunnel yamux configuration")
		http.Error(w, "invalid agent tunnel resource configuration", http.StatusInternalServerError)
		return
	}
	yamuxConfig, err := tunnel.NewYamuxConfig(nil, effectiveStreamWindowBytes)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Invalid agent tunnel yamux configuration")
		http.Error(w, "invalid agent tunnel configuration", http.StatusInternalServerError)
		return
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
	_, _ = rw.WriteString(tunnel.TunnelMaxConcurrentStreamsHeader + ": " + strconv.FormatInt(negotiatedStreams, 10) + "\r\n")
	if adaptiveCapacity {
		_, _ = rw.WriteString(tunnel.TunnelCapacityModeHeader + ": " + tunnel.TunnelCapacityModeAdaptive + "\r\n")
	}
	_, _ = rw.WriteString("\r\n")
	if err := rw.Flush(); err != nil {
		_ = rawConn.Close()
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Failed to write agent tunnel upgrade response")
		return
	}

	// Yamux starts its receive loop before Server returns. Hold reads until
	// GoAway has set localGoAway so even a SYN sent during session setup is RST
	// instead of entering the unbudgeted accept backlog.
	gatedConn := &agentTunnelReadGateConn{Conn: rawConn, ready: make(chan struct{})}
	session, err := yamux.Server(gatedConn, yamuxConfig)
	if err != nil {
		gatedConn.releaseReads()
		_ = rawConn.Close()
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Failed to initialize agent tunnel session")
		return
	}
	// Agent tunnel streams are always server-initiated. Fence the session before
	// exposing it so unsolicited agent SYNs are rejected instead of occupying
	// Yamux's unbudgeted accept backlog.
	if err := session.GoAway(); err != nil {
		gatedConn.releaseReads()
		_ = session.Close()
		_ = rawConn.Close()
		log.Error().Err(err).Str("agent", agent.PublicID).Msg("Failed to restrict agent tunnel stream direction")
		return
	}
	gatedConn.releaseReads()
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
	sessionCapacityKey := agentStreamCapacitySessionKey(agent, session)
	if a.agentStreamCapacity != nil {
		// Register before publishing through AgentHub so a selected connection
		// can never receive the unregistered single-session allowance.
		a.agentStreamCapacity.registerSessionWithLimit(sessionCapacityKey, negotiatedStreamLimit)
	}
	displaced, err := a.AgentHub.replace(agent)
	if err != nil {
		if a.agentStreamCapacity != nil {
			a.agentStreamCapacity.unregisterSession(sessionCapacityKey)
		}
		_ = session.Close()
		if a.DB != nil && agent.ConnectionDBID > 0 {
			if err := a.DB.UpdateConnectionDisconnected(context.Background(), agent.ConnectionDBID); err != nil {
				log.Warn().Err(err).Msg("Failed to update rejected connection disconnection time")
			}
			if err := a.DB.MarkAgentDisconnected(context.Background(), agent.AgentID); err != nil {
				log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Failed to update rejected agent disconnected timestamp")
			}
		}
		log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Failed to register agent tunnel")
		return
	}
	for _, old := range displaced {
		a.retireDisplacedAgentConnection(old)
	}
	if a.TargetHealth != nil {
		a.TargetHealth.recordAgentConnectedForAll(agent.AgentID, agent.PublicID)
	}
	// A campaign only accepts a tunnel established after the attested
	// activation edge. Run persistence asynchronously after the tunnel is
	// published so database latency cannot delay tunnel admission.
	go a.recordAgentUpdateFreshTunnel(agent)
	unlockAgentAuth()

	log.Info().
		Str("remote_addr", r.RemoteAddr).
		Str("agent", agent.PublicID).
		Int("tunnel_version", tunnel.ProtocolVersion).
		Int64("advertised_max_streams", advertisedStreams).
		Int64("negotiated_max_streams", negotiatedStreams).
		Bool("adaptive_capacity", adaptiveCapacity).
		Msg("Agent tunnel connected successfully")

	go func() {
		select {
		case <-agent.Done:
			_ = session.Close()
		case <-session.CloseChan():
		}
		a.cleanupAgentConnection(agent)
		log.Info().
			Str("agent", agent.PublicID).
			Int64("duration_ms", time.Since(agent.ConnectedAt).Milliseconds()).
			Int64("active_requests", agent.ActiveRequests.Load()).
			Msg("Agent tunnel disconnected")
	}()
}

// effectiveServerTunnelStreamWindowBytes keeps the Yamux receive credit within
// the per-stream lifetime charge whenever the server resource ledger is active.
// An explicit stream count is only an additional ceiling; it must not disable
// the memory/FD safety model used for automatic capacity.
func effectiveServerTunnelStreamWindowBytes(cfg *config.Config) (int64, error) {
	if cfg == nil {
		return tunnel.DefaultMaxStreamWindowSizeBytes, nil
	}
	if cfg.ServerTunnelCapacityAuto || cfg.ServerTunnelMaxConcurrentStreams > 0 {
		return tunnel.AdaptiveMaxStreamWindowSizeBytes(
			cfg.TunnelMaxStreamWindowBytes,
			cfg.ServerTunnelEstimatedStreamBytes,
		)
	}
	window, err := tunnel.NormalizeMaxStreamWindowSizeBytes(cfg.TunnelMaxStreamWindowBytes)
	return int64(window), err
}

func (a *App) cleanupAgentConnection(agent *AgentConn) bool {
	if a == nil || agent == nil {
		return false
	}
	unlock := a.lockAgentAuth(agent.AgentID)
	defer unlock()

	disconnected := false
	if a.AgentHub != nil {
		disconnected = a.AgentHub.disconnect(agent)
	}
	if a.agentStreamCapacity != nil && agent.Session != nil {
		a.agentStreamCapacity.unregisterSession(agentStreamCapacitySessionKey(agent, agent.Session))
	}
	if hook := a.agentDisconnectAfterHubRemoval; hook != nil {
		hook(agent, disconnected)
	}
	if disconnected && a.TargetHealth != nil {
		a.TargetHealth.recordAgentDisconnectedForAll(agent.AgentID)
	}
	if a.DB != nil && agent.ConnectionDBID > 0 {
		if err := a.DB.UpdateConnectionDisconnected(context.Background(), agent.ConnectionDBID); err != nil {
			log.Warn().Err(err).Msg("Failed to update disconnection time")
		}
		if disconnected {
			if err := a.DB.MarkAgentDisconnected(context.Background(), agent.AgentID); err != nil {
				log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Failed to update agent disconnected timestamp")
			}
		}
	}
	return disconnected
}

func (a *App) retireDisplacedAgentConnection(agent *AgentConn) {
	if agent == nil {
		return
	}
	log.Warn().
		Str("agent", agent.PublicID).
		Int64("active_requests", agent.ActiveRequests.Load()).
		Msg("Replacing existing agent tunnel with newer authenticated connection")
	if a.AgentTransports != nil {
		a.AgentTransports.closeAgentConnection(agent)
	}
	if a.agentStreamCapacity != nil && agent.Session != nil {
		a.agentStreamCapacity.unregisterSession(agentStreamCapacitySessionKey(agent, agent.Session))
	}
	agent.signalDone()
	if agent.Session != nil {
		if err := agent.Session.Close(); err != nil {
			log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Failed to close displaced agent tunnel session")
		}
	}
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
