package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"p2pstream/httpmsg"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/msg"
	"p2pstream/stats"
)

type AgentConn struct {
	WriteCh chan *msg.Request
}

type App struct {
	Config           *config.Config
	DB               *db.DB
	ActiveAgent      atomic.Pointer[AgentConn]
	PendingRequests  sync.Map // map[uuid.UUID]chan *msg.Request
	LatestAgentStats atomic.Pointer[stats.AgentStats]
}

func NewApp(cfg *config.Config, database *db.DB) *App {
	return &App{
		Config: cfg,
		DB:     database,
	}
}

func (a *App) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", a.wsHandler)
	mux.HandleFunc("/api/agent/stats", a.statsHandler)
	mux.HandleFunc("/", a.proxyHandler)
}

func (a *App) wsHandler(w http.ResponseWriter, r *http.Request) {
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

// AgentStatsPayload adds validation rules to the raw payload
type AgentStatsPayload struct {
	stats.AgentStats
}

func (p AgentStatsPayload) Validate() error {
	return validation.ValidateStruct(&p,
		validation.Field(&p.AllocAllocated, validation.Min(uint64(0))),
		validation.Field(&p.NumGoroutine, validation.Min(0)),
		validation.Field(&p.ReqSuccess, validation.Min(int32(0))),
		validation.Field(&p.ReqClientError, validation.Min(int32(0))),
		validation.Field(&p.ReqServerError, validation.Min(int32(0))),
		validation.Field(&p.BytesReceived, validation.Min(uint64(0))),
		validation.Field(&p.BytesSent, validation.Min(uint64(0))),
	)
}

func (a *App) statsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var payload AgentStatsPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		if err := payload.Validate(); err != nil {
			log.Warn().Err(err).Msg("Validation failed for agent stats payload")
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		a.LatestAgentStats.Store(&payload.AgentStats)

		log.Debug().
			Uint64("mem_mb", payload.AllocAllocated).
			Int("goroutines", payload.NumGoroutine).
			Int32("req_success", payload.ReqSuccess).
			Int32("req_err", payload.ReqServerError).
			Msg("Agent Health")

		if a.DB != nil {
			err := a.DB.InsertAgentStat(context.Background(), db.InsertAgentStatParams{
				MemoryMb:       int64(payload.AllocAllocated),
				Goroutines:     int64(payload.NumGoroutine),
				ReqSuccess:     int64(payload.ReqSuccess),
				ReqClientError: int64(payload.ReqClientError),
				ReqServerError: int64(payload.ReqServerError),
				BytesRx:        int64(payload.BytesReceived),
				BytesTx:        int64(payload.BytesSent),
			})
			if err != nil {
				log.Error().Err(err).Msg("Failed to insert agent stats into DB")
			}
		}

		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == http.MethodGet {
		s := a.LatestAgentStats.Load()
		if s == nil {
			http.Error(w, "No stats available yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (a *App) proxyHandler(w http.ResponseWriter, r *http.Request) {
	agent := a.ActiveAgent.Load()
	if agent == nil {
		http.Error(w, "No agent connected", http.StatusServiceUnavailable)
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
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
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
		return
	case firstMsg = <-respCh:
	}

	stream := &httpmsg.ChannelStream{Ch: respCh}
	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to decode response headers")
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

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
