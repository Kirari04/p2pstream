package server

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

type traceResponseRecorder struct {
	http.ResponseWriter
	statusCode int
	bytes      uint64
}

func (r *traceResponseRecorder) WriteHeader(statusCode int) {
	if r.statusCode == 0 {
		r.statusCode = statusCode
	}
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *traceResponseRecorder) Write(data []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(data)
	r.bytes += uint64(n)
	return n, err
}

func (r *traceResponseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *traceResponseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *traceResponseRecorder) responseBytes() uint64 {
	if r == nil {
		return 0
	}
	return r.bytes
}

type trafficRequestTrace struct {
	tracer         *trafficTracer
	requestID      uuid.UUID
	startedAt      time.Time
	level          p2pstreamv1.TrafficTraceLevel
	method         string
	host           string
	path           string
	query          string
	requestHeaders map[string]string
	requestBytes   uint64
	recorder       *traceResponseRecorder
}

func (a *App) newTrafficRequestTrace(r *http.Request, recorder *traceResponseRecorder) *trafficRequestTrace {
	if a == nil || a.TrafficTracer == nil {
		return nil
	}
	level, ok := a.TrafficTracer.enabledLevel()
	if !ok {
		return nil
	}
	requestID, err := uuid.NewV7()
	if err != nil {
		requestID = uuid.New()
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	trace := &trafficRequestTrace{
		tracer:    a.TrafficTracer,
		requestID: requestID,
		startedAt: time.Now(),
		level:     level,
		method:    r.Method,
		host:      r.Host,
		path:      path,
		query:     r.URL.RawQuery,
		recorder:  recorder,
	}
	if level >= p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_HEADERS {
		trace.requestHeaders = sanitizedHeaderMap(r.Header)
	}
	if level >= p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG && r.ContentLength > 0 {
		trace.requestBytes = uint64(r.ContentLength)
	}
	return trace
}

func (t *trafficRequestTrace) uuid() uuid.UUID {
	if t == nil {
		return uuid.Nil
	}
	return t.requestID
}

func (t *trafficRequestTrace) emitReceived(listenerID int64) {
	if t == nil {
		return
	}
	t.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RECEIVED, &publicRouteResolution{
		ListenerID: sql.NullInt64{Int64: listenerID, Valid: true},
	}, nil, 0, "", nil, map[string]string{
		"listener_id": int64TraceString(listenerID),
	})
}

func (t *trafficRequestTrace) emit(
	stage p2pstreamv1.TrafficTraceStage,
	resolution *publicRouteResolution,
	agent *AgentConn,
	statusCode int,
	errorKind string,
	responseHeaders http.Header,
	debugAttributes map[string]string,
) {
	if t == nil || t.tracer == nil {
		return
	}
	event := &p2pstreamv1.TrafficTraceEvent{
		RequestId:            t.requestID.String(),
		Stage:                stage,
		OccurredAtUnixMillis: time.Now().UnixMilli(),
		Method:               t.method,
		Path:                 t.path,
		DurationMs:           time.Since(t.startedAt).Milliseconds(),
	}
	if statusCode > 0 {
		event.StatusCode = int64(statusCode)
	}
	if errorKind != "" {
		event.ErrorKind = errorKind
	}
	if t.level >= p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DETAILED {
		event.Host = t.host
		event.Query = t.query
	}
	if t.level >= p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_HEADERS {
		event.RequestHeaders = t.requestHeaders
		event.ResponseHeaders = sanitizedHeaderMap(responseHeaders)
	}
	if t.level >= p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG {
		event.RequestBytes = t.requestBytes
		event.ResponseBytes = t.recorder.responseBytes()
		event.DebugAttributes = debugAttributes
		if event.DebugAttributes == nil {
			event.DebugAttributes = map[string]string{}
		}
		event.DebugAttributes["elapsed_ms"] = int64TraceString(event.DurationMs)
	}
	if resolution != nil {
		fillTrafficTraceResolution(event, *resolution)
	}
	if agent != nil {
		event.AgentId = agent.AgentID
		event.AgentPublicId = agent.PublicID
		event.AgentName = agent.Name
	}
	t.tracer.publish(event)
}

func fillTrafficTraceResolution(event *p2pstreamv1.TrafficTraceEvent, resolution publicRouteResolution) {
	if resolution.Listener.ID != 0 {
		event.ListenerId = resolution.Listener.ID
		event.ListenerName = resolution.Listener.Name
	} else if resolution.ListenerID.Valid {
		event.ListenerId = resolution.ListenerID.Int64
	}
	if resolution.Route.ID != 0 {
		event.RouteId = resolution.Route.ID
	}
	event.DefaultRoute = resolution.DefaultRoute
	event.RouteLabel = traceRouteLabel(resolution)
	if resolution.Backend.ID != 0 {
		event.BackendId = resolution.Backend.ID
		event.BackendName = resolution.Backend.Name
		event.TargetOrigin = resolution.Backend.TargetOrigin
		event.BackendType = protoBackendTypeFromString(resolution.Backend.BackendType)
		event.ForwardMode = protoForwardModeFromString(resolution.Backend.ForwardMode)
	} else if resolution.BackendID.Valid {
		event.BackendId = resolution.BackendID.Int64
	}
	if resolution.AgentID.Valid && event.AgentId == 0 {
		event.AgentId = resolution.AgentID.Int64
	}
	if resolution.RateLimitRuleID != 0 {
		event.RateLimitRuleId = resolution.RateLimitRuleID
		event.RateLimitRuleName = resolution.RateLimitRuleName
		event.RateLimitAlgorithm = protoRateLimitAlgorithmFromString(resolution.RateLimitAlgorithm)
	}
}

func traceRouteLabel(resolution publicRouteResolution) string {
	if resolution.DefaultRoute {
		return "Default route"
	}
	if resolution.Route.ID == 0 {
		return ""
	}
	var parts []string
	if resolution.Route.HostPattern != "" {
		parts = append(parts, resolution.Route.HostPattern)
	}
	if resolution.Route.PathPrefix != "" {
		parts = append(parts, resolution.Route.PathPrefix)
	}
	if len(parts) == 0 {
		return "Route #" + int64TraceString(resolution.Route.ID)
	}
	return strings.Join(parts, " ")
}

func int64TraceString(value int64) string {
	return strconv.FormatInt(value, 10)
}
