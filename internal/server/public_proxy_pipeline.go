package server

import (
	"context"
	"database/sql"
	"errors"
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

type publicProxyStage func(*publicProxyContext) publicProxyStageResult

type publicProxyContext struct {
	App        *App
	ListenerID int64
	StartedAt  time.Time

	Writer         http.ResponseWriter
	ResponseWriter http.ResponseWriter
	Recorder       *proxyResponseRecorder
	Request        *http.Request
	Observability  proxyRequestObservability
	RequestContext proxyRequestContext
	Trace          *trafficRequestTrace

	Resolution             publicRouteResolution
	TrafficShaperSelected  bool
	TrafficShaperDecision  publicTrafficShaperDecision
	CacheDecision          *publicCacheDecision
	RouteResolutionFailure error
	cleanup                []func()
}

var publicProxyStages = []publicProxyStage{
	rejectAmbiguousPublicPathStage,
	serveACMEChallengeStage,
	serveWAFReservedStage,
	beginWAFPressureStage,
	wafPolicyStage,
	rateLimitStage,
	trafficShaperStage,
	routeResolutionStage,
	redirectStage,
	routeSelectionTraceStage,
	cacheLookupStage,
	activeTargetAccountingAndForwardStage,
}

func newPublicProxyContext(app *App, listenerID int64, w http.ResponseWriter, r *http.Request) *publicProxyContext {
	var requestBytes atomic.Uint64
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &countingReadCloser{ReadCloser: r.Body, bytes: &requestBytes}
	}
	recorder := &proxyResponseRecorder{ResponseWriter: w}
	responseWriter := http.ResponseWriter(recorder)
	observability := proxyRequestObservability{requestBytes: &requestBytes, responseRecorder: recorder}
	trace := app.newTrafficRequestTrace(r, recorder)
	if trace != nil {
		trace.emitReceived(listenerID)
	}
	return &publicProxyContext{
		App:            app,
		ListenerID:     listenerID,
		StartedAt:      time.Now(),
		Writer:         w,
		ResponseWriter: responseWriter,
		Recorder:       recorder,
		Request:        r,
		Observability:  observability,
		RequestContext: proxyRequestContextFromHTTP(r),
		Trace:          trace,
	}
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

func rejectAmbiguousPublicPathStage(ctx *publicProxyContext) publicProxyStageResult {
	if !publicRequestPathIsAmbiguous(ctx.Request) {
		return publicProxyStageContinue
	}
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

func publicRequestPathIsAmbiguous(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if containsAmbiguousPublicPathEscape(r.URL.RawPath) ||
		containsAmbiguousPublicPathEscape(r.URL.EscapedPath()) ||
		containsAmbiguousPublicPathEscape(requestTargetPathForPublicValidation(r)) {
		return true
	}
	return strings.Contains(r.URL.Path, `\`) || containsPublicPathDotSegment(r.URL.Path)
}

func containsAmbiguousPublicPathEscape(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "%2e") ||
		strings.Contains(path, "%2f") ||
		strings.Contains(path, "%5c")
}

func containsPublicPathDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
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
	decision, handled := ctx.App.servePublicWAFReserved(ctx.ResponseWriter, ctx.Request, ctx.ListenerID)
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
	decision, allowed := ctx.App.checkPublicWAF(ctx.ListenerID, ctx.Request)
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

func rateLimitStage(ctx *publicProxyContext) publicProxyStageResult {
	decision, allowed := ctx.App.checkPublicRateLimits(ctx.ListenerID, ctx.Request)
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
		ctx.Trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RATE_LIMITED,
			&resolution,
			nil,
			decision.StatusCode,
			"rate_limited",
			ctx.ResponseWriter.Header(),
			map[string]string{
				"handler":              "rate_limit",
				"rate_limit_rule_id":   strconv.FormatInt(decision.Rule.ID, 10),
				"rate_limit_rule_name": decision.Rule.Name,
				"rate_limit_algorithm": decision.Rule.Algorithm,
			},
		)
	}
	ctx.App.recordProxyRequestEventWithIDsAndContext(
		context.Background(),
		decision.StatusCode,
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

func trafficShaperStage(ctx *publicProxyContext) publicProxyStageResult {
	decision, ok := ctx.App.selectPublicTrafficShaper(ctx.ListenerID, ctx.Request)
	if !ok {
		return publicProxyStageContinue
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
				"traffic_shaper_upload_bps":      strconv.FormatInt(decision.Rule.UploadBytesPerSecond, 10),
				"traffic_shaper_download_bps":    strconv.FormatInt(decision.Rule.DownloadBytesPerSecond, 10),
				"traffic_shaper_request_exempt":  strconv.FormatInt(decision.Rule.RequestExemptBytes, 10),
				"traffic_shaper_response_exempt": strconv.FormatInt(decision.Rule.ResponseExemptBytes, 10),
			},
		)
	}
	return publicProxyStageContinue
}

func routeResolutionStage(ctx *publicProxyContext) publicProxyStageResult {
	resolution, err := ctx.App.resolvePublicRoute(ctx.ListenerID, ctx.Request)
	if err != nil {
		statusCode := http.StatusBadGateway
		errorKind := "route_resolution_failed"
		if errors.Is(err, errNoPublicRouteAvailable) {
			statusCode = http.StatusNotFound
			errorKind = "no_route"
			http.NotFound(ctx.ResponseWriter, ctx.Request)
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
	decision := ctx.App.checkPublicCache(ctx.Request, ctx.Resolution)
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
		ctx.CacheDecision = &decision
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
		ctx.App.proxyAgentTargetRequest(ctx.ResponseWriter, ctx.Request, ctx.Resolution, ctx.Trace, shaper, ctx.CacheDecision, ctx.Observability)
		return publicProxyStageDone
	}
	ctx.App.proxyDirectTargetRequest(ctx.ResponseWriter, ctx.Request, ctx.Resolution, ctx.Trace, shaper, ctx.CacheDecision, ctx.Observability)
	return publicProxyStageDone
}
