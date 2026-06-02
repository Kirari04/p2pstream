package server

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/managementui"
	"p2pstream/msg"
	"p2pstream/stats"
)

type AgentConn struct {
	AgentID        int64
	PublicID       string
	Name           string
	WriteCh        chan *msg.Request
	Done           chan struct{}
	ActiveRequests atomic.Int64
	ConnectedAt    time.Time
	ConnectionDBID int64
}

type App struct {
	Config             *config.Config
	DB                 *db.DB
	StartedAt          time.Time
	PendingRequests    sync.Map // map[uuid.UUID]*pendingAgentRequest
	LateAgentResponses *lateAgentResponseTracker
	LatestAgentStats   atomic.Pointer[stats.AgentStats]
	AgentHub           *agentHub
	LoadBalancers      *loadBalancerRegistry
	BackendHealth      *publicBackendHealthMonitor
	TrafficTracer      *trafficTracer
	RateLimiter        *publicRateLimiter
	TrafficShaper      *publicTrafficShaper
	PublicWAF          *publicWAF
	PublicCache        *publicProxyCache
	PublicACME         *publicACMEManager
	LoginThrottle      *loginThrottle

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

	observabilityMu          sync.Mutex
	observabilityLastCleanup time.Time
}

func NewApp(cfg *config.Config, database *db.DB) *App {
	if cfg == nil {
		cfg = &config.Config{}
	}
	app := &App{
		Config:              cfg,
		DB:                  database,
		StartedAt:           time.Now(),
		LateAgentResponses:  newLateAgentResponseTracker(),
		AgentHub:            newAgentHub(),
		LoadBalancers:       newLoadBalancerRegistry(),
		BackendHealth:       newPublicBackendHealthMonitor(),
		TrafficTracer:       newTrafficTracer(),
		RateLimiter:         newPublicRateLimiter(),
		TrafficShaper:       newPublicTrafficShaper(),
		PublicWAF:           newPublicWAF(),
		PublicCache:         newPublicProxyCache(cfg.PublicCacheDir),
		LoginThrottle:       newLoginThrottle(cfg.LoginThrottleMaxKeys),
		proxyState:          p2pstreamv1.ProxyState_PROXY_STATE_STOPPED,
		publicListenerState: make(map[int64]*publicListenerRuntime),
	}
	app.PublicACME = newPublicACMEManager(app)
	if database != nil {
		app.initializeSetupToken(context.Background())
		app.ensureBootstrapAgent(context.Background())
	}
	return app
}

// RegisterManagementRoutes attaches the WebSocket and ConnectRPC APIs (Port 8081).
func (a *App) RegisterManagementRoutes(mux *http.ServeMux) {
	mux.HandleFunc(sourceOfferPath, sourceOfferHandler)
	mux.HandleFunc("/ws", a.wsHandler)
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
		resp.LatestAgentStats = &p2pstreamv1.AgentStatsSnapshot{
			MemorySysMb:          int64(latest.AllocAllocated),
			NumGoroutine:         int64(latest.NumGoroutine),
			ReqSuccess:           int64(latest.ReqSuccess),
			ReqClientError:       int64(latest.ReqClientError),
			ReqServerError:       int64(latest.ReqServerError),
			ReqInternalError:     int64(latest.ReqInternalError),
			BytesReceived:        latest.BytesReceived,
			BytesSent:            latest.BytesSent,
			ActiveRequests:       latest.ActiveRequests,
			CpuPercent:           latest.CPUPercent,
			ReportedAtUnixMillis: latest.Timestamp.UnixMilli(),
		}
	}

	return resp
}

func (a *App) wsHandler(w http.ResponseWriter, r *http.Request) {
	agentRow, err := a.authenticateAgent(r.Context(), r.Header.Get("X-P2PStream-Agent-ID"), r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("websocket accept error")
		return
	}
	defer c.Close(websocket.StatusInternalError, "internal server error")

	c.SetReadLimit(128 * 1024)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var connID int64
	if a.DB != nil {
		id, err := a.DB.InsertConnection(ctx, sql.NullInt64{Int64: agentRow.ID, Valid: true})
		if err == nil {
			connID = id
			if err := a.DB.MarkAgentConnected(ctx, agentRow.ID); err != nil {
				log.Warn().Err(err).Str("agent", agentRow.PublicID).Msg("Failed to update agent connected timestamp")
			}
		} else {
			log.Warn().Err(err).Msg("Failed to insert connection into DB")
		}
	}

	agent := &AgentConn{
		AgentID:        agentRow.ID,
		PublicID:       agentRow.PublicID,
		Name:           agentRow.Name,
		WriteCh:        make(chan *msg.Request, 100),
		Done:           make(chan struct{}),
		ConnectedAt:    time.Now(),
		ConnectionDBID: connID,
	}

	if err := a.AgentHub.connect(agent); err != nil {
		log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Rejecting duplicate agent connection")
		c.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	if a.BackendHealth != nil {
		a.BackendHealth.recordAgentConnectedForAll(agent.AgentID, agent.PublicID)
	}
	defer func() {
		a.AgentHub.disconnect(agent)
		if a.BackendHealth != nil {
			a.BackendHealth.recordAgentDisconnectedForAll(agent.AgentID)
		}
		a.failPendingRequestsForAgent(agent.AgentID, errAgentDisconnected)
	}()

	log.Info().
		Str("remote_addr", r.RemoteAddr).
		Str("agent", agent.PublicID).
		Msg("Agent connected successfully")

	go func() {
		defer cancel()
		for {
			select {
			case <-agent.Done:
				return
			case m := <-agent.WriteCh:
				cw, err := c.Writer(ctx, websocket.MessageBinary)
				if err != nil {
					log.Error().Err(err).Msg("ws write error")
					c.Close(websocket.StatusInternalError, "websocket write failed")
					return
				}
				_, err = m.WriteTo(cw)
				closeErr := cw.Close()
				if err != nil {
					log.Error().Err(err).Msg("failed to write message to ws")
					c.Close(websocket.StatusInternalError, "websocket write failed")
					return
				}
				if closeErr != nil {
					log.Error().Err(closeErr).Msg("failed to close ws writer")
					c.Close(websocket.StatusInternalError, "websocket write failed")
					return
				}
			}
		}
	}()

	for {
		_, reader, err := c.Reader(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("ws read loop ended")
			break
		}

		b, err := io.ReadAll(reader)
		if err != nil {
			log.Error().Err(err).Msg("ws ReadAll error")
			break
		}

		// Use bytes.NewReader here since msg.ParseRequest expects an io.Reader
		m, err := msg.ParseRequest(bytes.NewReader(b))
		if err != nil {
			log.Error().Err(err).Msg("failed to parse message")
			continue
		}

		if pendingValue, ok := a.PendingRequests.Load(m.ID); ok {
			pending := pendingValue.(*pendingAgentRequest)
			if pending.AgentID != agent.AgentID {
				log.Warn().
					Str("req_id", m.ID.String()).
					Str("from_agent", agent.PublicID).
					Str("expected_agent", pending.AgentPublicID).
					Msg("Received response from wrong agent")
				continue
			}
			select {
			case pending.ResponseCh <- m:
			case <-pending.ctx.Done():
			case <-agent.Done:
			case <-ctx.Done():
			}
		} else {
			if reason, ok := a.LateAgentResponses.lookup(m.ID); ok {
				log.Debug().Str("req_id", m.ID.String()).Str("reason", reason).Msg("Received late message for completed request")
			} else {
				log.Warn().Str("req_id", m.ID.String()).Msg("Received message for unknown request")
			}
		}
	}

	log.Info().Str("agent", agent.PublicID).Msg("Agent disconnected")
	if a.DB != nil && connID > 0 {
		if err := a.DB.UpdateConnectionDisconnected(context.Background(), connID); err != nil {
			log.Warn().Err(err).Msg("Failed to update disconnection time")
		}
		if err := a.DB.MarkAgentDisconnected(context.Background(), agent.AgentID); err != nil {
			log.Warn().Err(err).Str("agent", agent.PublicID).Msg("Failed to update agent disconnected timestamp")
		}
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
