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
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/tunnel"
)

var errNoRouteBackendAvailable = errors.New("no route backend available")
var errNoRouteTargetAvailable = errors.New("no route target available")
var errNoPublicRouteAvailable = errors.New("no public route available")

type publicRouteTargetHealthConfig struct {
	ID                            int64
	Name                          string
	TargetOrigin                  string
	TargetType                    string
	Transport                     string
	LoadBalancing                 string
	TLSSkipVerify                 bool
	StaticStatusCode              int
	StaticResponseHeaders         []publicResponseHeader
	StaticResponseBody            string
	StaticResponseBodyMode        string
	StaticResponseTemplateID      int64
	UpstreamRequestHeaders        []publicRequestHeader
	UpstreamBasicAuth             publicRouteTargetBasicAuthConfig
	UpstreamResponseHeaderTimeout time.Duration
	Enabled                       bool
	ParsedOrigin                  *url.URL
	AgentAssignments              []publicRouteTargetAgentAssignment
	HealthCheck                   publicRouteTargetHealthCheckConfig
}

type publicAgentConfig struct {
	ID       int64
	PublicID string
	Name     string
	Enabled  bool
	Labels   map[string]string
}

type publicRouteTargetAgentAssignment struct {
	TargetID int64
	AgentID  int64
	Position int64
	Weight   int64
	Enabled  bool
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

type publicRouteTargetBasicAuthConfig struct {
	Enabled  bool
	Username string
	Password string
}

type publicRouteTargetHealthCheckConfig struct {
	Enabled            bool
	Method             string
	Path               string
	Interval           time.Duration
	Timeout            time.Duration
	HealthyThreshold   int64
	UnhealthyThreshold int64
	ExpectedStatusMin  int64
	ExpectedStatusMax  int64
}

type publicAgentSelectorConfig struct {
	MatchLabels map[string]string
}

type publicRouteTargetConfig struct {
	ID                            int64
	RouteID                       int64
	Name                          string
	Position                      int64
	PriorityGroup                 int64
	Weight                        int64
	Enabled                       bool
	TargetType                    string
	URL                           string
	Transport                     string
	AgentSelector                 publicAgentSelectorConfig
	AgentLoadBalancing            string
	TLSSkipVerify                 bool
	UpstreamResponseHeaderTimeout time.Duration
	UpstreamRequestHeaders        []publicRequestHeader
	UpstreamBasicAuth             publicRouteTargetBasicAuthConfig
	HealthCheck                   publicRouteTargetHealthCheckConfig
	StaticStatusCode              int
	StaticResponseHeaders         []publicResponseHeader
	StaticResponseBody            string
	StaticResponseBodyMode        string
	StaticResponseTemplateID      int64
	ParsedURL                     *url.URL
}

type publicListenerConfig struct {
	ID          int64
	Name        string
	BindAddress string
	Port        int64
	Protocol    string
	Enabled     bool
}

type publicRouteConfig struct {
	ID                         int64
	ListenerID                 int64
	Priority                   int64
	HostPattern                string
	PathPrefix                 string
	TargetLoadBalancing        string
	IsDefault                  bool
	Targets                    []publicRouteTargetConfig
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
	RouteTargets        map[int64]publicRouteTargetConfig
	Agents              map[int64]publicAgentConfig
	Listeners           map[int64]publicListenerConfig
	RoutesByListener    map[int64][]publicRouteConfig
	CertsByListener     map[int64][]publicTLSCertificateConfig
	RateLimitRules      []publicRateLimitRuleConfig
	TrafficShaperRules  []publicTrafficShaperRuleConfig
	WafCaptchaProviders map[int64]publicWafCaptchaProviderConfig
	WafRules            []publicWafRuleConfig
	WafCookieSecret     []byte
	CacheSettings       publicCacheSettingsConfig
	CacheRules          []publicCacheRuleConfig
	ResponseTemplates   map[int64]publicResponseTemplateConfig
}

type publicRouteResolution struct {
	Target                              publicRouteTargetConfig
	Agent                               *AgentConn
	Listener                            publicListenerConfig
	Route                               publicRouteConfig
	Action                              string
	DefaultRoute                        bool
	ListenerID                          sql.NullInt64
	RouteTargetID                       sql.NullInt64
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
	WafRuleID                           int64
	WafRuleName                         string
	WafAction                           string
	WafActivationMode                   string
	WafAutomaticActive                  bool
	WafChallengeKind                    string
	CacheRuleID                         int64
	CacheRuleName                       string
	CacheStatus                         string
	CacheKeyDigest                      string
	RouteLoadBalancing                  string
	RouteFallbackSelected               bool
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
		newPublicProxyContext(a, listenerID, w, r).run()
	}
}

func trafficShaperDecisionIfSelected(decision publicTrafficShaperDecision, selected bool) *publicTrafficShaperDecision {
	if !selected {
		return nil
	}
	return &decision
}

func writeNoRouteTargetAvailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Service unavailable</title></head>
<body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#080a0d;color:#f4f7fa;font-family:Trebuchet MS,Verdana,sans-serif;padding:32px">
<main style="max-width:680px;border:1px solid #29313a;background:#11161c;padding:40px">
<p style="margin:0 0 12px;color:#34d399;text-transform:uppercase;font-size:12px;letter-spacing:0">p2pstream route unavailable</p>
<h1 style="margin:0 0 16px;font-size:42px;line-height:1">No target is available</h1>
<p style="margin:0;color:#9aa8b5;line-height:1.6">The matched route has no enabled, healthy, or connected target available.</p>
</main>
</body>
</html>`)
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
			resolution.RouteID,
			sql.NullInt64{},
			observability.requestBytesValue(),
			observability.responseBytesValue(),
		)
	}()
}

func (a *App) staticTargetResponse(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, observability proxyRequestObservability) {
	startedAt := time.Now()
	statusCode := resolution.Target.StaticStatusCode
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
		a.recordProxyRequestEventWithRouteTargetCache(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.RouteID,
			resolution.RouteTargetID,
			sql.NullInt64{},
			"",
			sql.NullInt64{},
			sql.NullInt64{},
			"",
			0,
			observability.requestBytesValue(),
			observability.responseBytesValue(),
		)
	}()

	for _, header := range resolution.Target.StaticResponseHeaders {
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
	body := io.NopCloser(strings.NewReader(resolution.Target.StaticResponseBody))
	if shaper != nil {
		body = shaper.wrapDownloadBody(r.Context(), body)
	}
	defer body.Close()
	_, _ = io.Copy(w, body)
}

func (a *App) proxyDirectTargetRequest(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, cacheDecision *publicCacheDecision, observability proxyRequestObservability) {
	a.proxyRouteTargetRequest(w, r, resolution, nil, trace, shaper, cacheDecision, observability)
}

func (a *App) proxyAgentTargetRequest(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, cacheDecision *publicCacheDecision, observability proxyRequestObservability) {
	a.proxyRouteTargetRequest(w, r, resolution, resolution.Agent, trace, shaper, cacheDecision, observability)
}

func (a *App) proxyRouteTargetRequest(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, agent *AgentConn, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, cacheDecision *publicCacheDecision, observability proxyRequestObservability) {
	startedAt := time.Now()
	statusCode := http.StatusOK
	errorKind := ""
	handler := "direct"
	var selectedAgentID sql.NullInt64
	if resolution.Target.Transport == publicRouteTargetTransportAgent {
		handler = "agent_target"
	}
	defer func() {
		if trace != nil {
			stage := p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT
			if errorKind != "" {
				stage = p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED
			}
			trace.emit(stage, &resolution, agent, statusCode, errorKind, w.Header(), map[string]string{"handler": handler})
		}
		a.recordProxyRequestEventWithRouteTargetCache(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.RouteID,
			resolution.RouteTargetID,
			sql.NullInt64{},
			"",
			selectedAgentID,
			cacheRuleID(cacheDecision),
			cacheStatus(cacheDecision),
			cacheBytes(cacheDecision),
			observability.requestBytesValue(),
			observability.responseBytesValue(),
		)
	}()

	targetOrigin := resolution.Target.ParsedURL
	if targetOrigin == nil {
		statusCode = http.StatusBadGateway
		errorKind = "target_unavailable"
		http.Error(w, "Selected target is unavailable", http.StatusBadGateway)
		return
	}
	if resolution.Target.Transport == publicRouteTargetTransportAgent {
		if agent == nil {
			statusCode = http.StatusServiceUnavailable
			errorKind = "no_target_agent"
			http.Error(w, "No matching route target agent connected", http.StatusServiceUnavailable)
			return
		}
		selectedAgentID = sql.NullInt64{Int64: agent.AgentID, Valid: true}
		resolution.AgentID = selectedAgentID
		if trace != nil {
			trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_AGENT_SELECTED, &resolution, agent, 0, "", nil, map[string]string{
				"load_balancer": resolution.Target.AgentLoadBalancing,
			})
		}
		agent.ActiveRequests.Add(1)
		defer agent.ActiveRequests.Add(-1)
	}

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

	if agent != nil {
		log.Info().
			Str("req_id", id.String()).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("agent", agent.PublicID).
			Msg("Proxying request through agent target")
	}
	if trace != nil {
		attributes := map[string]string{"handler": handler, "upstream": redactSensitiveTraceURL(targetOrigin.String())}
		if agent != nil {
			attributes["agent"] = agent.PublicID
		}
		trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_STARTED,
			&resolution,
			agent,
			0,
			"",
			nil,
			attributes,
		)
	}

	transport := directProxyTransport(resolution.Target.TLSSkipVerify, resolution.Target.UpstreamResponseHeaderTimeout)
	if agent != nil {
		transport = a.agentTargetTransport(agent, resolution.Target)
		r = r.WithContext(withAgentDialRequestID(r.Context(), id.String()))
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(proxyReq *httputil.ProxyRequest) {
			applyUpstreamTargetRequestConfig(proxyReq.Out, resolution.Target)
			applyTrustedForwardedHeaders(proxyReq.Out, proxyReq.In)
			if shaper != nil {
				proxyReq.Out.Body = shaper.wrapUploadBody(r.Context(), proxyReq.Out.Body)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			statusCode = resp.StatusCode
			if cacheDecision != nil && cacheDecision.Rule.AddCacheStatusHeader {
				resp.Header.Set("X-p2pstream-Cache", "MISS")
			}
			if cacheDecision != nil && cacheDecision.Cacheable {
				resp.Body = a.capturePublicCacheResponseBody(r.Context(), r, resolution, cacheDecision, resp, trace)
			}
			if shaper != nil {
				resp.Body = shaper.wrapDownloadBody(r.Context(), resp.Body)
			}
			if trace != nil {
				attributes := map[string]string{"handler": handler}
				if agent != nil {
					attributes["agent"] = agent.PublicID
				}
				trace.emit(
					p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RESPONDED,
					&resolution,
					agent,
					resp.StatusCode,
					"",
					resp.Header,
					attributes,
				)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if agent != nil && requestContextCanceled(r.Context(), err) {
				log.Debug().Err(err).Str("req_id", id.String()).Msg("Agent target proxy cancelled by client")
				statusCode = http.StatusGatewayTimeout
				errorKind = "client_cancelled"
				return
			}
			if agent == nil {
				log.Error().Err(err).Str("target", resolution.Target.Name).Msg("Direct target proxy failed")
				if r.Context().Err() == nil && !errors.Is(err, context.Canceled) {
					a.markPublicRouteTargetPassiveFailure(resolution.Target.ID, err)
				}
				if isTimeoutError(err) {
					statusCode = http.StatusGatewayTimeout
					errorKind = "upstream_response_header_timeout"
					http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
					return
				}
				statusCode = http.StatusBadGateway
				errorKind = "direct_proxy_failed"
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
				return
			}
			log.Error().Err(err).Str("req_id", id.String()).Str("agent", agent.PublicID).Msg("Agent target proxy failed")
			if selectedAgentID.Valid && shouldMarkAgentPassiveFailure(r.Context(), err) {
				a.markPublicRouteTargetAgentPassiveFailure(resolution.Target.ID, selectedAgentID.Int64, err)
			}
			var dialErr agentDialError
			switch {
			case errors.Is(err, errAgentDisconnected):
				statusCode = http.StatusBadGateway
				errorKind = "agent_disconnected"
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
			case errors.As(err, &dialErr):
				log.Debug().
					Err(err).
					Str("req_id", id.String()).
					Str("agent", agent.PublicID).
					Str("kind", dialErr.Kind).
					Msg("Agent target dial failed")
				if dialErr.Kind == "dial_timeout" {
					statusCode = http.StatusGatewayTimeout
					errorKind = "agent_dial_timeout"
					http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
					return
				}
				statusCode = http.StatusBadGateway
				if dialErr.Kind == "" {
					errorKind = "agent_dial_failed"
				} else {
					errorKind = "agent_" + dialErr.Kind
				}
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
			case isTimeoutError(err):
				statusCode = http.StatusGatewayTimeout
				errorKind = "upstream_response_header_timeout"
				http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
			default:
				statusCode = http.StatusBadGateway
				errorKind = "agent_proxy_failed"
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
			}
		},
		Transport: transport,
	}
	proxy.ServeHTTP(w, r)
}

type agentDialError struct {
	Kind string
	Err  string
}

func (e agentDialError) Error() string {
	if e.Err == "" {
		return e.Kind
	}
	return e.Kind + ": " + e.Err
}

func (a *App) agentTargetTransport(agent *AgentConn, target publicRouteTargetConfig) http.RoundTripper {
	if a.AgentTransports == nil {
		return newAgentTransportPool().publicRouteTargetTransport(a, agent, target)
	}
	return a.AgentTransports.publicRouteTargetTransport(a, agent, target)
}

func (a *App) dialViaAgent(ctx context.Context, agent *AgentConn, network string, address string, requestID string) (net.Conn, error) {
	if agent == nil || agent.Session == nil || agent.Session.IsClosed() {
		return nil, errAgentDisconnected
	}
	openCh := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	openDone := make(chan struct{})
	stopOpenWatch := func() {
		select {
		case <-openDone:
		default:
			close(openDone)
		}
	}
	session := agent.Session
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-agent.Done:
			_ = session.Close()
		case <-openDone:
		}
	}()
	go func() {
		conn, err := session.Open()
		result := struct {
			conn net.Conn
			err  error
		}{conn: conn, err: err}
		select {
		case openCh <- result:
		case <-ctx.Done():
			if conn != nil {
				_ = conn.Close()
			}
		case <-agent.Done:
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()

	var conn net.Conn
	select {
	case result := <-openCh:
		stopOpenWatch()
		if result.err != nil {
			if agent != nil {
				log.Debug().
					Err(result.err).
					Str("request_id", requestID).
					Str("agent", agent.PublicID).
					Str("address", redactAgentDialAddress(address)).
					Msg("Failed to open agent tunnel stream")
			}
			return nil, result.err
		}
		conn = result.conn
	case <-ctx.Done():
		_ = agent.Session.Close()
		stopOpenWatch()
		if agent != nil {
			log.Debug().
				Err(ctx.Err()).
				Str("request_id", requestID).
				Str("agent", agent.PublicID).
				Str("address", redactAgentDialAddress(address)).
				Msg("Agent tunnel stream open cancelled")
		}
		return nil, ctx.Err()
	case <-agent.Done:
		_ = agent.Session.Close()
		stopOpenWatch()
		log.Debug().
			Str("request_id", requestID).
			Str("agent", agent.PublicID).
			Str("address", redactAgentDialAddress(address)).
			Msg("Agent disconnected before tunnel stream opened")
		return nil, errAgentDisconnected
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	handshakeDone := make(chan struct{})
	stopHandshakeWatch := func() {
		select {
		case <-handshakeDone:
		default:
			close(handshakeDone)
		}
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-agent.Done:
			_ = conn.Close()
		case <-handshakeDone:
		}
	}()
	defer stopHandshakeWatch()
	req := tunnel.NewOpenRequest(requestID, network, address)
	if err := tunnel.WriteOpenRequest(conn, req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp, err := tunnel.ReadOpenResponse(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !resp.OK {
		_ = conn.Close()
		log.Debug().
			Str("request_id", requestID).
			Str("agent", agent.PublicID).
			Str("kind", resp.ErrorKind).
			Str("address", redactAgentDialAddress(address)).
			Msg("Agent dial failed")
		return nil, agentDialError{Kind: resp.ErrorKind, Err: resp.Error}
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func redactAgentDialAddress(address string) string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "<invalid>"
	}
	if port == "" {
		return "<host>"
	}
	return net.JoinHostPort("<host>", port)
}

func (a *App) selectTargetAgent(target publicRouteTargetConfig) *AgentConn {
	candidates := a.eligibleTargetAgentCandidates(target)
	if len(candidates) == 0 {
		return nil
	}
	if a.LoadBalancers == nil {
		return candidates[0].Conn
	}
	return a.LoadBalancers.selectTargetAgent(target, candidates)
}

func (a *App) eligibleTargetAgentCandidates(target publicRouteTargetConfig) []backendAgentCandidate {
	if a == nil || a.AgentHub == nil {
		return nil
	}
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	if snap == nil {
		return nil
	}
	candidates := make([]backendAgentCandidate, 0, len(snap.Agents))
	for agentID, agentConfig := range snap.Agents {
		if !agentConfig.Enabled || !agentSelectorMatchesLabels(target.AgentSelector, agentConfig.Labels) {
			continue
		}
		conn := a.AgentHub.connectedByID(agentID)
		if conn == nil {
			continue
		}
		if a.TargetHealth != nil && !a.TargetHealth.agentAvailable(target.ID, agentID) {
			continue
		}
		candidates = append(candidates, backendAgentCandidate{
			Conn:     conn,
			AgentID:  agentID,
			Position: agentID,
			Weight:   100,
		})
	}
	return candidates
}

func agentSelectorMatchesLabels(selector publicAgentSelectorConfig, labels map[string]string) bool {
	if len(selector.MatchLabels) == 0 {
		return false
	}
	for key, want := range selector.MatchLabels {
		if labels == nil {
			return false
		}
		if got, ok := labels[key]; !ok || got != want {
			return false
		}
	}
	return true
}

func requestContextCanceled(ctx context.Context, err error) bool {
	if ctx == nil {
		return false
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return true
	}
	return errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled)
}

func shouldMarkAgentPassiveFailure(requestCtx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return !requestContextCanceled(requestCtx, err)
}

func (a *App) selectRouteTarget(snap publicProxySnapshot, route publicRouteConfig) (publicRouteTargetConfig, *AgentConn, bool) {
	candidates := make([]routeTargetCandidate, 0, len(route.Targets))
	lowestPriorityGroupSet := false
	lowestPriorityGroup := int64(0)
	for _, target := range route.Targets {
		if !a.targetEligibleForRoute(snap, target) {
			continue
		}
		if !lowestPriorityGroupSet || target.PriorityGroup < lowestPriorityGroup {
			lowestPriorityGroup = target.PriorityGroup
			lowestPriorityGroupSet = true
		}
	}
	if !lowestPriorityGroupSet {
		return publicRouteTargetConfig{}, nil, false
	}
	for _, target := range route.Targets {
		if target.PriorityGroup != lowestPriorityGroup || !a.targetEligibleForRoute(snap, target) {
			continue
		}
		candidates = append(candidates, routeTargetCandidate{
			Target:         target,
			TargetID:       target.ID,
			Position:       target.Position,
			Weight:         target.Weight,
			ActiveRequests: 0,
		})
	}
	if len(candidates) == 0 {
		return publicRouteTargetConfig{}, nil, false
	}
	selected := candidates[0]
	if a.LoadBalancers != nil {
		var ok bool
		selected, ok = a.LoadBalancers.selectRouteTarget(route, candidates)
		if !ok {
			return publicRouteTargetConfig{}, nil, false
		}
	}
	if selected.Target.Transport != publicRouteTargetTransportAgent {
		return selected.Target, nil, true
	}
	agent := a.selectTargetAgent(selected.Target)
	if agent == nil {
		return publicRouteTargetConfig{}, nil, false
	}
	return selected.Target, agent, true
}

func (a *App) targetEligibleForRoute(snap publicProxySnapshot, target publicRouteTargetConfig) bool {
	if !target.Enabled {
		return false
	}
	if target.TargetType == publicRouteTargetTypeStatic {
		return true
	}
	if target.TargetType != publicRouteTargetTypeProxy || target.ParsedURL == nil {
		return false
	}
	if a.TargetHealth != nil && !a.TargetHealth.available(publicRouteTargetHealthConfigFromRouteTarget(target, snap.Agents)) {
		return false
	}
	if target.Transport == publicRouteTargetTransportAgent {
		return a.targetHasEligibleAgent(snap, target)
	}
	return true
}

func (a *App) targetHasEligibleAgent(snap publicProxySnapshot, target publicRouteTargetConfig) bool {
	if a == nil || a.AgentHub == nil {
		return false
	}
	for agentID, agentConfig := range snap.Agents {
		if !agentConfig.Enabled || !agentSelectorMatchesLabels(target.AgentSelector, agentConfig.Labels) {
			continue
		}
		if a.AgentHub.connectedByID(agentID) != nil {
			if a.TargetHealth == nil || a.TargetHealth.agentAvailable(target.ID, agentID) {
				return true
			}
		}
	}
	return false
}

func (a *App) beginPublicRouteTargetRequest(targetID int64) func() {
	if a.TargetHealth == nil {
		return func() {}
	}
	return a.TargetHealth.beginRequest(targetID)
}

func (a *App) markPublicRouteTargetPassiveFailure(targetID int64, err error) {
	if a.TargetHealth == nil {
		return
	}
	a.TargetHealth.markPassiveFailure(targetID, err)
}

func (a *App) markPublicRouteTargetAgentPassiveFailure(targetID int64, agentID int64, err error) {
	if a.TargetHealth == nil {
		return
	}
	a.TargetHealth.markAgentPassiveFailure(targetID, agentID, err)
}

func applyUpstreamRequestConfig(req *http.Request, backend publicRouteTargetHealthConfig) {
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

func applyUpstreamTargetRequestConfig(req *http.Request, target publicRouteTargetConfig) {
	if target.ParsedURL != nil {
		req.URL.Scheme = target.ParsedURL.Scheme
		req.URL.Host = target.ParsedURL.Host
		req.Host = target.ParsedURL.Host
	}
	req.RequestURI = ""
	for _, header := range target.UpstreamRequestHeaders {
		req.Header.Set(header.Name, header.Value)
	}
	if target.UpstreamBasicAuth.Enabled {
		req.SetBasicAuth(target.UpstreamBasicAuth.Username, target.UpstreamBasicAuth.Password)
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

	var matchedRoute publicRouteConfig
	var defaultRoute publicRouteConfig
	for _, route := range snap.RoutesByListener[listenerID] {
		if !route.Enabled {
			continue
		}
		if route.IsDefault {
			if defaultRoute.ID == 0 {
				defaultRoute = route
			}
			continue
		}
		if route.HostPattern != "" && !hostMatchesPattern(host, route.HostPattern) {
			continue
		}
		if route.PathPrefix != "" && !pathPrefixMatches(r.URL.Path, route.PathPrefix) {
			continue
		}
		matchedRoute = route
		break
	}
	isDefaultRoute := false
	if matchedRoute.ID == 0 {
		if defaultRoute.ID == 0 {
			return publicRouteResolution{}, errNoPublicRouteAvailable
		}
		matchedRoute = defaultRoute
		isDefaultRoute = true
	}

	routeID := sql.NullInt64{Int64: matchedRoute.ID, Valid: true}
	action := normalizePublicRouteAction(matchedRoute.Action)
	if action == publicRouteActionRedirect {
		return publicRouteResolution{
			Listener:     listener,
			Route:        matchedRoute,
			Action:       publicRouteActionRedirect,
			DefaultRoute: isDefaultRoute,
			ListenerID:   sql.NullInt64{Int64: listenerID, Valid: true},
			RouteID:      routeID,
		}, nil
	}
	target, agent, ok := a.selectRouteTarget(*snap, matchedRoute)
	if !ok {
		return publicRouteResolution{}, errNoRouteTargetAvailable
	}

	resolution := publicRouteResolution{
		Target:             target,
		Agent:              agent,
		Listener:           listener,
		Route:              matchedRoute,
		Action:             publicRouteActionForward,
		DefaultRoute:       isDefaultRoute,
		ListenerID:         sql.NullInt64{Int64: listenerID, Valid: true},
		RouteID:            routeID,
		RouteTargetID:      sql.NullInt64{Int64: target.ID, Valid: target.ID != 0},
		RouteLoadBalancing: matchedRoute.TargetLoadBalancing,
	}
	if agent != nil {
		resolution.AgentID = sql.NullInt64{Int64: agent.AgentID, Valid: true}
	}
	return resolution, nil
}

func normalizeRequestHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if strings.HasPrefix(host, "[") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			return normalizeBareRequestHost(h)
		}
		return normalizeBareRequestHost(strings.Trim(host, "[]"))
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return normalizeBareRequestHost(h)
	}
	if idx := strings.LastIndex(host, ":"); idx > -1 && strings.Count(host, ":") == 1 {
		return normalizeBareRequestHost(host[:idx])
	}
	return normalizeBareRequestHost(host)
}

func normalizeBareRequestHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(strings.Trim(host, "[]")))
	if strings.Contains(host, ":") {
		return host
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
		return normalizeLocalRedirectPath(requestPath)
	}
	if !pathPrefixMatches(requestPath, pathPrefix) {
		return ""
	}
	suffix := strings.TrimPrefix(requestPath, pathPrefix)
	if suffix == "" {
		return ""
	}
	return normalizeLocalRedirectPath(suffix)
}

func joinRedirectPath(basePath string, suffix string) string {
	basePath = normalizeLocalRedirectPath(basePath)
	if suffix == "" {
		return basePath
	}
	suffix = normalizeLocalRedirectPath(suffix)
	if basePath == "/" {
		return suffix
	}
	return strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(suffix, "/")
}

func isSafeLocalRedirectTarget(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	if len(path) > 1 && (path[1] == '/' || path[1] == '\\') {
		return false
	}
	return true
}

func normalizeLocalRedirectPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if isSafeLocalRedirectTarget(path) {
		return path
	}
	path = strings.TrimLeft(path, `/\`)
	if path == "" || path[0] == '/' || path[0] == '\\' {
		return "/"
	}
	normalized := "/" + path
	if !isSafeLocalRedirectTarget(normalized) {
		return "/"
	}
	return normalized
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

func directProxyTransport(tlsSkipVerify bool, responseHeaderTimeout time.Duration) http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = normalizeUpstreamResponseHeaderTimeout(responseHeaderTimeout)
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

func normalizeUpstreamResponseHeaderTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return time.Duration(defaultTargetUpstreamResponseHeaderTimeoutMillis) * time.Millisecond
	}
	return timeout
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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
