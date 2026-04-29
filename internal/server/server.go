package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/httpmsg"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/managementui"
	"p2pstream/msg"
	"p2pstream/stats"
)

type AgentConn struct {
	WriteCh chan *msg.Request
}

type App struct {
	Config           *config.Config
	DB               *db.DB
	StartedAt        time.Time
	ActiveAgent      atomic.Pointer[AgentConn]
	PendingRequests  sync.Map // map[uuid.UUID]chan *msg.Request
	LatestAgentStats atomic.Pointer[stats.AgentStats]

	ProxyIsRunning atomic.Bool
	ProxyLastError atomic.Pointer[string]

	setupMu sync.Mutex

	proxyMu        sync.Mutex
	proxySrv       *http.Server
	proxyState     p2pstreamv1.ProxyState
	proxyLastError string
	proxyStartedAt time.Time
	proxyStoppedAt time.Time

	observabilityMu          sync.Mutex
	observabilityLastCleanup time.Time
}

func NewApp(cfg *config.Config, database *db.DB) *App {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &App{
		Config:     cfg,
		DB:         database,
		StartedAt:  time.Now(),
		proxyState: p2pstreamv1.ProxyState_PROXY_STATE_STOPPED,
	}
}

// RegisterProxyRoutes attaches the standard proxy to the given mux (Port 80/443).
func (a *App) RegisterProxyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", a.proxyHandler)
}

// RegisterManagementRoutes attaches the WebSocket and ConnectRPC APIs (Port 8081).
func (a *App) RegisterManagementRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", a.wsHandler)
	path, handler := p2pstreamv1connect.NewAgentManagementServiceHandler(a)
	mux.Handle(path, handler)
	mux.Handle("/", managementui.NewHandler(a.Config.ManagementUIDevProxy, a.Config.ManagementUIDistDir))
}

// ReportStats implements the ConnectRPC AgentManagementService.
func (a *App) ReportStats(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.AgentStatsRequest],
) (*connect.Response[p2pstreamv1.AgentStatsResponse], error) {
	if !a.validAgentAuthorization(req.Header().Get("Authorization")) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("agent token required"))
	}

	payload := req.Msg

	s := stats.AgentStats{
		Timestamp:        time.Now(),
		NumGoroutine:     int(payload.NumGoroutine),
		AllocAllocated:   uint64(payload.MemorySysMb),
		ActiveRequests:   payload.ActiveRequests,
		ReqSuccess:       int32(payload.ReqSuccess),
		ReqClientError:   int32(payload.ReqClientError),
		ReqServerError:   int32(payload.ReqServerError),
		ReqInternalError: int32(payload.ReqInternalError),
		BytesReceived:    payload.BytesReceived,
		BytesSent:        payload.BytesSent,
	}

	a.LatestAgentStats.Store(&s)

	log.Debug().
		Int64("mem_mb", payload.MemorySysMb).
		Int64("goroutines", payload.NumGoroutine).
		Int64("req_success", payload.ReqSuccess).
		Int64("req_err", payload.ReqServerError).
		Msg("Agent Health")

	if a.DB != nil {
		err := a.DB.InsertAgentStat(ctx, db.InsertAgentStatParams{
			MemoryMb:         payload.MemorySysMb,
			Goroutines:       payload.NumGoroutine,
			ReqSuccess:       payload.ReqSuccess,
			ReqClientError:   payload.ReqClientError,
			ReqServerError:   payload.ReqServerError,
			ReqInternalError: payload.ReqInternalError,
			BytesRx:          int64(payload.BytesReceived),
			BytesTx:          int64(payload.BytesSent),
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
		AgentConnected: a.ActiveAgent.Load() != nil,
		TargetOrigin:   a.Config.TargetOrigin,
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
			ReportedAtUnixMillis: latest.Timestamp.UnixMilli(),
		}
	}

	return resp
}

func (a *App) wsHandler(w http.ResponseWriter, r *http.Request) {
	if !a.validAgentAuthorization(r.Header.Get("Authorization")) {
		http.Error(w, "agent token required", http.StatusUnauthorized)
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("websocket accept error")
		return
	}
	defer c.Close(websocket.StatusInternalError, "internal server error")

	c.SetReadLimit(128 * 1024)
	ctx := context.Background()

	// Log connection to DB
	var connID int64
	if a.DB != nil {
		id, err := a.DB.InsertConnection(ctx)
		if err == nil {
			connID = id
		} else {
			log.Warn().Err(err).Msg("Failed to insert connection into DB")
		}
	}

	agent := &AgentConn{
		WriteCh: make(chan *msg.Request, 100),
	}

	if swapped := a.ActiveAgent.CompareAndSwap(nil, agent); !swapped {
		log.Warn().Msg("Another agent is already connected, rejecting new connection")
		c.Close(websocket.StatusPolicyViolation, "an agent is already connected")
		return
	}
	defer a.ActiveAgent.CompareAndSwap(agent, nil)

	log.Info().Str("remote_addr", r.RemoteAddr).Msg("Agent connected successfully")

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-agent.WriteCh:
				cw, err := c.Writer(ctx, websocket.MessageBinary)
				if err != nil {
					log.Error().Err(err).Msg("ws write error")
					return
				}
				_, err = m.WriteTo(cw)
				cw.Close()
				if err != nil {
					log.Error().Err(err).Msg("failed to write message to ws")
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

		if ch, ok := a.PendingRequests.Load(m.ID); ok {
			ch.(chan *msg.Request) <- m
		} else {
			log.Warn().Str("req_id", m.ID.String()).Msg("Received message for unknown request")
		}
	}

	log.Info().Msg("Agent disconnected")
	if a.DB != nil && connID > 0 {
		if err := a.DB.UpdateConnectionDisconnected(context.Background(), connID); err != nil {
			log.Warn().Err(err).Msg("Failed to update disconnection time")
		}
	}
}

func (a *App) proxyHandler(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	statusCode := http.StatusOK
	errorKind := ""
	defer func() {
		a.recordProxyRequestEvent(context.Background(), statusCode, time.Since(startedAt), errorKind)
	}()

	agent := a.ActiveAgent.Load()
	if agent == nil {
		statusCode = http.StatusServiceUnavailable
		errorKind = "no_agent"
		http.Error(w, "No agent connected", http.StatusServiceUnavailable)
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		statusCode = http.StatusInternalServerError
		errorKind = "request_id_failed"
		http.Error(w, "Failed to generate ID", http.StatusInternalServerError)
		return
	}

	log.Info().
		Str("req_id", id.String()).
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Msg("Proxying request")

	respCh := make(chan *msg.Request, 100)
	a.PendingRequests.Store(id, respCh)
	defer a.PendingRequests.Delete(id)

	r.URL.Scheme = a.Config.ParsedTargetOrigin.Scheme
	r.URL.Host = a.Config.ParsedTargetOrigin.Host
	r.Host = a.Config.ParsedTargetOrigin.Host

	enc := httpmsg.NewRequestEncoder(id, r)
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to encode request chunk")
			statusCode = http.StatusInternalServerError
			errorKind = "request_encode_failed"
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		agent.WriteCh <- m
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var firstMsg *msg.Request
	select {
	case <-timeoutCtx.Done():
		statusCode = http.StatusGatewayTimeout
		errorKind = "agent_timeout"
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
		return
	case firstMsg = <-respCh:
	}

	stream := &httpmsg.ChannelStream{Ch: respCh}
	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to decode response headers")
		statusCode = http.StatusBadGateway
		errorKind = "response_decode_failed"
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	statusCode = resp.StatusCode

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if resp.Body != nil {
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}

	log.Info().Str("req_id", id.String()).Int("status", resp.StatusCode).Msg("Finished proxying request")
}

func (a *App) validAgentAuthorization(header string) bool {
	if a.Config == nil || a.Config.AgentToken == "" {
		return true
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix)) == a.Config.AgentToken
}
