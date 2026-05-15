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
	a.cleanupObservability(ctx, now)

	windows := make([]*p2pstreamv1.DashboardWindowSummary, 0, len(dashboardWindows))
	for _, window := range dashboardWindows {
		since := now.Add(-window.Since)

		proxySummary, err := a.DB.GetProxyRequestSummarySince(ctx, since)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		agentSummary, err := a.DB.GetAgentStatsSummarySince(ctx, since)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		windows = append(windows, &p2pstreamv1.DashboardWindowSummary{
			Label:                 window.Label,
			SinceUnixMillis:       since.UnixMilli(),
			ProxyRequests:         proxySummary.TotalRequests,
			ProxySuccess:          proxySummary.Success,
			ProxyClientError:      proxySummary.ClientError,
			ProxyServerError:      proxySummary.ServerError,
			ProxyInternalError:    proxySummary.InternalError,
			ProxyAvgDurationMs:    proxySummary.AvgDurationMs,
			ProxyRequestBytes:     uint64FromInt64(proxySummary.RequestBytes),
			ProxyResponseBytes:    uint64FromInt64(proxySummary.ResponseBytes),
			ProxyTotalBytes:       uint64FromInt64(proxySummary.TotalBytes),
			ProxyAvgRequestBytes:  uint64FromInt64(proxySummary.AvgRequestBytes),
			ProxyAvgResponseBytes: uint64FromInt64(proxySummary.AvgResponseBytes),
			ProxyMaxDurationMs:    proxySummary.MaxDurationMs,
			ProxySlowRequests:     proxySummary.SlowRequests,
			ProxyCacheHits:        proxySummary.CacheHits,
			ProxyCacheMisses:      proxySummary.CacheMisses,
			ProxyCacheBypasses:    proxySummary.CacheBypasses,
			ProxyCacheStored:      proxySummary.CacheStored,
			ProxyCacheStoreFailed: proxySummary.CacheStoreFailed,
			ProxyCacheHitBytes:    uint64FromInt64(proxySummary.CacheHitBytes),
			ProxyCacheStoredBytes: uint64FromInt64(proxySummary.CacheStoredBytes),
			AgentSamples:          agentSummary.Samples,
			AgentReqSuccess:       agentSummary.ReqSuccess,
			AgentReqClientError:   agentSummary.ReqClientError,
			AgentReqServerError:   agentSummary.ReqServerError,
			AgentReqInternalError: agentSummary.ReqInternalError,
			AgentBytesReceived:    uint64FromInt64(agentSummary.BytesRx),
			AgentBytesSent:        uint64FromInt64(agentSummary.BytesTx),
			AgentAvgMemoryMb:      agentSummary.AvgMemoryMb,
			AgentMaxMemoryMb:      agentSummary.MaxMemoryMb,
			AgentAvgGoroutines:    agentSummary.AvgGoroutines,
			AgentMaxGoroutines:    agentSummary.MaxGoroutines,
			AgentAvgCpuPercent:    agentSummary.AvgCpuPercent,
			AgentMaxCpuPercent:    agentSummary.MaxCpuPercent,
		})
	}

	agentConnections, err := a.agentConnectionSummary(ctx, now)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	topSince := now.Add(-dashboardTopWindow)
	topListenerRows, err := a.DB.ListTopProxyListenersSince(ctx, topSince)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	topBackendRows, err := a.DB.ListTopProxyBackendsSince(ctx, topSince)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	topRouteRows, err := a.DB.ListTopProxyRoutesSince(ctx, topSince)
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
		Since:         now.Add(-dashboardTrafficBucketWindow),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &p2pstreamv1.GetDashboardResponse{
		Status:                a.statusResponse(),
		Windows:               windows,
		AgentConnections:      agentConnections,
		RetentionDays:         int64(a.observabilityRetentionDays()),
		GeneratedAtUnixMillis: now.UnixMilli(),
		TopListeners:          dashboardListenerSummaries(topListenerRows),
		TopBackends:           dashboardBackendSummaries(topBackendRows),
		TopRoutes:             dashboardRouteSummaries(topRouteRows),
		TopAgents:             dashboardAgentSummaries(topAgentRows),
		TopErrorKinds:         dashboardErrorKindSummaries(topErrorRows),
		StatusClasses:         dashboardStatusClassSummaries(statusClassRows),
		TrafficBuckets:        dashboardTrafficBuckets(trafficBucketRows),
		ManagementSecurity:    a.managementSecurity(),
	}
	return connect.NewResponse(resp), nil
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

func (a *App) recordProxyRequestEvent(ctx context.Context, statusCode int, duration time.Duration, errorKind string) {
	a.recordProxyRequestEventWithIDs(ctx, statusCode, duration, errorKind, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, 0, 0)
}

func (a *App) recordProxyRequestEventWithIDs(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	backendID sql.NullInt64,
	routeID sql.NullInt64,
	agentID sql.NullInt64,
	requestBytes uint64,
	responseBytes uint64,
) {
	a.recordProxyRequestEventWithPolicyIDs(ctx, statusCode, duration, errorKind, listenerID, backendID, routeID, sql.NullInt64{}, "", agentID, requestBytes, responseBytes)
}

func (a *App) recordProxyRequestEventWithPolicyIDs(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	backendID sql.NullInt64,
	routeID sql.NullInt64,
	wafRuleID sql.NullInt64,
	wafAction string,
	agentID sql.NullInt64,
	requestBytes uint64,
	responseBytes uint64,
) {
	a.recordProxyRequestEventWithCache(ctx, statusCode, duration, errorKind, listenerID, backendID, routeID, wafRuleID, wafAction, agentID, sql.NullInt64{}, "", 0, requestBytes, responseBytes)
}

func (a *App) recordProxyRequestEventWithCache(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	backendID sql.NullInt64,
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
	if a.DB == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	if statusCode == 0 {
		statusCode = http.StatusInternalServerError
	}

	if err := a.DB.InsertProxyRequestEvent(ctx, db.InsertProxyRequestEventParams{
		StatusCode:    int64(statusCode),
		DurationMs:    duration.Milliseconds(),
		ErrorKind:     errorKind,
		ListenerID:    listenerID,
		BackendID:     backendID,
		RouteID:       routeID,
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

func dashboardBackendSummaries(rows []db.ListTopProxyBackendsSinceRow) []*p2pstreamv1.DashboardProxyDimensionSummary {
	items := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, dashboardDimensionSummary(
			p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_BACKEND,
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
	if !a.observabilityLastCleanup.IsZero() && now.Sub(a.observabilityLastCleanup) < time.Hour {
		a.observabilityMu.Unlock()
		return
	}
	a.observabilityLastCleanup = now
	a.observabilityMu.Unlock()

	cutoff := now.AddDate(0, 0, -a.observabilityRetentionDays())
	if err := a.DB.DeleteProxyRequestEventsBefore(ctx, cutoff); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old proxy request events")
	}
	if err := a.DB.DeleteAgentStatsBefore(ctx, cutoff); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old agent stats")
	}
	if err := a.DB.DeleteDisconnectedConnectionsBefore(ctx, sql.NullTime{Time: cutoff, Valid: true}); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old disconnected agent connections")
	}
}

func (a *App) observabilityRetentionDays() int {
	if a.Config == nil || a.Config.ObservabilityRetentionDays < 1 {
		return 30
	}
	return a.Config.ObservabilityRetentionDays
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

func sqliteTimeUnixMillis(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
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
