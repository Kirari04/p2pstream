package server

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

type publicProxyStageResult int

const (
	publicProxyStageContinue publicProxyStageResult = iota
	publicProxyStageDone
)

const publicInvalidRequestTargetErrorKind = "invalid_request_target"

const publicProxyContextPoolCapacity = 256

type publicProxyStage func(*publicProxyContext) publicProxyStageResult

type publicProxyContext struct {
	App        *App
	ListenerID int64
	StartedAt  time.Time
	Snapshot   *publicProxySnapshot

	Writer         http.ResponseWriter
	ResponseWriter http.ResponseWriter
	Recorder       *proxyResponseRecorder
	Request        *http.Request
	Observability  proxyRequestObservability
	RequestContext proxyRequestContext
	Trace          *trafficRequestTrace

	Resolution                   publicRouteResolution
	RouteMatch                   publicRouteMatch
	HasRouteMatch                bool
	EncodedPathSeparatorsAllowed bool
	PathIssues                   publicPathIssueSet
	TrafficShaperSelected        bool
	TrafficShaperDecision        publicTrafficShaperDecision
	CacheDecision                *publicCacheDecision
	RouteResolutionFailure       error
	cleanup                      []func()
}

type boundedPublicProxyContextPool struct {
	contexts chan *publicProxyContext
}

var reusablePublicProxyContexts = newBoundedPublicProxyContextPool(publicProxyContextPoolCapacity)

func newBoundedPublicProxyContextPool(capacity int) *boundedPublicProxyContextPool {
	if capacity < 0 {
		capacity = 0
	}
	return &boundedPublicProxyContextPool{contexts: make(chan *publicProxyContext, capacity)}
}

func (p *boundedPublicProxyContextPool) acquire() *publicProxyContext {
	if p != nil {
		select {
		case ctx := <-p.contexts:
			*ctx = publicProxyContext{}
			return ctx
		default:
		}
	}
	return &publicProxyContext{}
}

func (p *boundedPublicProxyContextPool) release(ctx *publicProxyContext) {
	if ctx == nil {
		return
	}
	// Drop every request, snapshot, decision, error, and cleanup reference
	// before retaining the bounded container for another public request.
	*ctx = publicProxyContext{}
	if p == nil {
		return
	}
	select {
	case p.contexts <- ctx:
	default:
	}
}

var publicProxyStages = []publicProxyStage{
	publicRequestAdmissionStage,
	rejectGloballyInvalidPublicPathStage,
	serveACMEChallengeStage,
	validatePublicSiteAuthorityStage,
	serveWAFReservedStage,
	routePathSecurityStage,
	beginWAFPressureStage,
	wafPolicyStage,
	rateLimitStage,
	publicAccessControlStage,
	trafficShaperStage,
	routeResolutionStage,
	redirectStage,
	routeSelectionTraceStage,
	publicResponseAdmissionStage,
	cacheLookupStage,
	activeTargetAccountingAndForwardStage,
}

type publicSiteAuthorityContextKey struct{}

type publicSiteAuthorityContext struct {
	Hostname     string
	RawAuthority string
	Parsed       bool
}

func validatePublicSiteAuthorityStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx == nil || ctx.Request == nil || ctx.Snapshot == nil || (len(ctx.Snapshot.SiteHostsByListener[ctx.ListenerID]) == 0 && len(ctx.Snapshot.SiteBindingsByListener[ctx.ListenerID]) == 0 && ctx.Snapshot.DefaultSiteByListener[ctx.ListenerID].SiteID == 0) {
		return publicProxyStageContinue
	}
	hostname, err := parsePublicSiteRequestAuthority(ctx.Request.Host)
	if err != nil {
		http.Error(ctx.ResponseWriter, "bad request", http.StatusBadRequest)
		ctx.App.recordProxyRequestEventWithIDsAndContext(context.Background(), http.StatusBadRequest, time.Since(ctx.StartedAt), "invalid_authority", sql.NullInt64{Int64: ctx.ListenerID, Valid: true}, sql.NullInt64{}, sql.NullInt64{}, ctx.Observability.requestBytesValue(), ctx.Observability.responseBytesValue(), ctx.RequestContext)
		if ctx.Trace != nil {
			resolution := publicRouteResolution{ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true}}
			ctx.Trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED, &resolution, nil, http.StatusBadRequest, "invalid_authority", ctx.ResponseWriter.Header(), nil)
		}
		return publicProxyStageDone
	}
	rawAuthority := ctx.Request.Host
	ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), publicSiteAuthorityContextKey{}, publicSiteAuthorityContext{Hostname: hostname, RawAuthority: rawAuthority, Parsed: true}))
	// Only a managed binding claims and canonicalizes the authority. Do this
	// before any managed-host rejection so event and trace data use the same
	// canonical host as routing, policy, cache, and authentication.
	_, _, hostManaged := matchPublicSiteForListener(ctx.Snapshot, ctx.ListenerID, hostname)
	if hostManaged {
		ctx.Request.Host = hostname
	}
	ctx.Recorder.request = ctx.Request
	ctx.RequestContext = proxyRequestContextFromHTTP(ctx.Request)
	if ctx.Trace != nil && hostManaged {
		ctx.Trace.host = hostname
	}
	listener := ctx.Snapshot.Listeners[ctx.ListenerID]
	if listener.Protocol == publicListenerProtocolHTTPS && ctx.Request.TLS != nil {
		serverName := ctx.Request.TLS.ServerName
		sni, sniValid := "", true
		if serverName != "" {
			var sniErr error
			sni, sniErr = normalizePublicSiteRequestHostname(serverName)
			sniValid = sniErr == nil
		}
		_, _, sniManaged := matchPublicSiteForListener(ctx.Snapshot, ctx.ListenerID, sni)
		if (hostManaged || sniManaged) && (!sniValid || (serverName == "" && net.ParseIP(hostname) == nil) || (serverName != "" && sni != hostname)) {
			http.Error(ctx.ResponseWriter, "misdirected request", http.StatusMisdirectedRequest)
			ctx.App.recordProxyRequestEventWithIDsAndContext(context.Background(), http.StatusMisdirectedRequest, time.Since(ctx.StartedAt), "site_sni_host_mismatch", sql.NullInt64{Int64: ctx.ListenerID, Valid: true}, sql.NullInt64{}, sql.NullInt64{}, ctx.Observability.requestBytesValue(), ctx.Observability.responseBytesValue(), ctx.RequestContext)
			if ctx.Trace != nil {
				resolution := publicRouteResolution{ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true}}
				ctx.Trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED, &resolution, nil, http.StatusMisdirectedRequest, "site_sni_host_mismatch", ctx.ResponseWriter.Header(), nil)
			}
			return publicProxyStageDone
		}
	}
	return publicProxyStageContinue
}

func newPublicProxyContext(app *App, listenerID int64, w http.ResponseWriter, r *http.Request) *publicProxyContext {
	ctx := &publicProxyContext{}
	initializePublicProxyContext(ctx, app, listenerID, w, r)
	return ctx
}

func initializePublicProxyContext(ctx *publicProxyContext, app *App, listenerID int64, w http.ResponseWriter, r *http.Request) {
	if ctx == nil {
		return
	}
	*ctx = publicProxyContext{}
	var snap *publicProxySnapshot
	if app != nil {
		snap = app.currentPublicSnapshot()
	}
	r = resolvePublicRequestIdentity(snap, r)
	if app != nil && app.Config != nil {
		r = applyPublicRequestBodyLimits(app.Config.PublicMaxRequestBodyBytes, app.Config.PublicRequestBodyIdleMillis, w, r)
	} else {
		r = applyPublicRequestBodyLimits(0, 0, w, r)
	}
	var requestBytes atomic.Uint64
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &countingReadCloser{ReadCloser: r.Body, bytes: &requestBytes}
	}
	bodyState, _ := r.Context().Value(publicRequestBodyStateContextKey{}).(*publicRequestBodyState)
	recorder := &proxyResponseRecorder{ResponseWriter: w, request: r, requestBodyState: bodyState}
	responseWriter := http.ResponseWriter(recorder)
	observability := proxyRequestObservability{requestBytes: &requestBytes, responseRecorder: recorder}
	trace := newPublicProxyTrafficTrace(app, r, recorder, func() string {
		return publicTracePathForRequest(app, snap, listenerID, r)
	})
	if trace != nil {
		trace.emitReceived(listenerID)
	}
	*ctx = publicProxyContext{
		App:            app,
		ListenerID:     listenerID,
		StartedAt:      time.Now(),
		Snapshot:       snap,
		Writer:         w,
		ResponseWriter: responseWriter,
		Recorder:       recorder,
		Request:        r,
		Observability:  observability,
		RequestContext: proxyRequestContextFromHTTP(r),
		Trace:          trace,
	}
}

func newPublicProxyTrafficTrace(app *App, r *http.Request, recorder *proxyResponseRecorder, safePath func() string) *trafficRequestTrace {
	if app == nil || app.TrafficTracer == nil {
		return nil
	}
	if _, enabled := app.TrafficTracer.enabledLevel(); !enabled {
		return nil
	}
	path := "/"
	if safePath != nil {
		path = safePath()
	}
	// Keep the tracer's own enabled check: settings can change after the fast
	// path above and before trace construction completes.
	return app.newTrafficRequestTrace(r, recorder, path)
}

func publicRequestAdmissionStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.App != nil && ctx.App.agentStreamCapacity != nil {
		// The physical connection holds a parser allowance, not a hard bound on
		// pre-handler allocations: many short HTTP/1 header keys can amplify
		// their wire size. Actual process/cgroup pressure remains a separate
		// guard. Each decoded request owns its retained headers independently,
		// including HTTP/2 requests multiplexed on that connection. Relay buffers
		// are charged only after policy/redirect handling, when serving a body.
		requestBytes := saturatingAddInt64(publicRequestWorkingBytes, publicRequestHeaderMemory(ctx.Request))
		if ctx.Request != nil && ctx.Request.ProtoMajor == 2 && ctx.Request.ContentLength != 0 {
			// HTTP/2 can buffer upload data while the origin is stalled. The
			// relay's copy buffer alone does not cover that receive credit.
			uploadCredit := int64(publicHTTP2ReceiveWindowBytes)
			if ctx.Request.ContentLength > 0 {
				uploadCredit = min(uploadCredit, ctx.Request.ContentLength)
			}
			requestBytes = saturatingAddInt64(requestBytes, uploadCredit)
		}
		release, ok, _ := ctx.App.tryReservePublicOwnedResource(adaptiveResourceOwnerPublicRequest, requestBytes, 0)
		if !ok {
			return rejectPublicRequestCapacity(ctx, "public_request_resource_pressure", "Public proxy resource pressure")
		}
		ctx.deferCleanup(release)
	}
	if ctx.App != nil && ctx.App.publicProxyRequests != nil {
		release, ok := ctx.App.publicProxyRequests.tryAcquire()
		if !ok {
			return rejectPublicRequestCapacity(ctx, "public_request_capacity", "Public proxy capacity reached")
		}
		ctx.deferCleanup(release)
	}
	if ctx.App != nil && ctx.App.publicClientRequests != nil {
		dynamicLimit := int64(-1)
		release, ok := ctx.App.publicClientRequests.tryAcquire(remoteIPForRateLimit(ctx.Request), dynamicLimit)
		if !ok {
			ctx.App.publicClientRequestRejected.Add(1)
			return rejectPublicRequestCapacity(ctx, "public_client_request_capacity", "Public client capacity reached")
		}
		ctx.deferCleanup(release)
	}
	if ctx.Request != nil && ctx.Request.ContentLength > publicRequestBodyLimit(ctx.App) {
		return rejectPublicRequestBodyTooLarge(ctx)
	}
	return publicProxyStageContinue
}

const publicRequestWorkingBytes = int64(32 * 1024)

// publicRequestHeaderMemory counts decoded data, not HPACK/wire bytes. The
// per-key/value allowances cover map entries, slice/string headers and rounding;
// two copies cover routing/transport header clones without relying on sharing.
// It performs no allocations and uses saturating arithmetic on public input.
func publicRequestHeaderMemory(r *http.Request) int64 {
	if r == nil {
		return 0
	}
	bytes := int64(len(r.Method))
	for _, value := range []string{r.Host, r.RequestURI} {
		bytes = saturatingAddInt64(bytes, int64(len(value)))
	}
	if r.URL != nil {
		for _, value := range []string{r.URL.Path, r.URL.RawPath, r.URL.RawQuery} {
			bytes = saturatingAddInt64(bytes, int64(len(value)))
		}
	}
	for key, values := range r.Header {
		bytes = saturatingAddInt64(bytes, int64(len(key))+128)
		for _, value := range values {
			bytes = saturatingAddInt64(bytes, int64(len(value))+32)
		}
	}
	return saturatingAddInt64(bytes, bytes)
}

// Both disk-cache/static responses and upstream responses need bounded copy
// storage. Local rejects and redirects finish before this stage and therefore
// do not compete with relay buffers. Existing security/policy ordering remains
// ahead of cache lookup and forwarding.
func publicResponseAdmissionStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.App == nil || ctx.App.agentStreamCapacity == nil {
		return publicProxyStageContinue
	}
	release, ok, _ := ctx.App.tryReservePublicOwnedResource(adaptiveResourceOwnerPublicResponse, reverseProxyCopyBufferSize, 0)
	if !ok {
		return rejectPublicRequestCapacity(ctx, "public_response_resource_pressure", "Public proxy resource pressure")
	}
	ctx.deferCleanup(release)
	return publicProxyStageContinue
}

func rejectPublicRequestCapacity(ctx *publicProxyContext, errorKind, message string) publicProxyStageResult {
	ctx.ResponseWriter.Header().Set("Retry-After", "1")
	http.Error(ctx.ResponseWriter, message, http.StatusServiceUnavailable)
	resolution := ctx.Resolution
	if !resolution.ListenerID.Valid {
		resolution.ListenerID = sql.NullInt64{Int64: ctx.ListenerID, Valid: true}
	}
	requestContext := ctx.RequestContext
	if resolution.Route.ID != 0 || resolution.RouteID.Valid {
		requestContext = proxyRequestContextFromResolution(ctx.Request, resolution)
	}
	ctx.App.recordProxyRequestEventWithRouteTargetIDsAndContext(
		context.Background(),
		http.StatusServiceUnavailable,
		time.Since(ctx.StartedAt),
		errorKind,
		resolution.ListenerID,
		resolution.RouteID,
		resolution.RouteTargetID,
		resolution.AgentID,
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		requestContext,
	)
	if ctx.Trace != nil {
		ctx.Trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED, &resolution, nil, http.StatusServiceUnavailable, errorKind, ctx.ResponseWriter.Header(), nil)
	}
	return publicProxyStageDone
}

func rejectPublicRequestBodyTooLarge(ctx *publicProxyContext) publicProxyStageResult {
	http.Error(ctx.ResponseWriter, "Request body too large", http.StatusRequestEntityTooLarge)
	ctx.App.recordProxyRequestEventWithIDsAndContext(
		context.Background(),
		http.StatusRequestEntityTooLarge,
		time.Since(ctx.StartedAt),
		"request_body_too_large",
		sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		sql.NullInt64{},
		sql.NullInt64{},
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		ctx.RequestContext,
	)
	if ctx.Trace != nil {
		resolution := publicRouteResolution{ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true}}
		ctx.Trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED,
			&resolution,
			nil,
			http.StatusRequestEntityTooLarge,
			"request_body_too_large",
			ctx.ResponseWriter.Header(),
			nil,
		)
	}
	return publicProxyStageDone
}

func (ctx *publicProxyContext) run() {
	defer ctx.runCleanup()
	for _, stage := range publicProxyStages {
		if stage(ctx) == publicProxyStageDone {
			return
		}
	}
}

func (ctx *publicProxyContext) deferCleanup(fn func()) {
	if fn == nil {
		return
	}
	ctx.cleanup = append(ctx.cleanup, fn)
}

func (ctx *publicProxyContext) runCleanup() {
	for i := len(ctx.cleanup) - 1; i >= 0; i-- {
		ctx.cleanup[i]()
	}
}

func rejectGloballyInvalidPublicPathStage(ctx *publicProxyContext) publicProxyStageResult {
	ctx.PathIssues = classifyPublicRequestPath(ctx.Request)
	if !ctx.PathIssues.DecodedDotSegment && !ctx.PathIssues.RawBackslash {
		return publicProxyStageContinue
	}
	return rejectInvalidPublicRequestTarget(ctx)
}

func routePathSecurityStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.App == nil {
		return publicProxyStageContinue
	}
	if !ctx.PathIssues.hasEncodedSeparator() {
		match, err := ctx.App.matchPublicRouteInSnapshot(ctx.Snapshot, ctx.ListenerID, ctx.Request)
		if err != nil {
			match.Err = err
		}
		ctx.RouteMatch = match
		ctx.HasRouteMatch = true
		return publicProxyStageContinue
	}
	match, err := ctx.App.matchPublicRouteInSnapshot(ctx.Snapshot, ctx.ListenerID, ctx.Request)
	if err != nil {
		match.Err = err
		ctx.RouteMatch = match
		ctx.HasRouteMatch = true
		if errors.Is(err, errNoPublicRouteAvailable) {
			return rejectInvalidPublicRequestTarget(ctx)
		}
		return publicProxyStageContinue
	}
	ctx.RouteMatch = match
	ctx.HasRouteMatch = true
	if publicRouteAllowsEncodedPathSeparators(match.Route) {
		ctx.EncodedPathSeparatorsAllowed = true
		return publicProxyStageContinue
	}
	return rejectInvalidPublicRequestTarget(ctx)
}

func rejectInvalidPublicRequestTarget(ctx *publicProxyContext) publicProxyStageResult {
	http.Error(ctx.ResponseWriter, "bad request", http.StatusBadRequest)
	ctx.App.recordProxyRequestEventWithIDsAndContext(
		context.Background(),
		http.StatusBadRequest,
		time.Since(ctx.StartedAt),
		publicInvalidRequestTargetErrorKind,
		sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		sql.NullInt64{},
		sql.NullInt64{},
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		ctx.RequestContext,
	)
	if ctx.Trace != nil {
		resolution := publicRouteResolution{ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true}}
		ctx.Trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED,
			&resolution,
			nil,
			http.StatusBadRequest,
			publicInvalidRequestTargetErrorKind,
			ctx.ResponseWriter.Header(),
			nil,
		)
	}
	return publicProxyStageDone
}

type publicPathIssueSet struct {
	DecodedDotSegment bool
	RawBackslash      bool
	EncodedSlash      bool
	EncodedBackslash  bool
}

func (s publicPathIssueSet) hasEncodedSeparator() bool {
	return s.EncodedSlash || s.EncodedBackslash
}

func classifyPublicRequestPath(r *http.Request) publicPathIssueSet {
	var issues publicPathIssueSet
	if r == nil || r.URL == nil {
		return issues
	}
	issues.DecodedDotSegment = containsPublicPathDotSegment(r.URL.Path)
	rawTargetPath := requestTargetPathForPublicValidation(r)
	issues.RawBackslash = containsRawPublicPathBackslash(rawTargetPath) || containsRawPublicPathBackslash(r.URL.RawPath)
	for _, path := range []string{r.URL.RawPath, r.URL.EscapedPath(), rawTargetPath} {
		if containsEncodedPublicPathSlash(path) {
			issues.EncodedSlash = true
		}
		if containsEncodedPublicPathBackslash(path) {
			issues.EncodedBackslash = true
		}
	}
	return issues
}

func containsPublicPathDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func containsRawPublicPathBackslash(path string) bool {
	return strings.Contains(path, `\`)
}

func containsEncodedPublicPathSlash(path string) bool {
	return strings.Contains(strings.ToLower(path), "%2f")
}

func containsEncodedPublicPathBackslash(path string) bool {
	return strings.Contains(strings.ToLower(path), "%5c")
}

func publicRequestHasEncodedPathSeparator(r *http.Request) bool {
	return classifyPublicRequestPath(r).hasEncodedSeparator()
}

func publicRouteAllowsEncodedPathSeparators(route publicRouteConfig) bool {
	return normalizePublicRoutePathSecurityMode(route.PathSecurityMode) == publicRoutePathSecurityModeAllowEncodedSeparators
}

func requestTargetPathForPublicValidation(r *http.Request) string {
	if r == nil {
		return ""
	}
	target := r.RequestURI
	if target == "" && r.URL != nil {
		target = r.URL.RequestURI()
	}
	if idx := strings.IndexByte(target, '?'); idx >= 0 {
		target = target[:idx]
	}
	if strings.HasPrefix(target, "/") || target == "" {
		return target
	}
	if schemeIdx := strings.Index(target, "://"); schemeIdx >= 0 {
		rest := target[schemeIdx+3:]
		if pathIdx := strings.IndexByte(rest, '/'); pathIdx >= 0 {
			return rest[pathIdx:]
		}
		return "/"
	}
	return target
}

func serveACMEChallengeStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.App.PublicACME == nil || !ctx.App.PublicACME.ServeHTTPChallenge(ctx.ResponseWriter, ctx.Request) {
		return publicProxyStageContinue
	}
	statusCode := ctx.Recorder.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	ctx.App.recordProxyRequestEventWithIDsAndContext(
		context.Background(),
		statusCode,
		time.Since(ctx.StartedAt),
		"",
		sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		sql.NullInt64{},
		sql.NullInt64{},
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		ctx.RequestContext,
	)
	return publicProxyStageDone
}

func serveWAFReservedStage(ctx *publicProxyContext) publicProxyStageResult {
	decision, handled := ctx.App.servePublicWAFReservedWithSnapshot(ctx.Snapshot, ctx.ResponseWriter, ctx.Request, ctx.ListenerID)
	if !handled {
		return publicProxyStageContinue
	}
	statusCode := ctx.Recorder.statusCode
	if statusCode == 0 {
		statusCode = decision.StatusCode
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if ctx.Trace != nil && decision.Action != "" {
		resolution := traceResolutionFromWafDecision(decision, ctx.ListenerID)
		ctx.Trace.emit(
			wafTraceStage(decision),
			&resolution,
			nil,
			statusCode,
			decision.ErrorKind,
			ctx.ResponseWriter.Header(),
			wafDebugAttributes(decision),
		)
	}
	ctx.App.recordProxyRequestEventWithPolicyIDsAndContext(
		context.Background(),
		statusCode,
		time.Since(ctx.StartedAt),
		decision.ErrorKind,
		sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		sql.NullInt64{},
		sql.NullInt64{Int64: decision.Rule.ID, Valid: decision.Rule.ID != 0},
		decision.Action,
		sql.NullInt64{},
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		ctx.RequestContext,
	)
	return publicProxyStageDone
}

func beginWAFPressureStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.App.PublicWAF != nil {
		ctx.deferCleanup(ctx.App.PublicWAF.beginProxyRequest())
	}
	return publicProxyStageContinue
}

func wafPolicyStage(ctx *publicProxyContext) publicProxyStageResult {
	decision, allowed := ctx.App.checkPublicWAFWithSnapshot(ctx.Snapshot, ctx.ListenerID, ctx.Request)
	if allowed {
		return publicProxyStageContinue
	}
	writePublicWafResponse(ctx.ResponseWriter, ctx.Request, decision)
	statusCode := ctx.Recorder.statusCode
	if statusCode == 0 {
		statusCode = decision.StatusCode
	}
	if statusCode == 0 {
		statusCode = http.StatusForbidden
	}
	if ctx.Trace != nil {
		resolution := traceResolutionFromWafDecision(decision, ctx.ListenerID)
		stage := wafTraceStage(decision)
		attributes := wafDebugAttributes(decision)
		if decision.MatchError != nil {
			stage = p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED
			attributes["policy_match_error"] = decision.MatchError.Error()
		}
		ctx.Trace.emit(
			stage,
			&resolution,
			nil,
			statusCode,
			decision.ErrorKind,
			ctx.ResponseWriter.Header(),
			attributes,
		)
	}
	ctx.App.recordProxyRequestEventWithPolicyIDsAndContext(
		context.Background(),
		statusCode,
		time.Since(ctx.StartedAt),
		decision.ErrorKind,
		sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		sql.NullInt64{},
		sql.NullInt64{Int64: decision.Rule.ID, Valid: decision.Rule.ID != 0},
		decision.Action,
		sql.NullInt64{},
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		ctx.RequestContext,
	)
	return publicProxyStageDone
}

func rateLimitStage(ctx *publicProxyContext) publicProxyStageResult {
	decision, allowed := ctx.App.checkPublicRateLimitsWithSnapshot(ctx.Snapshot, ctx.ListenerID, ctx.Request)
	if allowed {
		return publicProxyStageContinue
	}
	writeRateLimitResponse(ctx.ResponseWriter, decision)
	if ctx.Trace != nil {
		resolution := publicRouteResolution{
			Listener:           decision.Listener,
			ListenerID:         sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
			RateLimitRuleID:    decision.Rule.ID,
			RateLimitRuleName:  decision.Rule.Name,
			RateLimitAlgorithm: decision.Rule.Algorithm,
		}
		stage := p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RATE_LIMITED
		errorKind := "rate_limited"
		attributes := map[string]string{
			"handler":              "rate_limit",
			"rate_limit_rule_id":   strconv.FormatInt(decision.Rule.ID, 10),
			"rate_limit_rule_name": decision.Rule.Name,
			"rate_limit_algorithm": decision.Rule.Algorithm,
		}
		if decision.MatchError != nil {
			stage = p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED
			errorKind = decision.ErrorKind
			attributes["policy_match_error"] = decision.MatchError.Error()
		}
		ctx.Trace.emit(
			stage,
			&resolution,
			nil,
			decision.StatusCode,
			errorKind,
			ctx.ResponseWriter.Header(),
			attributes,
		)
	}
	ctx.App.recordProxyRequestEventWithIDsAndContext(
		context.Background(),
		decision.StatusCode,
		time.Since(ctx.StartedAt),
		decision.ErrorKind,
		sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		sql.NullInt64{},
		sql.NullInt64{},
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		ctx.RequestContext,
	)
	return publicProxyStageDone
}

func trafficShaperStage(ctx *publicProxyContext) publicProxyStageResult {
	decision, ok := ctx.App.selectPublicTrafficShaperWithSnapshot(ctx.Snapshot, ctx.ListenerID, ctx.Request)
	if !ok {
		return publicProxyStageContinue
	}
	if decision.MatchError != nil {
		return rejectPublicPolicyMatchFailure(ctx, decision.MatchError)
	}
	ctx.TrafficShaperDecision = decision
	ctx.TrafficShaperSelected = true
	if ctx.Trace != nil {
		resolution := publicRouteResolution{
			Listener:   decision.Listener,
			ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		}
		applyTrafficShaperResolutionFields(&resolution, decision)
		ctx.Trace.emit(
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
				"traffic_shaper_protocol_scope":  decision.Rule.ProtocolScope,
				"traffic_shaper_upload_bps":      strconv.FormatInt(decision.Rule.UploadBytesPerSecond, 10),
				"traffic_shaper_download_bps":    strconv.FormatInt(decision.Rule.DownloadBytesPerSecond, 10),
				"traffic_shaper_request_exempt":  strconv.FormatInt(decision.Rule.RequestExemptBytes, 10),
				"traffic_shaper_response_exempt": strconv.FormatInt(decision.Rule.ResponseExemptBytes, 10),
			},
		)
	}
	return publicProxyStageContinue
}

func rejectPublicPolicyMatchFailure(ctx *publicProxyContext, matchErr error) publicProxyStageResult {
	http.Error(ctx.ResponseWriter, strings.TrimSuffix(publicPolicyMatchFailureBody, "\n"), http.StatusServiceUnavailable)
	resolution := publicRouteResolution{ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true}}
	ctx.App.recordProxyRequestEventWithIDsAndContext(
		context.Background(),
		http.StatusServiceUnavailable,
		time.Since(ctx.StartedAt),
		publicPolicyMatchFailureErrorKind,
		resolution.ListenerID,
		sql.NullInt64{},
		sql.NullInt64{},
		ctx.Observability.requestBytesValue(),
		ctx.Observability.responseBytesValue(),
		ctx.RequestContext,
	)
	if ctx.Trace != nil {
		attributes := map[string]string{"handler": "policy_match"}
		if matchErr != nil {
			attributes["policy_match_error"] = matchErr.Error()
		}
		ctx.Trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED,
			&resolution,
			nil,
			http.StatusServiceUnavailable,
			publicPolicyMatchFailureErrorKind,
			ctx.ResponseWriter.Header(),
			attributes,
		)
	}
	return publicProxyStageDone
}

func routeResolutionStage(ctx *publicProxyContext) publicProxyStageResult {
	var (
		resolution publicRouteResolution
		err        error
	)
	if ctx.HasRouteMatch {
		resolution, err = ctx.App.resolvePublicRouteFromMatch(ctx.RouteMatch)
	} else {
		match, matchErr := ctx.App.matchPublicRouteInSnapshot(ctx.Snapshot, ctx.ListenerID, ctx.Request)
		if matchErr != nil {
			err = matchErr
		} else {
			resolution, err = ctx.App.resolvePublicRouteFromMatch(match)
		}
	}
	if err != nil {
		statusCode := http.StatusBadGateway
		errorKind := "route_resolution_failed"
		if errors.Is(err, errNoPublicRouteAvailable) {
			statusCode = http.StatusNotFound
			errorKind = "no_route"
			http.NotFound(ctx.ResponseWriter, ctx.Request)
		} else if errors.Is(err, errMalformedPublicAuthority) {
			statusCode = http.StatusBadRequest
			errorKind = "invalid_authority"
			http.Error(ctx.ResponseWriter, "bad request", statusCode)
		} else if errors.Is(err, errPublicSiteSNIMismatch) {
			statusCode = http.StatusMisdirectedRequest
			errorKind = "site_sni_host_mismatch"
			http.Error(ctx.ResponseWriter, "misdirected request", statusCode)
		} else if errors.Is(err, errNoRouteTargetAvailable) || errors.Is(err, errNoRouteBackendAvailable) {
			statusCode = http.StatusServiceUnavailable
			errorKind = "no_route_target_available"
			writeNoRouteTargetAvailable(ctx.ResponseWriter)
		} else {
			http.Error(ctx.ResponseWriter, err.Error(), statusCode)
		}
		ctx.App.recordProxyRequestEventWithIDsAndContext(
			context.Background(),
			statusCode,
			time.Since(ctx.StartedAt),
			errorKind,
			sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
			sql.NullInt64{},
			sql.NullInt64{},
			ctx.Observability.requestBytesValue(),
			ctx.Observability.responseBytesValue(),
			ctx.RequestContext,
		)
		if ctx.Trace != nil {
			failureResolution := publicRouteResolution{ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true}}
			if ctx.TrafficShaperSelected {
				applyTrafficShaperResolutionFields(&failureResolution, ctx.TrafficShaperDecision)
			}
			ctx.Trace.emit(
				p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_FAILED,
				&failureResolution,
				nil,
				statusCode,
				errorKind,
				ctx.ResponseWriter.Header(),
				nil,
			)
		}
		return publicProxyStageDone
	}
	if ctx.TrafficShaperSelected {
		applyTrafficShaperResolutionFields(&resolution, ctx.TrafficShaperDecision)
	}
	ctx.Resolution = resolution
	if ctx.Trace != nil {
		ctx.Trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ROUTE_RESOLVED, &ctx.Resolution, nil, 0, "", nil, nil)
	}
	return publicProxyStageContinue
}

func redirectStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.Resolution.Action != publicRouteActionRedirect {
		return publicProxyStageContinue
	}
	ctx.App.redirectRouteResponse(ctx.ResponseWriter, ctx.Request, ctx.Resolution, ctx.Trace, ctx.Observability)
	return publicProxyStageDone
}

func routeSelectionTraceStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.Trace == nil {
		return publicProxyStageContinue
	}
	attributes := map[string]string(nil)
	if ctx.Resolution.RouteLoadBalancing != "" {
		attributes = map[string]string{
			"route_load_balancer": ctx.Resolution.RouteLoadBalancing,
			"route_fallback":      strconv.FormatBool(ctx.Resolution.RouteFallbackSelected),
		}
	}
	ctx.Trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_BACKEND_SELECTED, &ctx.Resolution, nil, 0, "", nil, attributes)
	return publicProxyStageContinue
}

func cacheLookupStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.Resolution.Target.TargetType != publicRouteTargetTypeProxy {
		return publicProxyStageContinue
	}
	decision := ctx.App.checkPublicCacheWithSnapshot(ctx.Snapshot, ctx.Request, ctx.Resolution)
	if decision.Status == publicCacheStatusHit && !ctx.App.preparePublicCacheHitBody(ctx.Request, &decision) {
		decision.Status = publicCacheStatusMiss
		decision.Entry = nil
		decision.HitBody = nil
	}
	applyCacheResolutionFields(&ctx.Resolution, decision)
	if ctx.Trace != nil {
		ctx.Trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_CACHE_LOOKUP,
			&ctx.Resolution,
			nil,
			0,
			"",
			nil,
			publicCacheTraceAttributes(decision),
		)
	}
	switch decision.Status {
	case publicCacheStatusHit:
		if ctx.Trace != nil {
			ctx.Trace.emit(
				p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_CACHE_HIT,
				&ctx.Resolution,
				nil,
				int(decision.Entry.StatusCode),
				"",
				nil,
				publicCacheTraceAttributes(decision),
			)
		}
		ctx.App.servePublicCacheHit(ctx.ResponseWriter, ctx.Request, ctx.Resolution, ctx.Trace, trafficShaperDecisionIfSelected(ctx.TrafficShaperDecision, ctx.TrafficShaperSelected), decision, ctx.Observability)
		return publicProxyStageDone
	case publicCacheStatusMiss:
		// Only a miss retains the decision during forwarding. Keeping this
		// copy separate lets bypasses use stack storage, including listeners
		// with caching disabled.
		retainedDecision := decision
		ctx.CacheDecision = &retainedDecision
		if ctx.Trace != nil {
			ctx.Trace.emit(
				p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_CACHE_MISS,
				&ctx.Resolution,
				nil,
				0,
				"",
				nil,
				publicCacheTraceAttributes(decision),
			)
		}
	default:
		if ctx.Trace != nil {
			ctx.Trace.emit(
				p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_CACHE_BYPASS,
				&ctx.Resolution,
				nil,
				0,
				"",
				nil,
				publicCacheTraceAttributes(decision),
			)
		}
	}
	return publicProxyStageContinue
}

func activeTargetAccountingAndForwardStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx.Resolution.Target.TargetType == publicRouteTargetTypeProxy && ctx.Resolution.RouteTargetID.Valid && ctx.App.publicTargetRequests != nil {
		release, ok := ctx.App.publicTargetRequests.tryAcquire(ctx.Resolution.RouteTargetID.Int64)
		if !ok {
			return rejectPublicRequestCapacity(ctx, "route_target_capacity", "Route target capacity reached")
		}
		defer release()
	}
	if ctx.Resolution.RouteTargetID.Valid {
		done := ctx.App.beginPublicRouteTargetRequest(ctx.Resolution.RouteTargetID.Int64)
		defer done()
	}
	shaper := trafficShaperDecisionIfSelected(ctx.TrafficShaperDecision, ctx.TrafficShaperSelected)
	if ctx.Resolution.Target.TargetType == publicRouteTargetTypeStatic {
		ctx.App.staticTargetResponse(ctx.ResponseWriter, ctx.Request, ctx.Resolution, ctx.Trace, shaper, ctx.Observability)
		return publicProxyStageDone
	}
	if ctx.Resolution.Target.Transport == publicRouteTargetTransportAgent {
		ctx.App.proxyAgentTargetRequestWithResolution(ctx.ResponseWriter, ctx.Request, &ctx.Resolution, ctx.Trace, shaper, ctx.CacheDecision, ctx.Observability)
		return publicProxyStageDone
	}
	ctx.App.proxyDirectTargetRequestWithResolution(ctx.ResponseWriter, ctx.Request, &ctx.Resolution, ctx.Trace, shaper, ctx.CacheDecision, ctx.Observability)
	return publicProxyStageDone
}
