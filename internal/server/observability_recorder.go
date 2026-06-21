package server

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"p2pstream/internal/db"
)

type proxyRequestEvent struct {
	StatusCode    int
	Duration      time.Duration
	ErrorKind     string
	ListenerID    sql.NullInt64
	RouteID       sql.NullInt64
	RouteTargetID sql.NullInt64
	WafRuleID     sql.NullInt64
	WafAction     string
	AgentID       sql.NullInt64
	CacheRuleID   sql.NullInt64
	CacheStatus   string
	CacheBytes    uint64
	RequestBytes  uint64
	ResponseBytes uint64
	Context       proxyRequestContext
}

type observabilityRecorder struct {
	app *App
}

func newObservabilityRecorder(app *App) *observabilityRecorder {
	return &observabilityRecorder{app: app}
}

func (a *App) observabilityRecorderService() *observabilityRecorder {
	if a == nil {
		return newObservabilityRecorder(nil)
	}
	if a.observabilityRecorder != nil {
		return a.observabilityRecorder
	}
	a.observabilityRecorder = newObservabilityRecorder(a)
	return a.observabilityRecorder
}

func (r *observabilityRecorder) recordProxyRequestEvent(ctx context.Context, event proxyRequestEvent) {
	a := r.app
	if a == nil || a.DB == nil {
		return
	}
	if event.Duration < 0 {
		event.Duration = 0
	}
	if event.StatusCode == 0 {
		event.StatusCode = http.StatusInternalServerError
	}

	occurredAt := time.Now().UTC()
	if err := a.insertProxyRequestEventWithRollups(ctx, db.InsertProxyRequestEventAtParams{
		OccurredAt:    occurredAt,
		StatusCode:    int64(event.StatusCode),
		DurationMs:    event.Duration.Milliseconds(),
		ErrorKind:     event.ErrorKind,
		Method:        event.Context.Method,
		Host:          event.Context.Host,
		PathPrefix:    event.Context.PathPrefix,
		ListenerID:    event.ListenerID,
		RouteID:       event.RouteID,
		RouteTargetID: event.RouteTargetID,
		WafRuleID:     event.WafRuleID,
		WafAction:     event.WafAction,
		AgentID:       event.AgentID,
		RequestBytes:  int64FromUint64(event.RequestBytes),
		ResponseBytes: int64FromUint64(event.ResponseBytes),
		CacheRuleID:   event.CacheRuleID,
		CacheStatus:   event.CacheStatus,
		CacheBytes:    int64FromUint64(event.CacheBytes),
	}); err != nil {
		log.Warn().Err(err).Msg("Failed to record proxy request event")
	}
}

func (r *observabilityRecorder) cleanup(ctx context.Context, now time.Time) {
	a := r.app
	if a == nil || a.DB == nil {
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
	if err := a.DB.DeleteProxyRequestStatusRollupsBefore(ctx, cutoffBucketUnixMillis); err != nil {
		log.Warn().Err(err).Msg("Failed to clean up old proxy request status rollups")
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
