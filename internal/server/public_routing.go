package server

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"p2pstream/httpmsg"
	"p2pstream/msg"
)

type publicBackendConfig struct {
	ID                    int64
	Name                  string
	TargetOrigin          string
	BackendType           string
	TLSSkipVerify         bool
	StaticStatusCode      int
	StaticResponseHeaders []publicResponseHeader
	StaticResponseBody    string
	Enabled               bool
	ParsedOrigin          *url.URL
}

type publicResponseHeader struct {
	Name  string
	Value string
}

type publicListenerConfig struct {
	ID               int64
	Name             string
	BindAddress      string
	Port             int64
	Protocol         string
	Enabled          bool
	DefaultBackendID int64
}

type publicRouteConfig struct {
	ID          int64
	ListenerID  int64
	Priority    int64
	HostPattern string
	PathPrefix  string
	BackendID   int64
	Enabled     bool
}

type publicTLSCertificateConfig struct {
	ID              int64
	ListenerID      int64
	HostnamePattern string
	CertPath        string
	KeyPath         string
	Enabled         bool
}

type publicProxySnapshot struct {
	Backends         map[int64]publicBackendConfig
	Listeners        map[int64]publicListenerConfig
	RoutesByListener map[int64][]publicRouteConfig
	CertsByListener  map[int64][]publicTLSCertificateConfig
}

type publicRouteResolution struct {
	Backend    publicBackendConfig
	ListenerID sql.NullInt64
	BackendID  sql.NullInt64
	RouteID    sql.NullInt64
}

func (a *App) publicProxyHandler(listenerID int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resolution, err := a.resolvePublicRoute(listenerID, r)
		if err != nil {
			a.recordProxyRequestEventWithIDs(
				context.Background(),
				http.StatusBadGateway,
				0,
				"route_resolution_failed",
				sql.NullInt64{Int64: listenerID, Valid: true},
				sql.NullInt64{},
				sql.NullInt64{},
			)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if resolution.Backend.BackendType == publicBackendTypeStatic {
			a.staticBackendResponse(w, r, resolution)
			return
		}
		a.proxyRequest(w, r, resolution)
	}
}

func (a *App) staticBackendResponse(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution) {
	startedAt := time.Now()
	statusCode := resolution.Backend.StaticStatusCode
	if statusCode == 0 {
		statusCode = int(defaultStaticStatusCode)
	}
	errorKind := ""
	defer func() {
		a.recordProxyRequestEventWithIDs(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.BackendID,
			resolution.RouteID,
		)
	}()

	for _, header := range resolution.Backend.StaticResponseHeaders {
		w.Header().Add(header.Name, header.Value)
	}
	w.WriteHeader(statusCode)
	if !shouldWriteStaticResponseBody(r.Method, statusCode) {
		return
	}
	_, _ = io.WriteString(w, resolution.Backend.StaticResponseBody)
}

func (a *App) proxyRequest(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution) {
	startedAt := time.Now()
	statusCode := http.StatusOK
	errorKind := ""
	defer func() {
		a.recordProxyRequestEventWithIDs(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.BackendID,
			resolution.RouteID,
		)
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

	targetOrigin := resolution.Backend.ParsedOrigin
	if targetOrigin == nil {
		statusCode = http.StatusBadGateway
		errorKind = "backend_unavailable"
		http.Error(w, "Selected backend is unavailable", http.StatusBadGateway)
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

	r.URL.Scheme = targetOrigin.Scheme
	r.URL.Host = targetOrigin.Host
	r.Host = targetOrigin.Host

	enc := httpmsg.NewRequestEncoderWithMetadata(id, r, map[string]string{
		httpmsg.MetadataTLSSkipVerify: strconv.FormatBool(resolution.Backend.TLSSkipVerify),
	})
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
		_, _ = io.Copy(w, resp.Body)
	}

	log.Info().Str("req_id", id.String()).Int("status", resp.StatusCode).Msg("Finished proxying request")
}

func (a *App) resolvePublicRoute(listenerID int64, r *http.Request) (publicRouteResolution, error) {
	host := normalizeRequestHost(r.Host)

	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	if snap == nil {
		return publicRouteResolution{}, errors.New("public proxy config is not loaded")
	}

	listener, ok := snap.Listeners[listenerID]
	if !ok {
		return publicRouteResolution{}, errors.New("listener not found")
	}

	backendID := listener.DefaultBackendID
	var routeID sql.NullInt64
	for _, route := range snap.RoutesByListener[listenerID] {
		if !route.Enabled {
			continue
		}
		if route.HostPattern != "" && !hostMatchesPattern(host, route.HostPattern) {
			continue
		}
		if route.PathPrefix != "" && !strings.HasPrefix(r.URL.Path, route.PathPrefix) {
			continue
		}
		backendID = route.BackendID
		routeID = sql.NullInt64{Int64: route.ID, Valid: true}
		break
	}

	backend, ok := snap.Backends[backendID]
	if !ok || !backend.Enabled {
		return publicRouteResolution{}, errors.New("selected backend is unavailable")
	}
	if backend.BackendType == publicBackendTypeProxyForward && backend.ParsedOrigin == nil {
		return publicRouteResolution{}, errors.New("selected backend is unavailable")
	}

	return publicRouteResolution{
		Backend:    backend,
		ListenerID: sql.NullInt64{Int64: listenerID, Valid: true},
		BackendID:  sql.NullInt64{Int64: backendID, Valid: true},
		RouteID:    routeID,
	}, nil
}

func normalizeRequestHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if strings.HasPrefix(host, "[") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			return strings.Trim(h, "[]")
		}
		return strings.Trim(host, "[]")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	if idx := strings.LastIndex(host, ":"); idx > -1 && strings.Count(host, ":") == 1 {
		return host[:idx]
	}
	return strings.TrimSuffix(host, ".")
}

func hostMatchesPattern(host string, pattern string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	pattern = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pattern)), ".")
	if pattern == "" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(host, suffix) && len(host) > len(strings.TrimPrefix(suffix, "."))
	}
	return host == pattern
}

func shouldWriteStaticResponseBody(method string, statusCode int) bool {
	if method == http.MethodHead {
		return false
	}
	if statusCode >= 100 && statusCode < 200 {
		return false
	}
	return statusCode != http.StatusNoContent && statusCode != http.StatusNotModified
}

func sortPublicRoutes(routes []publicRouteConfig) {
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Priority == routes[j].Priority {
			return routes[i].ID < routes[j].ID
		}
		return routes[i].Priority < routes[j].Priority
	})
}
