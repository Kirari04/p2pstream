package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
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
			AgentSamples:          agentSummary.Samples,
			AgentReqSuccess:       agentSummary.ReqSuccess,
			AgentReqClientError:   agentSummary.ReqClientError,
			AgentReqServerError:   agentSummary.ReqServerError,
			AgentReqInternalError: agentSummary.ReqInternalError,
			AgentBytesReceived:    uint64(agentSummary.BytesRx),
			AgentBytesSent:        uint64(agentSummary.BytesTx),
			AgentAvgMemoryMb:      agentSummary.AvgMemoryMb,
			AgentMaxMemoryMb:      agentSummary.MaxMemoryMb,
			AgentAvgGoroutines:    agentSummary.AvgGoroutines,
			AgentMaxGoroutines:    agentSummary.MaxGoroutines,
		})
	}

	agentConnections, err := a.agentConnectionSummary(ctx, now)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &p2pstreamv1.GetDashboardResponse{
		Status:                a.statusResponse(),
		Windows:               windows,
		AgentConnections:      agentConnections,
		RetentionDays:         int64(a.observabilityRetentionDays()),
		GeneratedAtUnixMillis: now.UnixMilli(),
	}
	return connect.NewResponse(resp), nil
}

func (a *App) recordProxyRequestEvent(ctx context.Context, statusCode int, duration time.Duration, errorKind string) {
	a.recordProxyRequestEventWithIDs(ctx, statusCode, duration, errorKind, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
}

func (a *App) recordProxyRequestEventWithIDs(
	ctx context.Context,
	statusCode int,
	duration time.Duration,
	errorKind string,
	listenerID sql.NullInt64,
	backendID sql.NullInt64,
	routeID sql.NullInt64,
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
		StatusCode: int64(statusCode),
		DurationMs: duration.Milliseconds(),
		ErrorKind:  errorKind,
		ListenerID: listenerID,
		BackendID:  backendID,
		RouteID:    routeID,
	}); err != nil {
		log.Warn().Err(err).Msg("Failed to record proxy request event")
	}
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
		Connected:                    a.ActiveAgent.Load() != nil,
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
