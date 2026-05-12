package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/httpmsg"
	"p2pstream/msg"
)

const publicUpstreamResponseHeaderTimeout = 30 * time.Second

type publicBackendConfig struct {
	ID                     int64
	Name                   string
	TargetOrigin           string
	BackendType            string
	ForwardMode            string
	LoadBalancing          string
	TLSSkipVerify          bool
	StaticStatusCode       int
	StaticResponseHeaders  []publicResponseHeader
	StaticResponseBody     string
	UpstreamRequestHeaders []publicRequestHeader
	UpstreamBasicAuth      publicBackendBasicAuthConfig
	Enabled                bool
	ParsedOrigin           *url.URL
	AgentAssignments       []publicBackendAgentConfig
}

type publicAgentConfig struct {
	ID       int64
	PublicID string
	Name     string
	Enabled  bool
}

type publicBackendAgentConfig struct {
	BackendID int64
	AgentID   int64
	Position  int64
	Weight    int64
	Enabled   bool
}

type publicResponseHeader struct {
	Name  string
	Value string
}

type publicRequestHeader struct {
	Name      string
	Value     string
	Sensitive bool
}

type publicBackendBasicAuthConfig struct {
	Enabled  bool
	Username string
	Password string
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
	ID                         int64
	ListenerID                 int64
	Priority                   int64
	HostPattern                string
	PathPrefix                 string
	BackendID                  int64
	Action                     string
	RedirectTargetMode         string
	RedirectTarget             string
	RedirectStatusCode         int64
	RedirectPreservePathSuffix bool
	RedirectPreserveQuery      bool
	Enabled                    bool
}

type publicTLSCertificateConfig struct {
	ID                int64
	ListenerID        int64
	HostnamePattern   string
	CertPath          string
	KeyPath           string
	Enabled           bool
	Source            string
	ACMEChallengeType string
	Status            string
}

type publicProxySnapshot struct {
	Backends           map[int64]publicBackendConfig
	Agents             map[int64]publicAgentConfig
	Listeners          map[int64]publicListenerConfig
	RoutesByListener   map[int64][]publicRouteConfig
	CertsByListener    map[int64][]publicTLSCertificateConfig
	RateLimitRules     []publicRateLimitRuleConfig
	TrafficShaperRules []publicTrafficShaperRuleConfig
}

type publicRouteResolution struct {
	Backend                             publicBackendConfig
	Listener                            publicListenerConfig
	Route                               publicRouteConfig
	Action                              string
	DefaultRoute                        bool
	ListenerID                          sql.NullInt64
	BackendID                           sql.NullInt64
	RouteID                             sql.NullInt64
	AgentID                             sql.NullInt64
	RateLimitRuleID                     int64
	RateLimitRuleName                   string
	RateLimitAlgorithm                  string
	TrafficShaperRuleID                 int64
	TrafficShaperRuleName               string
	TrafficShaperBudgetScope            string
	TrafficShaperUploadBytesPerSecond   int64
	TrafficShaperDownloadBytesPerSecond int64
	TrafficShaperRequestExemptBytes     int64
	TrafficShaperResponseExemptBytes    int64
}

type proxyRequestObservability struct {
	requestBytes     *atomic.Uint64
	responseRecorder *proxyResponseRecorder
}

func (o proxyRequestObservability) requestBytesValue() uint64 {
	if o.requestBytes == nil {
		return 0
	}
	return o.requestBytes.Load()
}

func (o proxyRequestObservability) responseBytesValue() uint64 {
	if o.responseRecorder == nil {
		return 0
	}
	return o.responseRecorder.responseBytes()
}

func (a *App) publicProxyHandler(listenerID int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestStartedAt := time.Now()
		var requestBytes atomic.Uint64
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = &countingReadCloser{ReadCloser: r.Body, bytes: &requestBytes}
		}
		recorder := &proxyResponseRecorder{ResponseWriter: w}
		responseWriter := http.ResponseWriter(recorder)
		observability := proxyRequestObservability{requestBytes: &requestBytes, responseRecorder: recorder}
		trace := a.newTrafficRequestTrace(r, recorder)
		if trace != nil {
			trace.emitReceived(listenerID)
		}

		if a.PublicACME != nil && a.PublicACME.ServeHTTPChallenge(responseWriter, r) {
			statusCode := recorder.statusCode
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			a.recordProxyRequestEventWithIDs(
				context.Background(),
				statusCode,
				time.Since(requestStartedAt),
				"",
				sql.NullInt64{Int64: listenerID, Valid: true},
				sql.NullInt64{},
				sql.NullInt64{},
				sql.NullInt64{},
				observability.requestBytesValue(),
				observability.responseBytesValue(),
			)
			return
		}

		if decision, allowed := a.checkPublicRateLimits(listenerID, r); !allowed {
			writeRateLimitResponse(responseWriter, decision)
			if trace != nil {
				resolution := publicRouteResolution{
					Listener:           decision.Listener,
					ListenerID:         sql.NullInt64{Int64: listenerID, Valid: true},
					RateLimitRuleID:    decision.Rule.ID,
					RateLimitRuleName:  decision.Rule.Name,
					RateLimitAlgorithm: decision.Rule.Algorithm,
				}
				trace.emit(
					p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RATE_LIMITED,
					&resolution,
					nil,
					decision.StatusCode,
					"rate_limited",
					responseWriter.Header(),
					map[string]string{
						"handler":              "rate_limit",
						"rate_limit_rule_id":   strconv.FormatInt(decision.Rule.ID, 10),
						"rate_limit_rule_name": decision.Rule.Name,
						"rate_limit_algorithm": decision.Rule.Algorithm,
					},
				)
			}
			a.recordProxyRequestEventWithIDs(
				context.Background(),
				decision.StatusCode,
				time.Since(requestStartedAt),
				"",
				sql.NullInt64{Int64: listenerID, Valid: true},
				sql.NullInt64{},
				sql.NullInt64{},
				sql.NullInt64{},
				observability.requestBytesValue(),
				observability.responseBytesValue(),
			)
			return
		}

		var trafficShaperDecision publicTrafficShaperDecision
		trafficShaperSelected := false
		if decision, ok := a.selectPublicTrafficShaper(listenerID, r); ok {
			trafficShaperDecision = decision
			trafficShaperSelected = true
			if trace != nil {
				resolution := publicRouteResolution{
					Listener:   decision.Listener,
					ListenerID: sql.NullInt64{Int64: listenerID, Valid: true},
				}
				applyTrafficShaperResolutionFields(&resolution, decision)
				trace.emit(
					p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_TRAFFIC_SHAPER_SELECTED,
					&resolution,
					nil,
					0,
					"",
					nil,
					map[string]string{
						"handler":                        "traffic_shaper",
						"traffic_shaper_rule_id":         strconv.FormatInt(decision.Rule.ID, 10),
						"traffic_shaper_rule_name":       decision.Rule.Name,
						"traffic_shaper_budget_scope":    decision.Rule.BudgetScope,
						"traffic_shaper_upload_bps":      strconv.FormatInt(decision.Rule.UploadBytesPerSecond, 10),
						"traffic_shaper_download_bps":    strconv.FormatInt(decision.Rule.DownloadBytesPerSecond, 10),
						"traffic_shaper_request_exempt":  strconv.FormatInt(decision.Rule.RequestExemptBytes, 10),
						"traffic_shaper_response_exempt": strconv.FormatInt(decision.Rule.ResponseExemptBytes, 10),
					},
				)
			}
		}

		resolution, err := a.resolvePublicRoute(listenerID, r)
		if err != nil {
			http.Error(responseWriter, err.Error(), http.StatusBadGateway)
			a.recordProxyRequestEventWithIDs(
				context.Background(),
				http.StatusBadGateway,
				time.Since(requestStartedAt),
				"route_resolution_failed",
				sql.NullInt64{Int64: listenerID, Valid: true},
				sql.NullInt64{},
				sql.NullInt64{},
				sql.NullInt64{},
				observability.requestBytesValue(),
				observability.responseBytesValue(),
			)
			if trace != nil {
				failureResolution := publicRouteResolution{ListenerID: sql.NullInt64{Int64: listenerID, Valid: true}}
				if trafficShaperSelected {
					applyTrafficShaperResolutionFields(&failureResolution, trafficShaperDecision)
				}
				trace.emit(
					p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED,
					&failureResolution,
					nil,
					http.StatusBadGateway,
					"route_resolution_failed",
					responseWriter.Header(),
					nil,
				)
			}
			return
		}
		if trafficShaperSelected {
			applyTrafficShaperResolutionFields(&resolution, trafficShaperDecision)
		}
		if trace != nil {
			trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ROUTE_RESOLVED, &resolution, nil, 0, "", nil, nil)
		}
		if resolution.Action == publicRouteActionRedirect {
			a.redirectRouteResponse(responseWriter, r, resolution, trace, observability)
			return
		}
		if trace != nil {
			trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_BACKEND_SELECTED, &resolution, nil, 0, "", nil, nil)
		}
		if resolution.Backend.BackendType == publicBackendTypeStatic {
			a.staticBackendResponse(responseWriter, r, resolution, trace, trafficShaperDecisionIfSelected(trafficShaperDecision, trafficShaperSelected), observability)
			return
		}
		if resolution.Backend.ForwardMode == publicBackendForwardModeAgentPool {
			a.proxyAgentRequest(responseWriter, r, resolution, trace, trafficShaperDecisionIfSelected(trafficShaperDecision, trafficShaperSelected), observability)
			return
		}
		a.proxyDirectRequest(responseWriter, r, resolution, trace, trafficShaperDecisionIfSelected(trafficShaperDecision, trafficShaperSelected), observability)
	}
}

func trafficShaperDecisionIfSelected(decision publicTrafficShaperDecision, selected bool) *publicTrafficShaperDecision {
	if !selected {
		return nil
	}
	return &decision
}

func applyTrafficShaperResolutionFields(resolution *publicRouteResolution, decision publicTrafficShaperDecision) {
	if resolution == nil {
		return
	}
	resolution.TrafficShaperRuleID = decision.Rule.ID
	resolution.TrafficShaperRuleName = decision.Rule.Name
	resolution.TrafficShaperBudgetScope = decision.Rule.BudgetScope
	resolution.TrafficShaperUploadBytesPerSecond = decision.Rule.UploadBytesPerSecond
	resolution.TrafficShaperDownloadBytesPerSecond = decision.Rule.DownloadBytesPerSecond
	resolution.TrafficShaperRequestExemptBytes = decision.Rule.RequestExemptBytes
	resolution.TrafficShaperResponseExemptBytes = decision.Rule.ResponseExemptBytes
}

func (a *App) redirectRouteResponse(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, observability proxyRequestObservability) {
	startedAt := time.Now()
	statusCode := int(resolution.Route.RedirectStatusCode)
	if statusCode == 0 {
		statusCode = int(defaultRedirectStatusCode)
	}
	errorKind := ""
	location, err := redirectLocationForRequest(r, resolution.Route)
	if err != nil {
		statusCode = http.StatusInternalServerError
		errorKind = "redirect_failed"
		http.Error(w, "Redirect configuration is invalid", http.StatusInternalServerError)
	} else {
		w.Header().Set("Location", location)
		w.WriteHeader(statusCode)
	}
	defer func() {
		attributes := map[string]string{
			"handler":              "redirect",
			"route_action":         publicRouteActionRedirect,
			"redirect_target_mode": resolution.Route.RedirectTargetMode,
		}
		if location != "" {
			attributes["redirect_location"] = redactSensitiveTraceURL(location)
		}
		if trace != nil {
			stage := p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT
			if errorKind != "" {
				stage = p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED
			}
			trace.emit(stage, &resolution, nil, statusCode, errorKind, w.Header(), attributes)
		}
		a.recordProxyRequestEventWithIDs(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			sql.NullInt64{},
			resolution.RouteID,
			sql.NullInt64{},
			observability.requestBytesValue(),
			observability.responseBytesValue(),
		)
	}()
}

func (a *App) staticBackendResponse(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, observability proxyRequestObservability) {
	startedAt := time.Now()
	statusCode := resolution.Backend.StaticStatusCode
	if statusCode == 0 {
		statusCode = int(defaultStaticStatusCode)
	}
	errorKind := ""
	defer func() {
		if trace != nil {
			stage := p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT
			if errorKind != "" {
				stage = p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED
			}
			trace.emit(stage, &resolution, nil, statusCode, errorKind, w.Header(), map[string]string{"handler": "static"})
		}
		a.recordProxyRequestEventWithIDs(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.BackendID,
			resolution.RouteID,
			sql.NullInt64{},
			observability.requestBytesValue(),
			observability.responseBytesValue(),
		)
	}()

	for _, header := range resolution.Backend.StaticResponseHeaders {
		w.Header().Add(header.Name, header.Value)
	}
	w.WriteHeader(statusCode)
	if trace != nil {
		trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RESPONDED,
			&resolution,
			nil,
			statusCode,
			"",
			w.Header(),
			map[string]string{"handler": "static"},
		)
	}
	if !shouldWriteStaticResponseBody(r.Method, statusCode) {
		return
	}
	body := io.NopCloser(strings.NewReader(resolution.Backend.StaticResponseBody))
	if shaper != nil {
		body = shaper.wrapDownloadBody(r.Context(), body)
	}
	defer body.Close()
	_, _ = io.Copy(w, body)
}

func (a *App) proxyDirectRequest(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, observability proxyRequestObservability) {
	startedAt := time.Now()
	statusCode := http.StatusOK
	errorKind := ""
	defer func() {
		if trace != nil {
			stage := p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT
			if errorKind != "" {
				stage = p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED
			}
			trace.emit(stage, &resolution, nil, statusCode, errorKind, w.Header(), map[string]string{"handler": "direct"})
		}
		a.recordProxyRequestEventWithIDs(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.BackendID,
			resolution.RouteID,
			sql.NullInt64{},
			observability.requestBytesValue(),
			observability.responseBytesValue(),
		)
	}()

	targetOrigin := resolution.Backend.ParsedOrigin
	if targetOrigin == nil {
		statusCode = http.StatusBadGateway
		errorKind = "backend_unavailable"
		http.Error(w, "Selected backend is unavailable", http.StatusBadGateway)
		return
	}
	if trace != nil {
		trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_STARTED,
			&resolution,
			nil,
			0,
			"",
			nil,
			map[string]string{"handler": "direct", "upstream": redactSensitiveTraceURL(targetOrigin.String())},
		)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(proxyReq *httputil.ProxyRequest) {
			applyUpstreamRequestConfig(proxyReq.Out, resolution.Backend)
			applyTrustedForwardedHeaders(proxyReq.Out, proxyReq.In)
			if shaper != nil {
				proxyReq.Out.Body = shaper.wrapUploadBody(r.Context(), proxyReq.Out.Body)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			statusCode = resp.StatusCode
			if shaper != nil {
				resp.Body = shaper.wrapDownloadBody(r.Context(), resp.Body)
			}
			if trace != nil {
				trace.emit(
					p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RESPONDED,
					&resolution,
					nil,
					resp.StatusCode,
					"",
					resp.Header,
					map[string]string{"handler": "direct"},
				)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error().Err(err).Str("backend", resolution.Backend.Name).Msg("Direct proxy failed")
			statusCode = http.StatusBadGateway
			errorKind = "direct_proxy_failed"
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
		Transport: directProxyTransport(resolution.Backend.TLSSkipVerify),
	}
	proxy.ServeHTTP(w, r)
}

func (a *App) proxyAgentRequest(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, observability proxyRequestObservability) {
	startedAt := time.Now()
	statusCode := http.StatusOK
	errorKind := ""
	var selectedAgentID sql.NullInt64
	var agent *AgentConn
	defer func() {
		if trace != nil {
			stage := p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT
			if errorKind != "" {
				stage = p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED
			}
			trace.emit(stage, &resolution, agent, statusCode, errorKind, w.Header(), map[string]string{"handler": "agent_pool"})
		}
		a.recordProxyRequestEventWithIDs(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.BackendID,
			resolution.RouteID,
			selectedAgentID,
			observability.requestBytesValue(),
			observability.responseBytesValue(),
		)
	}()

	agent = a.selectBackendAgent(resolution.Backend)
	if agent == nil {
		statusCode = http.StatusServiceUnavailable
		errorKind = "no_backend_agent"
		http.Error(w, "No assigned backend agent connected", http.StatusServiceUnavailable)
		return
	}
	selectedAgentID = sql.NullInt64{Int64: agent.AgentID, Valid: true}
	resolution.AgentID = selectedAgentID
	if trace != nil {
		trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_AGENT_SELECTED, &resolution, agent, 0, "", nil, map[string]string{
			"load_balancer": resolution.Backend.LoadBalancing,
		})
	}
	agent.ActiveRequests.Add(1)
	defer agent.ActiveRequests.Add(-1)

	id := uuid.Nil
	if trace != nil {
		id = trace.uuid()
	}
	if id == uuid.Nil {
		var err error
		id, err = uuid.NewV7()
		if err != nil {
			statusCode = http.StatusInternalServerError
			errorKind = "request_id_failed"
			http.Error(w, "Failed to generate ID", http.StatusInternalServerError)
			return
		}
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

	pendingCtx, pendingCancel := context.WithCancel(r.Context())
	defer pendingCancel()
	pending := &pendingAgentRequest{
		AgentID:       agent.AgentID,
		AgentPublicID: agent.PublicID,
		ResponseCh:    make(chan *msg.Request, 100),
		ErrorCh:       make(chan error, 1),
		ctx:           pendingCtx,
		cancel:        pendingCancel,
	}
	a.PendingRequests.Store(id, pending)
	defer a.PendingRequests.Delete(id)

	outReq := r.Clone(r.Context())
	applyUpstreamRequestConfig(outReq, resolution.Backend)
	applyTrustedForwardedHeaders(outReq, r)
	if shaper != nil {
		outReq.Body = shaper.wrapUploadBody(r.Context(), outReq.Body)
	}

	if trace != nil {
		trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_STARTED,
			&resolution,
			agent,
			0,
			"",
			nil,
			map[string]string{"handler": "agent_pool", "agent": agent.PublicID, "upstream": redactSensitiveTraceURL(targetOrigin.String())},
		)
	}
	enc := httpmsg.NewRequestEncoderWithMetadata(id, outReq, map[string]string{
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
		select {
		case agent.WriteCh <- m:
		case <-agent.Done:
			statusCode = http.StatusBadGateway
			errorKind = "agent_disconnected"
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		case <-r.Context().Done():
			statusCode = http.StatusGatewayTimeout
			errorKind = "client_cancelled"
			return
		}
	}

	timeoutCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var firstMsg *msg.Request
	select {
	case <-timeoutCtx.Done():
		statusCode = http.StatusGatewayTimeout
		errorKind = "agent_timeout"
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
		return
	case err := <-pending.ErrorCh:
		statusCode = http.StatusBadGateway
		if errors.Is(err, errAgentDisconnected) {
			errorKind = "agent_disconnected"
		} else {
			errorKind = "agent_failed"
		}
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	case firstMsg = <-pending.ResponseCh:
		if firstMsg == nil {
			statusCode = http.StatusBadGateway
			errorKind = "agent_disconnected"
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}
	}

	stream := &httpmsg.ChannelStream{Ctx: pendingCtx, Ch: pending.ResponseCh}
	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to decode response headers")
		statusCode = http.StatusBadGateway
		errorKind = "response_decode_failed"
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	statusCode = resp.StatusCode
	if trace != nil {
		trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RESPONDED,
			&resolution,
			agent,
			resp.StatusCode,
			"",
			resp.Header,
			map[string]string{"handler": "agent_pool", "agent": agent.PublicID},
		)
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if resp.Body != nil {
		if shaper != nil {
			resp.Body = shaper.wrapDownloadBody(r.Context(), resp.Body)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(w, resp.Body)
	}

	log.Info().Str("req_id", id.String()).Int("status", resp.StatusCode).Msg("Finished proxying request")
}

func (a *App) selectBackendAgent(backend publicBackendConfig) *AgentConn {
	candidates := make([]backendAgentCandidate, 0, len(backend.AgentAssignments))
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	for _, assignment := range backend.AgentAssignments {
		if !assignment.Enabled {
			continue
		}
		if snap != nil {
			agentConfig, ok := snap.Agents[assignment.AgentID]
			if !ok || !agentConfig.Enabled {
				continue
			}
		}
		conn := a.AgentHub.connectedByID(assignment.AgentID)
		if conn == nil {
			continue
		}
		candidates = append(candidates, backendAgentCandidate{
			Conn:     conn,
			AgentID:  assignment.AgentID,
			Position: assignment.Position,
			Weight:   assignment.Weight,
		})
	}
	return a.LoadBalancers.selectAgent(backend, candidates)
}

func applyUpstreamRequestConfig(req *http.Request, backend publicBackendConfig) {
	if backend.ParsedOrigin != nil {
		req.URL.Scheme = backend.ParsedOrigin.Scheme
		req.URL.Host = backend.ParsedOrigin.Host
		req.Host = backend.ParsedOrigin.Host
	}
	req.RequestURI = ""
	for _, header := range backend.UpstreamRequestHeaders {
		req.Header.Set(header.Name, header.Value)
	}
	if backend.UpstreamBasicAuth.Enabled {
		req.SetBasicAuth(backend.UpstreamBasicAuth.Username, backend.UpstreamBasicAuth.Password)
	}
}

func applyTrustedForwardedHeaders(outReq *http.Request, inReq *http.Request) {
	if outReq == nil {
		return
	}
	for _, name := range []string{
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Forwarded-Port",
		"X-Real-Ip",
	} {
		outReq.Header.Del(name)
	}
	if inReq == nil {
		return
	}
	clientIP := remoteAddrIP(inReq.RemoteAddr)
	if clientIP != "" {
		outReq.Header.Set("X-Forwarded-For", clientIP)
		outReq.Header.Set("X-Real-IP", clientIP)
	}
	if inReq.Host != "" {
		outReq.Header.Set("X-Forwarded-Host", inReq.Host)
	}
	proto := "http"
	if inReq.TLS != nil {
		proto = "https"
	}
	outReq.Header.Set("X-Forwarded-Proto", proto)
	if port := forwardedPort(inReq.Host, proto); port != "" {
		outReq.Header.Set("X-Forwarded-Port", port)
	}
}

func remoteAddrIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddr)
}

func forwardedPort(host string, proto string) string {
	if _, port, err := net.SplitHostPort(host); err == nil {
		return port
	}
	switch proto {
	case "https":
		return "443"
	default:
		return "80"
	}
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
	var matchedRoute publicRouteConfig
	action := publicRouteActionForward
	defaultRoute := true
	for _, route := range snap.RoutesByListener[listenerID] {
		if !route.Enabled {
			continue
		}
		if route.HostPattern != "" && !hostMatchesPattern(host, route.HostPattern) {
			continue
		}
		if route.PathPrefix != "" && !pathPrefixMatches(r.URL.Path, route.PathPrefix) {
			continue
		}
		routeID = sql.NullInt64{Int64: route.ID, Valid: true}
		matchedRoute = route
		action = normalizePublicRouteAction(route.Action)
		defaultRoute = false
		if action == publicRouteActionRedirect {
			return publicRouteResolution{
				Listener:     listener,
				Route:        matchedRoute,
				Action:       publicRouteActionRedirect,
				DefaultRoute: defaultRoute,
				ListenerID:   sql.NullInt64{Int64: listenerID, Valid: true},
				RouteID:      routeID,
			}, nil
		}
		backendID = route.BackendID
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
		Backend:      backend,
		Listener:     listener,
		Route:        matchedRoute,
		Action:       publicRouteActionForward,
		DefaultRoute: defaultRoute,
		ListenerID:   sql.NullInt64{Int64: listenerID, Valid: true},
		BackendID:    sql.NullInt64{Int64: backendID, Valid: true},
		RouteID:      routeID,
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

func redirectLocationForRequest(r *http.Request, route publicRouteConfig) (string, error) {
	mode := normalizePublicRouteRedirectTargetMode(route.RedirectTargetMode)
	switch mode {
	case publicRouteRedirectTargetModeSameHostPath, publicRouteRedirectTargetModeAbsoluteURL:
		target, err := url.Parse(route.RedirectTarget)
		if err != nil {
			return "", err
		}
		if route.RedirectPreservePathSuffix {
			target.Path = joinRedirectPath(target.Path, redirectPathSuffix(r, route.PathPrefix))
			target.RawPath = ""
		}
		target.RawQuery = mergeRedirectQuery(target.RawQuery, r.URL.RawQuery, route.RedirectPreserveQuery)
		return target.String(), nil
	case publicRouteRedirectTargetModeExternalOriginKeepPath:
		target, err := url.Parse(route.RedirectTarget)
		if err != nil {
			return "", err
		}
		target.Path = r.URL.Path
		target.RawPath = r.URL.RawPath
		if target.Path == "" {
			target.Path = "/"
		}
		target.RawQuery = ""
		target.RawQuery = mergeRedirectQuery(target.RawQuery, r.URL.RawQuery, route.RedirectPreserveQuery)
		return target.String(), nil
	default:
		return "", errors.New("unsupported redirect target mode")
	}
}

func redirectPathSuffix(r *http.Request, pathPrefix string) string {
	if r == nil || r.URL == nil {
		return ""
	}
	requestPath := r.URL.Path
	if requestPath == "" {
		requestPath = "/"
	}
	pathPrefix = strings.TrimSpace(pathPrefix)
	if pathPrefix == "" || pathPrefix == "/" {
		return requestPath
	}
	if !pathPrefixMatches(requestPath, pathPrefix) {
		return ""
	}
	suffix := strings.TrimPrefix(requestPath, pathPrefix)
	if suffix == "" {
		return ""
	}
	if !strings.HasPrefix(suffix, "/") {
		return "/" + suffix
	}
	return suffix
}

func joinRedirectPath(basePath string, suffix string) string {
	if suffix == "" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	if basePath == "" {
		basePath = "/"
	}
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	if basePath == "/" {
		return suffix
	}
	return strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(suffix, "/")
}

func pathPrefixMatches(requestPath string, pathPrefix string) bool {
	pathPrefix = strings.TrimSpace(pathPrefix)
	if pathPrefix == "" || pathPrefix == "/" {
		return true
	}
	if requestPath == "" {
		requestPath = "/"
	}
	if !strings.HasPrefix(requestPath, pathPrefix) {
		return false
	}
	if len(requestPath) == len(pathPrefix) {
		return true
	}
	if strings.HasSuffix(pathPrefix, "/") {
		return true
	}
	return requestPath[len(pathPrefix)] == '/'
}

func mergeRedirectQuery(configuredQuery string, incomingQuery string, preserveIncoming bool) string {
	if !preserveIncoming || incomingQuery == "" {
		return configuredQuery
	}
	if configuredQuery == "" {
		return incomingQuery
	}
	return configuredQuery + "&" + incomingQuery
}

func directProxyTransport(tlsSkipVerify bool) http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = publicUpstreamResponseHeaderTimeout
	if tlsSkipVerify {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	return transport
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
