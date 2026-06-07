package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

var dashboardWindows = []struct {
	Label string
	Since time.Duration
}{
	{Label: "5m", Since: 5 * time.Minute},
	{Label: "1h", Since: time.Hour},
	{Label: "24h", Since: 24 * time.Hour},
	{Label: "30d", Since: 30 * 24 * time.Hour},
}

const (
	dashboardTopWindow            = time.Hour
	dashboardTrafficBucketWindow  = time.Hour
	dashboardTrafficBucketSeconds = int64(5 * 60)
	observabilityCleanupInterval  = time.Hour
)

func (a *App) GetDashboard(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetDashboardRequest],
) (*connect.Response[p2pstreamv1.GetDashboardResponse], error) {
	if _, err := a.requireUser(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.DB == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("database is required for dashboard"))
	}

	now := time.Now().UTC()
	if a.dashboardCacheActive() {
		return connect.NewResponse(a.dashboardResponseFromCache(now)), nil
	}

	resp, err := a.buildDashboardDirect(ctx, now)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (a *App) buildDashboardDirect(ctx context.Context, now time.Time) (*p2pstreamv1.GetDashboardResponse, error) {
	useRollups, err := a.observabilityRollupsReady(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	windows := make([]*p2pstreamv1.DashboardWindowSummary, 0, len(dashboardWindows))
	for _, window := range dashboardWindows {
		since := now.Add(-window.Since)
		if useRollups {
			sinceUnixMillis := rollupBucketUnixMillis(since)
			proxySummary, err := a.DB.GetProxyRequestRollupSummarySince(ctx, sinceUnixMillis)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			agentSummary, err := a.DB.GetAgentStatsRollupSummarySince(ctx, sinceUnixMillis)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			windows = append(windows, dashboardWindowSummary(
				window.Label,
				sinceUnixMillis,
				dashboardProxyWindowFromRollup(proxySummary),
				dashboardAgentWindowFromRollup(agentSummary),
			))
			continue
		}

		proxySummary, err := a.DB.GetProxyRequestSummarySince(ctx, since)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		agentSummary, err := a.DB.GetAgentStatsSummarySince(ctx, since)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		windows = append(windows, dashboardWindowSummary(
			window.Label,
			since.UnixMilli(),
			dashboardProxyWindowFromRaw(proxySummary),
			dashboardAgentWindowFromRaw(agentSummary),
		))
	}

	agentConnections, err := a.agentConnectionSummary(ctx, now)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	agentUptimeSummaries, recentAgentConnections, err := a.agentUptimeDashboard(ctx, now)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	topSince := now.Add(-dashboardTopWindow)
	trafficSince := now.Add(-dashboardTrafficBucketWindow)

	var topListeners []*p2pstreamv1.DashboardProxyDimensionSummary
	var topRoutes []*p2pstreamv1.DashboardProxyDimensionSummary
	var topRouteTargets []*p2pstreamv1.DashboardProxyDimensionSummary
	var topAgents []*p2pstreamv1.DashboardProxyDimensionSummary
	var topErrorKinds []*p2pstreamv1.DashboardProxyDimensionSummary
	var statusClasses []*p2pstreamv1.DashboardProxyDimensionSummary
	var trafficBuckets []*p2pstreamv1.DashboardTrafficBucket

	if useRollups {
		topSinceUnixMillis := rollupBucketUnixMillis(topSince)
		trafficSinceUnixMillis := rollupBucketUnixMillis(trafficSince)
		topListenerRows, err := a.DB.ListTopProxyListenersRollupsSince(ctx, topSinceUnixMillis)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topRouteRows, err := a.DB.ListTopProxyRoutesRollupsSince(ctx, topSinceUnixMillis)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topRouteTargetRows, err := a.DB.ListTopProxyRouteTargetsRollupsSince(ctx, topSinceUnixMillis)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topAgentRows, err := a.DB.ListTopProxyAgentsRollupsSince(ctx, topSinceUnixMillis)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topErrorRows, err := a.DB.ListTopProxyErrorKindsRollupsSince(ctx, topSinceUnixMillis)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		statusClassRows, err := a.DB.ListProxyStatusClassesRollupsSince(ctx, topSinceUnixMillis)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		trafficBucketRows, err := a.DB.ListProxyTrafficBucketRollupsSince(ctx, db.ListProxyTrafficBucketRollupsSinceParams{
			BucketSeconds:   dashboardTrafficBucketSeconds,
			SinceUnixMillis: trafficSinceUnixMillis,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topListeners = dashboardRollupListenerSummaries(topListenerRows)
		topRoutes = dashboardRollupRouteSummaries(topRouteRows)
		topRouteTargets = dashboardRollupRouteTargetSummaries(topRouteTargetRows)
		topAgents = dashboardRollupAgentSummaries(topAgentRows)
		topErrorKinds = dashboardRollupErrorKindSummaries(topErrorRows)
		statusClasses = dashboardRollupStatusClassSummaries(statusClassRows)
		trafficBuckets = dashboardRollupTrafficBuckets(trafficBucketRows)
	} else {
		topListenerRows, err := a.DB.ListTopProxyListenersSince(ctx, topSince)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topRouteRows, err := a.DB.ListTopProxyRoutesSince(ctx, topSince)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topRouteTargetRows, err := a.DB.ListTopProxyRouteTargetsSince(ctx, topSince)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topAgentRows, err := a.DB.ListTopProxyAgentsSince(ctx, topSince)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topErrorRows, err := a.DB.ListTopProxyErrorKindsSince(ctx, topSince)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		statusClassRows, err := a.DB.ListProxyStatusClassesSince(ctx, topSince)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		trafficBucketRows, err := a.DB.ListProxyTrafficBucketsSince(ctx, db.ListProxyTrafficBucketsSinceParams{
			BucketSeconds: dashboardTrafficBucketSeconds,
			Since:         trafficSince,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		topListeners = dashboardListenerSummaries(topListenerRows)
		topRoutes = dashboardRouteSummaries(topRouteRows)
		topRouteTargets = dashboardRouteTargetSummaries(topRouteTargetRows)
		topAgents = dashboardAgentSummaries(topAgentRows)
		topErrorKinds = dashboardErrorKindSummaries(topErrorRows)
		statusClasses = dashboardStatusClassSummaries(statusClassRows)
		trafficBuckets = dashboardTrafficBuckets(trafficBucketRows)
	}

	resp := &p2pstreamv1.GetDashboardResponse{
		Status:                 a.statusResponse(),
		Windows:                windows,
		AgentConnections:       agentConnections,
		RetentionDays:          int64(a.observabilityRetentionDays()),
		GeneratedAtUnixMillis:  now.UnixMilli(),
		TopListeners:           topListeners,
		TopRoutes:              topRoutes,
		TopRouteTargets:        topRouteTargets,
		TopAgents:              topAgents,
		TopErrorKinds:          topErrorKinds,
		StatusClasses:          statusClasses,
		TrafficBuckets:         trafficBuckets,
		ManagementSecurity:     a.managementSecurity(),
		AgentUptimeSummaries:   agentUptimeSummaries,
		RecentAgentConnections: recentAgentConnections,
	}
	return resp, nil
}

func (a *App) managementSecurity() *p2pstreamv1.ManagementSecurity {
	cfg := a.Config
	if cfg == nil {
		return &p2pstreamv1.ManagementSecurity{}
	}
	return &p2pstreamv1.ManagementSecurity{
		TlsEnabled:                     cfg.ManagementTLSEnabled,
		AutoTls:                        cfg.ManagementTLSAutoGenerated,
		InsecureHttpAllowed:            cfg.ManagementAllowInsecureHTTP,
		AgentHttpsRequired:             !cfg.ManagementAllowInsecureHTTP,
		AgentClientCertificateRequired: strings.TrimSpace(cfg.ManagementTLSClientCAFile) != "",
		DefaultManagementUrl:           cfg.ManagementDefaultURL,
		ManagementCaPem:                cfg.ManagementCAPEM,
		ManagementCaSha256:             cfg.ManagementCASHA256,
		DetectedAdvertiseHost:          cfg.ManagementDetectedAdvertiseHost,
	}
}

type dashboardProxyWindowMetrics struct {
	TotalRequests    int64
	Success          int64
	ClientError      int64
	ServerError      int64
	InternalError    int64
	AvgDurationMs    int64
	RequestBytes     int64
	ResponseBytes    int64
	TotalBytes       int64
	AvgRequestBytes  int64
	AvgResponseBytes int64
	MaxDurationMs    int64
	SlowRequests     int64
	CacheHits        int64
	CacheMisses      int64
	CacheBypasses    int64
	CacheStored      int64
	CacheStoreFailed int64
	CacheHitBytes    int64
	CacheStoredBytes int64
}

type dashboardAgentWindowMetrics struct {
	Samples          int64
	ReqSuccess       int64
	ReqClientError   int64
	ReqServerError   int64
	ReqInternalError int64
	BytesRx          int64
	BytesTx          int64
	AvgMemoryMb      int64
	MaxMemoryMb      int64
	AvgGoroutines    int64
	MaxGoroutines    int64
	AvgCpuPercent    float64
	MaxCpuPercent    float64
}

func dashboardWindowSummary(
	label string,
	sinceUnixMillis int64,
	proxy dashboardProxyWindowMetrics,
	agent dashboardAgentWindowMetrics,
) *p2pstreamv1.DashboardWindowSummary {
	return &p2pstreamv1.DashboardWindowSummary{
		Label:                 label,
		SinceUnixMillis:       sinceUnixMillis,
		ProxyRequests:         proxy.TotalRequests,
		ProxySuccess:          proxy.Success,
		ProxyClientError:      proxy.ClientError,
		ProxyServerError:      proxy.ServerError,
		ProxyInternalError:    proxy.InternalError,
		ProxyAvgDurationMs:    proxy.AvgDurationMs,
		ProxyRequestBytes:     uint64FromInt64(proxy.RequestBytes),
		ProxyResponseBytes:    uint64FromInt64(proxy.ResponseBytes),
		ProxyTotalBytes:       uint64FromInt64(proxy.TotalBytes),
		ProxyAvgRequestBytes:  uint64FromInt64(proxy.AvgRequestBytes),
		ProxyAvgResponseBytes: uint64FromInt64(proxy.AvgResponseBytes),
		ProxyMaxDurationMs:    proxy.MaxDurationMs,
		ProxySlowRequests:     proxy.SlowRequests,
		ProxyCacheHits:        proxy.CacheHits,
		ProxyCacheMisses:      proxy.CacheMisses,
		ProxyCacheBypasses:    proxy.CacheBypasses,
		ProxyCacheStored:      proxy.CacheStored,
		ProxyCacheStoreFailed: proxy.CacheStoreFailed,
		ProxyCacheHitBytes:    uint64FromInt64(proxy.CacheHitBytes),
		ProxyCacheStoredBytes: uint64FromInt64(proxy.CacheStoredBytes),
		AgentSamples:          agent.Samples,
		AgentReqSuccess:       agent.ReqSuccess,
		AgentReqClientError:   agent.ReqClientError,
		AgentReqServerError:   agent.ReqServerError,
		AgentReqInternalError: agent.ReqInternalError,
		AgentBytesReceived:    uint64FromInt64(agent.BytesRx),
		AgentBytesSent:        uint64FromInt64(agent.BytesTx),
		AgentAvgMemoryMb:      agent.AvgMemoryMb,
		AgentMaxMemoryMb:      agent.MaxMemoryMb,
		AgentAvgGoroutines:    agent.AvgGoroutines,
		AgentMaxGoroutines:    agent.MaxGoroutines,
		AgentAvgCpuPercent:    agent.AvgCpuPercent,
		AgentMaxCpuPercent:    agent.MaxCpuPercent,
	}
}

func dashboardProxyWindowFromRaw(row db.GetProxyRequestSummarySinceRow) dashboardProxyWindowMetrics {
	return dashboardProxyWindowMetrics{
		TotalRequests:    row.TotalRequests,
		Success:          row.Success,
		ClientError:      row.ClientError,
		ServerError:      row.ServerError,
		InternalError:    row.InternalError,
		AvgDurationMs:    row.AvgDurationMs,
		RequestBytes:     row.RequestBytes,
		ResponseBytes:    row.ResponseBytes,
		TotalBytes:       row.TotalBytes,
		AvgRequestBytes:  row.AvgRequestBytes,
		AvgResponseBytes: row.AvgResponseBytes,
		MaxDurationMs:    row.MaxDurationMs,
		SlowRequests:     row.SlowRequests,
		CacheHits:        row.CacheHits,
		CacheMisses:      row.CacheMisses,
		CacheBypasses:    row.CacheBypasses,
		CacheStored:      row.CacheStored,
		CacheStoreFailed: row.CacheStoreFailed,
		CacheHitBytes:    row.CacheHitBytes,
		CacheStoredBytes: row.CacheStoredBytes,
	}
}

func dashboardProxyWindowFromRollup(row db.GetProxyRequestRollupSummarySinceRow) dashboardProxyWindowMetrics {
	return dashboardProxyWindowMetrics{
		TotalRequests:    row.TotalRequests,
		Success:          row.Success,
		ClientError:      row.ClientError,
		ServerError:      row.ServerError,
		InternalError:    row.InternalError,
		AvgDurationMs:    row.AvgDurationMs,
		RequestBytes:     row.RequestBytes,
		ResponseBytes:    row.ResponseBytes,
		TotalBytes:       row.TotalBytes,
		AvgRequestBytes:  row.AvgRequestBytes,
		AvgResponseBytes: row.AvgResponseBytes,
		MaxDurationMs:    row.MaxDurationMs,
		SlowRequests:     row.SlowRequests,
		CacheHits:        row.CacheHits,
		CacheMisses:      row.CacheMisses,
		CacheBypasses:    row.CacheBypasses,
		CacheStored:      row.CacheStored,
		CacheStoreFailed: row.CacheStoreFailed,
		CacheHitBytes:    row.CacheHitBytes,
		CacheStoredBytes: row.CacheStoredBytes,
	}
}

func dashboardAgentWindowFromRaw(row db.GetAgentStatsSummarySinceRow) dashboardAgentWindowMetrics {
	return dashboardAgentWindowMetrics{
		Samples:          row.Samples,
		ReqSuccess:       row.ReqSuccess,
		ReqClientError:   row.ReqClientError,
		ReqServerError:   row.ReqServerError,
		ReqInternalError: row.ReqInternalError,
		BytesRx:          row.BytesRx,
		BytesTx:          row.BytesTx,
		AvgMemoryMb:      row.AvgMemoryMb,
		MaxMemoryMb:      row.MaxMemoryMb,
		AvgGoroutines:    row.AvgGoroutines,
		MaxGoroutines:    row.MaxGoroutines,
		AvgCpuPercent:    row.AvgCpuPercent,
		MaxCpuPercent:    row.MaxCpuPercent,
	}
}

func dashboardAgentWindowFromRollup(row db.GetAgentStatsRollupSummarySinceRow) dashboardAgentWindowMetrics {
	return dashboardAgentWindowMetrics{
		Samples:          row.Samples,
		ReqSuccess:       row.ReqSuccess,
		ReqClientError:   row.ReqClientError,
		ReqServerError:   row.ReqServerError,
		ReqInternalError: row.ReqInternalError,
		BytesRx:          row.BytesRx,
		BytesTx:          row.BytesTx,
		AvgMemoryMb:      row.AvgMemoryMb,
		MaxMemoryMb:      row.MaxMemoryMb,
		AvgGoroutines:    row.AvgGoroutines,
		MaxGoroutines:    row.MaxGoroutines,
		AvgCpuPercent:    row.AvgCpuPercent,
		MaxCpuPercent:    row.MaxCpuPercent,
	}
}

func (a *App) recordProxyRequestEvent(ctx context.Context, statusCode int, duration time.Duration, errorKind string) {
	a.recordProxyRequestEventWithIDs(ctx, statusCode, duration, errorKind, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, 0, 0)
}

func (a *App) recordProxyRequestEventWithIDs(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	routeID sql.NullInt64,
	agentID sql.NullInt64,
	requestBytes uint64,
	responseBytes uint64,
) {
	a.recordProxyRequestEventWithPolicyIDs(ctx, statusCode, duration, errorKind, listenerID, routeID, sql.NullInt64{}, "", agentID, requestBytes, responseBytes)
}

func (a *App) recordProxyRequestEventWithPolicyIDs(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	routeID sql.NullInt64,
	wafRuleID sql.NullInt64,
	wafAction string,
	agentID sql.NullInt64,
	requestBytes uint64,
	responseBytes uint64,
) {
	a.recordProxyRequestEventWithCache(ctx, statusCode, duration, errorKind, listenerID, routeID, wafRuleID, wafAction, agentID, sql.NullInt64{}, "", 0, requestBytes, responseBytes)
}

func (a *App) recordProxyRequestEventWithCache(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	routeID sql.NullInt64,
	wafRuleID sql.NullInt64,
	wafAction string,
	agentID sql.NullInt64,
	cacheRuleID sql.NullInt64,
	cacheStatus string,
	cacheBytes uint64,
	requestBytes uint64,
	responseBytes uint64,
) {
	a.recordProxyRequestEventWithRouteTargetCache(ctx, statusCode, duration, errorKind, listenerID, routeID, sql.NullInt64{}, wafRuleID, wafAction, agentID, cacheRuleID, cacheStatus, cacheBytes, requestBytes, responseBytes)
}

func (a *App) recordProxyRequestEventWithRouteTargetCache(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	routeID sql.NullInt64,
	routeTargetID sql.NullInt64,
	wafRuleID sql.NullInt64,
	wafAction string,
	agentID sql.NullInt64,
	cacheRuleID sql.NullInt64,
	cacheStatus string,
	cacheBytes uint64,
	requestBytes uint64,
	responseBytes uint64,
) {
	if a.DB == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	if statusCode == 0 {
		statusCode = http.StatusInternalServerError
	}

	occurredAt := time.Now().UTC()
	if err := a.insertProxyRequestEventWithRollups(ctx, db.InsertProxyRequestEventAtParams{
		OccurredAt:    occurredAt,
		StatusCode:    int64(statusCode),
		DurationMs:    duration.Milliseconds(),
		ErrorKind:     errorKind,
		ListenerID:    listenerID,
		RouteID:       routeID,
		RouteTargetID: routeTargetID,
		WafRuleID:     wafRuleID,
		WafAction:     wafAction,
		AgentID:       agentID,
		RequestBytes:  int64FromUint64(requestBytes),
		ResponseBytes: int64FromUint64(responseBytes),
		CacheRuleID:   cacheRuleID,
		CacheStatus:   cacheStatus,
		CacheBytes:    int64FromUint64(cacheBytes),
	}); err != nil {
		log.Warn().Err(err).Msg("Failed to record proxy request event")
	}
}

func dashboardListenerSummaries(rows []db.ListTopProxyListenersSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_LISTENER,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRouteSummaries(rows []db.ListTopProxyRoutesSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ROUTE,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRouteTargetSummaries(rows []db.ListTopProxyRouteTargetsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ROUTE_TARGET,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardAgentSummaries(rows []db.ListTopProxyAgentsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_AGENT,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardErrorKindSummaries(rows []db.ListTopProxyErrorKindsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ERROR_KIND,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardStatusClassSummaries(rows []db.ListProxyStatusClassesSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_STATUS_CLASS,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRollupListenerSummaries(rows []db.ListTopProxyListenersRollupsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_LISTENER,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRollupRouteSummaries(rows []db.ListTopProxyRoutesRollupsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ROUTE,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRollupRouteTargetSummaries(rows []db.ListTopProxyRouteTargetsRollupsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ROUTE_TARGET,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRollupAgentSummaries(rows []db.ListTopProxyAgentsRollupsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_AGENT,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRollupErrorKindSummaries(rows []db.ListTopProxyErrorKindsRollupsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ERROR_KIND,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardRollupStatusClassSummaries(rows []db.ListProxyStatusClassesRollupsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_STATUS_CLASS,
			row.ID,
			row.Label,
			row.Requests,
			row.Success,
			row.ClientError,
			row.ServerError,
			row.InternalError,
			row.AvgDurationMs,
			row.RequestBytes,
			row.ResponseBytes,
		))
	}
	return items
}

func dashboardDimensionSummary(
	dimension p2pstreamv1.DashboardProxyDimension,
	id int64,
	label any,
	requests int64,
	success int64,
	clientError int64,
	serverError int64,
	internalError int64,
	avgDurationMs int64,
	requestBytes int64,
	responseBytes int64,
) *p2pstreamv1.DashboardProxyDimensionSummary {
	return &p2pstreamv1.DashboardProxyDimensionSummary{
		Dimension:     dimension,
		Id:            id,
		Label:         dashboardLabelString(label),
		Requests:      requests,
		Success:       success,
		ClientError:   clientError,
		ServerError:   serverError,
		InternalError: internalError,
		AvgDurationMs: avgDurationMs,
		RequestBytes:  uint64FromInt64(requestBytes),
		ResponseBytes: uint64FromInt64(responseBytes),
	}
}

func dashboardTrafficBuckets(rows []db.ListProxyTrafficBucketsSinceRow) []*p2pstreamv1.DashboardTrafficBucket {
	items := make([]*p2pstreamv1.DashboardTrafficBucket, 0, len(rows))
	for _, row := range rows {
		items = append(items, &p2pstreamv1.DashboardTrafficBucket{
			BucketUnixMillis: row.BucketUnixMillis,
			Requests:         row.Requests,
			Success:          row.Success,
			ClientError:      row.ClientError,
			ServerError:      row.ServerError,
			InternalError:    row.InternalError,
			RequestBytes:     uint64FromInt64(row.RequestBytes),
			ResponseBytes:    uint64FromInt64(row.ResponseBytes),
			AvgDurationMs:    row.AvgDurationMs,
		})
	}
	return items
}

func dashboardRollupTrafficBuckets(rows []db.ListProxyTrafficBucketRollupsSinceRow) []*p2pstreamv1.DashboardTrafficBucket {
	items := make([]*p2pstreamv1.DashboardTrafficBucket, 0, len(rows))
	for _, row := range rows {
		items = append(items, &p2pstreamv1.DashboardTrafficBucket{
			BucketUnixMillis: row.BucketUnixMillis,
			Requests:         row.Requests,
			Success:          row.Success,
			ClientError:      row.ClientError,
			ServerError:      row.ServerError,
			InternalError:    row.InternalError,
			RequestBytes:     uint64FromInt64(row.RequestBytes),
			ResponseBytes:    uint64FromInt64(row.ResponseBytes),
			AvgDurationMs:    row.AvgDurationMs,
		})
	}
	return items
}

func dashboardLabelString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

func uint64FromInt64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func int64FromUint64(value uint64) int64 {
	maxInt64 := uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		return int64(maxInt64)
	}
	return int64(value)
}

func (a *App) cleanupObservability(ctx context.Context, now time.Time) {
	if a.DB == nil {
		return
	}

	a.observabilityMu.Lock()
	if !a.observabilityLastCleanup.IsZero() && now.Sub(a.observabilityLastCleanup) < observabilityCleanupInterval {
		a.observabilityMu.Unlock()
		return
	}
	a.observabilityLastCleanup = now
	a.observabilityMu.Unlock()

	cutoff := now.AddDate(0, 0, -a.observabilityRetentionDays())
	cutoffBucketUnixMillis := rollupBucketUnixMillis(cutoff)
	if err := a.DB.DeleteProxyRequestRollupsBefore(ctx, cutoffBucketUnixMillis); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old proxy request rollups")
	}
	if err := a.DB.DeleteProxyRequestTupleRollupsBefore(ctx, cutoffBucketUnixMillis); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old proxy request tuple rollups")
	}
	if err := a.DB.DeleteAgentStatRollupsBefore(ctx, cutoffBucketUnixMillis); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old agent stat rollups")
	}
	if err := a.DB.DeleteProxyRequestEventsBefore(ctx, cutoff); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old proxy request events")
	}
	if err := a.DB.DeleteAgentStatsBefore(ctx, cutoff); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old agent stats")
	}
	if err := a.DB.DeleteDisconnectedConnectionsBefore(ctx, sql.NullTime{Time: cutoff, Valid: true}); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old disconnected agent connections")
	}

	maxRows := a.observabilityMaxRows()
	if maxRows <= 0 {
		return
	}
	ready, err := a.observabilityRollupsReady(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to check observability rollup readiness for row cap")
		return
	}
	if !ready {
		return
	}

	for i := 0; i < observabilityRowCapDeleteMaxBatches; i++ {
		deleted, err := a.DB.DeleteOldestProxyRequestEventsOverLimit(ctx, db.DeleteOldestProxyRequestEventsOverLimitParams{
			Offset:      maxRows,
			DeleteLimit: observabilityRowCapDeleteBatchRows,
		})
		if err != nil {
			log.Warn().Err(err).Msg("Failed to enforce proxy request event row cap")
			break
		}
		if deleted < observabilityRowCapDeleteBatchRows {
			break
		}
	}
	for i := 0; i < observabilityRowCapDeleteMaxBatches; i++ {
		deleted, err := a.DB.DeleteOldestAgentStatsOverLimit(ctx, db.DeleteOldestAgentStatsOverLimitParams{
			Offset:      maxRows,
			DeleteLimit: observabilityRowCapDeleteBatchRows,
		})
		if err != nil {
			log.Warn().Err(err).Msg("Failed to enforce agent stat row cap")
			break
		}
		if deleted < observabilityRowCapDeleteBatchRows {
			break
		}
	}
}

func (a *App) observabilityRetentionDays() int {
	if a.Config == nil || a.Config.ObservabilityRetentionDays < 1 {
		return 30
	}
	return a.Config.ObservabilityRetentionDays
}

func (a *App) observabilityMaxRows() int64 {
	if a.Config == nil {
		return 0
	}
	return a.Config.ObservabilityMaxRows
}

func (a *App) agentConnectionSummary(ctx context.Context, now time.Time) (*p2pstreamv1.AgentConnectionSummary, error) {
	since := now.AddDate(0, 0, -a.observabilityRetentionDays())
	summary, err := a.DB.GetConnectionSummarySince(ctx, since)
	if err != nil {
		return nil, err
	}

	resp := &p2pstreamv1.AgentConnectionSummary{
		Connected:                    a.AgentHub.connectedCount() > 0,
		TotalConnections:             summary.TotalConnections,
		LastConnectedAtUnixMillis:    sqliteTimeUnixMillis(summary.LastConnectedAt),
		LastDisconnectedAtUnixMillis: sqliteTimeUnixMillis(summary.LastDisconnectedAt),
	}

	active, err := a.DB.GetActiveConnection(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return resp, nil
		}
		return nil, err
	}
	resp.ActiveConnectedAtUnixMillis = active.ConnectedAt.UnixMilli()
	return resp, nil
}

type dashboardTimeInterval struct {
	start time.Time
	end   time.Time
}

type dashboardAgentUptimeState struct {
	summary               *p2pstreamv1.AgentUptimeSummary
	observedSince         time.Time
	observedUntil         time.Time
	intervals             []dashboardTimeInterval
	currentConnectedAt    time.Time
	hasCurrentConnectedAt bool
	lastConnectedAt       sql.NullTime
}

func (a *App) agentUptimeDashboard(ctx context.Context, now time.Time) ([]*p2pstreamv1.AgentUptimeSummary, []*p2pstreamv1.AgentConnectionSession, error) {
	summaries, err := a.agentUptimeSummaries(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	recent, err := a.recentAgentConnectionSessions(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	return summaries, recent, nil
}

func (a *App) agentUptimeSummaries(ctx context.Context, now time.Time) ([]*p2pstreamv1.AgentUptimeSummary, error) {
	agents, err := a.DB.ListAgents(ctx)
	if err != nil {
		return nil, err
	}

	retentionSince := now.AddDate(0, 0, -a.observabilityRetentionDays()).UTC()
	now = now.UTC()
	connectedAgents := map[int64]*AgentConn{}
	if a.AgentHub != nil {
		connectedAgents = a.AgentHub.connectedIDs()
	}

	states := make(map[int64]*dashboardAgentUptimeState, len(agents))
	summaries := make([]*p2pstreamv1.AgentUptimeSummary, 0, len(agents))
	for _, agent := range agents {
		observedSince := maxTime(retentionSince, agent.CreatedAt.UTC())
		if observedSince.After(now) {
			observedSince = now
		}
		_, connected := connectedAgents[agent.ID]
		summary := &p2pstreamv1.AgentUptimeSummary{
			AgentId:                      agent.ID,
			AgentPublicId:                agent.PublicID,
			AgentName:                    agent.Name,
			Enabled:                      agent.Enabled != 0,
			Connected:                    connected,
			ObservedSinceUnixMillis:      observedSince.UnixMilli(),
			ObservedUntilUnixMillis:      now.UnixMilli(),
			LastConnectedAtUnixMillis:    nullTimeUnixMillis(agent.LastConnectedAt),
			LastDisconnectedAtUnixMillis: nullTimeUnixMillis(agent.LastDisconnectedAt),
		}
		states[agent.ID] = &dashboardAgentUptimeState{
			summary:         summary,
			observedSince:   observedSince,
			observedUntil:   now,
			lastConnectedAt: agent.LastConnectedAt,
		}
		summaries = append(summaries, summary)
	}

	connections, err := a.DB.ListConnectionsSince(ctx, db.ListConnectionsSinceParams{
		ConnectedAt:    retentionSince,
		DisconnectedAt: sql.NullTime{Time: retentionSince, Valid: true},
	})
	if err != nil {
		return nil, err
	}

	for _, row := range connections {
		if !row.AgentID.Valid {
			continue
		}
		state := states[row.AgentID.Int64]
		if state == nil {
			continue
		}

		end := now
		if row.DisconnectedAt.Valid {
			end = row.DisconnectedAt.Time.UTC()
		}
		if end.After(state.observedUntil) {
			end = state.observedUntil
		}
		start := maxTime(row.ConnectedAt.UTC(), state.observedSince)
		if end.After(start) {
			state.intervals = append(state.intervals, dashboardTimeInterval{start: start, end: end})
			state.summary.ConnectionCount++
		}
		if row.DisconnectedAt.Valid {
			disconnectedAt := row.DisconnectedAt.Time.UTC()
			if !disconnectedAt.Before(state.observedSince) && !disconnectedAt.After(state.observedUntil) {
				state.summary.DisconnectCount++
			}
		}
		if !row.DisconnectedAt.Valid {
			conn := connectedAgents[row.AgentID.Int64]
			if conn != nil && (conn.ConnectionDBID == 0 || conn.ConnectionDBID == row.ID || !state.hasCurrentConnectedAt || row.ConnectedAt.After(state.currentConnectedAt)) {
				state.currentConnectedAt = row.ConnectedAt.UTC()
				state.hasCurrentConnectedAt = true
			}
		}
	}

	for _, state := range states {
		summary := state.summary
		observedDurationMillis := dashboardDurationMillis(state.observedSince, state.observedUntil)
		summary.UptimeMillis = sumMergedIntervalMillis(state.intervals)
		if summary.UptimeMillis > observedDurationMillis {
			summary.UptimeMillis = observedDurationMillis
		}
		summary.DowntimeMillis = observedDurationMillis - summary.UptimeMillis
		if summary.DowntimeMillis < 0 {
			summary.DowntimeMillis = 0
		}
		if observedDurationMillis > 0 {
			summary.UptimePercent = float64(summary.UptimeMillis) / float64(observedDurationMillis)
		}

		conn := connectedAgents[summary.AgentId]
		summary.Connected = conn != nil
		if conn != nil {
			if !state.hasCurrentConnectedAt && !conn.ConnectedAt.IsZero() {
				state.currentConnectedAt = conn.ConnectedAt.UTC()
				state.hasCurrentConnectedAt = true
			}
			if !state.hasCurrentConnectedAt && state.lastConnectedAt.Valid {
				state.currentConnectedAt = state.lastConnectedAt.Time.UTC()
				state.hasCurrentConnectedAt = true
			}
			if state.hasCurrentConnectedAt {
				summary.CurrentConnectedAtUnixMillis = state.currentConnectedAt.UnixMilli()
				summary.CurrentUptimeMillis = dashboardDurationMillis(state.currentConnectedAt, now)
			}
			continue
		}

		summary.CurrentConnectedAtUnixMillis = 0
		summary.CurrentUptimeMillis = 0
		if summary.LastDisconnectedAtUnixMillis > 0 {
			offlineSince := time.UnixMilli(summary.LastDisconnectedAtUnixMillis).UTC()
			summary.CurrentOfflineSinceUnixMillis = summary.LastDisconnectedAtUnixMillis
			summary.CurrentDowntimeMillis = dashboardDurationMillis(offlineSince, now)
		}
	}

	return summaries, nil
}

func (a *App) recentAgentConnectionSessions(ctx context.Context, now time.Time) ([]*p2pstreamv1.AgentConnectionSession, error) {
	rows, err := a.DB.ListRecentConnections(ctx, 50)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	sessions := make([]*p2pstreamv1.AgentConnectionSession, 0, len(rows))
	for _, row := range rows {
		agentID := int64(0)
		if row.AgentID.Valid {
			agentID = row.AgentID.Int64
		}
		end := now
		disconnectedAtUnixMillis := int64(0)
		active := !row.DisconnectedAt.Valid
		if row.DisconnectedAt.Valid {
			end = row.DisconnectedAt.Time.UTC()
			disconnectedAtUnixMillis = end.UnixMilli()
		}
		sessions = append(sessions, &p2pstreamv1.AgentConnectionSession{
			Id:                       row.ID,
			AgentId:                  agentID,
			AgentPublicId:            row.AgentPublicID,
			AgentName:                row.AgentName,
			ConnectedAtUnixMillis:    row.ConnectedAt.UTC().UnixMilli(),
			DisconnectedAtUnixMillis: disconnectedAtUnixMillis,
			DurationMillis:           dashboardDurationMillis(row.ConnectedAt.UTC(), end),
			Active:                   active,
		})
	}
	return sessions, nil
}

func sumMergedIntervalMillis(intervals []dashboardTimeInterval) int64 {
	if len(intervals) == 0 {
		return 0
	}
	current := intervals[0]
	total := int64(0)
	for _, interval := range intervals[1:] {
		if interval.start.After(current.end) {
			total += dashboardDurationMillis(current.start, current.end)
			current = interval
			continue
		}
		if interval.end.After(current.end) {
			current.end = interval.end
		}
	}
	total += dashboardDurationMillis(current.start, current.end)
	return total
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func dashboardDurationMillis(start, end time.Time) int64 {
	if end.Before(start) || end.Equal(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func sqliteTimeUnixMillis(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case sql.NullTime:
		return nullTimeUnixMillis(v)
	case time.Time:
		if v.IsZero() {
			return 0
		}
		return v.UnixMilli()
	case string:
		return parseSQLiteTimeUnixMillis(v)
	case []byte:
		return parseSQLiteTimeUnixMillis(string(v))
	default:
		return 0
	}
}

func parseSQLiteTimeUnixMillis(value string) int64 {
	if value == "" {
		return 0
	}

	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}
