package server

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/rs/zerolog/log"

	"p2pstream/internal/db"
)

const (
	observabilityRollupBackfillBatchRows = int64(10000)
	observabilityRollupBackfillInterval  = 200 * time.Millisecond
	observabilityRowCapDeleteBatchRows   = int64(10000)
	observabilityRowCapDeleteMaxBatches  = 10
)

func (a *App) insertProxyRequestEventWithRollups(ctx context.Context, event db.InsertProxyRequestEventAtParams) error {
	return a.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, []db.InsertProxyRequestEventAtParams{event}, nil)
}

func (a *App) insertProxyRequestEventsWithRollupsAndCacheTouches(ctx context.Context, events []db.InsertProxyRequestEventAtParams, cacheTouches []db.TouchPublicCacheEntryParams) error {
	if a.DB == nil {
		return nil
	}
	if len(events) == 0 && len(cacheTouches) == 0 {
		return nil
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	totals := make(map[int64]db.UpsertProxyRequestRollupMinuteParams)
	tuples := make(map[proxyRequestTupleRollupKey]db.UpsertProxyRequestTupleRollupMinuteParams)
	statuses := make(map[proxyRequestStatusRollupKey]db.UpsertProxyRequestStatusRollupMinuteParams)
	retries := make(map[proxyRetryRollupKey]db.UpsertProxyRetryRollupMinuteParams)
	for _, event := range events {
		if _, err := qtx.InsertProxyRequestEventAt(ctx, event); err != nil {
			return err
		}
		total, tuple, status := proxyRequestRollupParams(event)
		mergeProxyRequestRollup(totals, total)
		mergeProxyRequestTupleRollup(tuples, tuple)
		mergeProxyRequestStatusRollup(statuses, status)
		if retry, ok := proxyRetryRollupParams(event); ok {
			mergeProxyRetryRollup(retries, retry)
		}
	}
	for _, total := range totals {
		if err := qtx.UpsertProxyRequestRollupMinute(ctx, total); err != nil {
			return err
		}
	}
	for _, tuple := range tuples {
		if err := qtx.UpsertProxyRequestTupleRollupMinute(ctx, tuple); err != nil {
			return err
		}
	}
	for _, status := range statuses {
		if err := qtx.UpsertProxyRequestStatusRollupMinute(ctx, status); err != nil {
			return err
		}
	}
	for _, retry := range retries {
		if err := qtx.UpsertProxyRetryRollupMinute(ctx, retry); err != nil {
			return err
		}
	}
	for _, touch := range cacheTouches {
		if touch.KeyDigest == "" || touch.StoredAt.IsZero() || touch.LastAccessedAt.IsZero() || touch.HitCount <= 0 {
			continue
		}
		if err := qtx.TouchPublicCacheEntry(ctx, touch); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type proxyRequestTupleRollupKey struct {
	BucketUnixMillis int64
	ListenerID       int64
	RouteTargetID    int64
	RouteID          int64
	AgentID          int64
	ErrorKind        string
	StatusClass      int64
}

type proxyRequestStatusRollupKey struct {
	BucketUnixMillis int64
	StatusCode       int64
}

type proxyRetryRollupKey struct {
	BucketUnixMillis int64
	RetryRuleID      int64
	FailedAgentID    int64
	ErrorKind        string
}

func mergeProxyRequestRollup(totals map[int64]db.UpsertProxyRequestRollupMinuteParams, next db.UpsertProxyRequestRollupMinuteParams) {
	current, ok := totals[next.BucketUnixMillis]
	if !ok {
		totals[next.BucketUnixMillis] = next
		return
	}
	current.Requests += next.Requests
	current.Success += next.Success
	current.ClientError += next.ClientError
	current.ServerError += next.ServerError
	current.InternalError += next.InternalError
	current.DurationMsSum += next.DurationMsSum
	if next.MaxDurationMs > current.MaxDurationMs {
		current.MaxDurationMs = next.MaxDurationMs
	}
	current.SlowRequests += next.SlowRequests
	current.RequestBytes += next.RequestBytes
	current.ResponseBytes += next.ResponseBytes
	current.CacheHits += next.CacheHits
	current.CacheMisses += next.CacheMisses
	current.CacheBypasses += next.CacheBypasses
	current.CacheStored += next.CacheStored
	current.CacheStoreFailed += next.CacheStoreFailed
	current.CacheHitBytes += next.CacheHitBytes
	current.CacheStoredBytes += next.CacheStoredBytes
	totals[next.BucketUnixMillis] = current
}

func mergeProxyRequestTupleRollup(tuples map[proxyRequestTupleRollupKey]db.UpsertProxyRequestTupleRollupMinuteParams, next db.UpsertProxyRequestTupleRollupMinuteParams) {
	key := proxyRequestTupleRollupKey{
		BucketUnixMillis: next.BucketUnixMillis,
		ListenerID:       next.ListenerID,
		RouteTargetID:    next.RouteTargetID,
		RouteID:          next.RouteID,
		AgentID:          next.AgentID,
		ErrorKind:        next.ErrorKind,
		StatusClass:      next.StatusClass,
	}
	current, ok := tuples[key]
	if !ok {
		tuples[key] = next
		return
	}
	current.Requests += next.Requests
	current.Success += next.Success
	current.ClientError += next.ClientError
	current.ServerError += next.ServerError
	current.InternalError += next.InternalError
	current.DurationMsSum += next.DurationMsSum
	current.RequestBytes += next.RequestBytes
	current.ResponseBytes += next.ResponseBytes
	tuples[key] = current
}

func mergeProxyRequestStatusRollup(statuses map[proxyRequestStatusRollupKey]db.UpsertProxyRequestStatusRollupMinuteParams, next db.UpsertProxyRequestStatusRollupMinuteParams) {
	key := proxyRequestStatusRollupKey{
		BucketUnixMillis: next.BucketUnixMillis,
		StatusCode:       next.StatusCode,
	}
	current, ok := statuses[key]
	if !ok {
		statuses[key] = next
		return
	}
	current.Requests += next.Requests
	current.Success += next.Success
	current.ClientError += next.ClientError
	current.ServerError += next.ServerError
	current.InternalError += next.InternalError
	current.DurationMsSum += next.DurationMsSum
	current.RequestBytes += next.RequestBytes
	current.ResponseBytes += next.ResponseBytes
	statuses[key] = current
}

func mergeProxyRetryRollup(retries map[proxyRetryRollupKey]db.UpsertProxyRetryRollupMinuteParams, next db.UpsertProxyRetryRollupMinuteParams) {
	key := proxyRetryRollupKey{
		BucketUnixMillis: next.BucketUnixMillis,
		RetryRuleID:      next.RetryRuleID,
		FailedAgentID:    next.FailedAgentID,
		ErrorKind:        next.ErrorKind,
	}
	current, ok := retries[key]
	if !ok {
		retries[key] = next
		return
	}
	current.MatchedRequests += next.MatchedRequests
	current.RetriedRequests += next.RetriedRequests
	current.RetryAttempts += next.RetryAttempts
	current.RecoveredRequests += next.RecoveredRequests
	current.ExhaustedRequests += next.ExhaustedRequests
	current.SkippedRequests += next.SkippedRequests
	current.DurationMsSum += next.DurationMsSum
	current.RetriedDurationMsSum += next.RetriedDurationMsSum
	retries[key] = current
}

func (a *App) insertAgentStatWithRollup(ctx context.Context, stat db.InsertAgentStatAtParams) error {
	if a.DB == nil {
		return nil
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	if _, err := qtx.InsertAgentStatAt(ctx, stat); err != nil {
		return err
	}
	if err := qtx.UpsertAgentStatRollupMinute(ctx, db.UpsertAgentStatRollupMinuteParams{
		BucketUnixMillis: rollupBucketUnixMillis(stat.ReportedAt),
		Samples:          1,
		ReqSuccess:       stat.ReqSuccess,
		ReqClientError:   stat.ReqClientError,
		ReqServerError:   stat.ReqServerError,
		ReqInternalError: stat.ReqInternalError,
		BytesRx:          stat.BytesRx,
		BytesTx:          stat.BytesTx,
		MemoryMbSum:      stat.MemoryMb,
		MaxMemoryMb:      stat.MemoryMb,
		GoroutinesSum:    stat.Goroutines,
		MaxGoroutines:    stat.Goroutines,
		CpuPercentSum:    stat.CpuPercent,
		MaxCpuPercent:    stat.CpuPercent,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func proxyRequestRollupParams(event db.InsertProxyRequestEventAtParams) (db.UpsertProxyRequestRollupMinuteParams, db.UpsertProxyRequestTupleRollupMinuteParams, db.UpsertProxyRequestStatusRollupMinuteParams) {
	success := int64Bool(event.StatusCode >= 200 && event.StatusCode < 400)
	clientError := int64Bool(event.StatusCode >= 400 && event.StatusCode < 500)
	serverError := int64Bool(event.StatusCode >= 500)
	internalError := int64Bool(event.ErrorKind != "")
	cacheHits := int64Bool(event.CacheStatus == publicCacheStatusHit)
	cacheMisses := int64Bool(event.CacheStatus == publicCacheStatusMiss || event.CacheStatus == publicCacheStatusStored || event.CacheStatus == publicCacheStatusStoreFailed)
	cacheBypasses := int64Bool(event.CacheStatus == publicCacheStatusBypass)
	cacheStored := int64Bool(event.CacheStatus == publicCacheStatusStored)
	cacheStoreFailed := int64Bool(event.CacheStatus == publicCacheStatusStoreFailed)
	cacheHitBytes := int64(0)
	if event.CacheStatus == publicCacheStatusHit {
		cacheHitBytes = event.CacheBytes
	}
	cacheStoredBytes := int64(0)
	if event.CacheStatus == publicCacheStatusStored {
		cacheStoredBytes = event.CacheBytes
	}

	bucketUnixMillis := rollupBucketUnixMillis(event.OccurredAt)
	total := db.UpsertProxyRequestRollupMinuteParams{
		BucketUnixMillis: bucketUnixMillis,
		Requests:         1,
		Success:          success,
		ClientError:      clientError,
		ServerError:      serverError,
		InternalError:    internalError,
		DurationMsSum:    event.DurationMs,
		MaxDurationMs:    event.DurationMs,
		SlowRequests:     int64Bool(event.DurationMs >= 1000),
		RequestBytes:     event.RequestBytes,
		ResponseBytes:    event.ResponseBytes,
		CacheHits:        cacheHits,
		CacheMisses:      cacheMisses,
		CacheBypasses:    cacheBypasses,
		CacheStored:      cacheStored,
		CacheStoreFailed: cacheStoreFailed,
		CacheHitBytes:    cacheHitBytes,
		CacheStoredBytes: cacheStoredBytes,
	}
	tuple := db.UpsertProxyRequestTupleRollupMinuteParams{
		BucketUnixMillis: bucketUnixMillis,
		ListenerID:       nullInt64Value(event.ListenerID),
		RouteTargetID:    nullInt64Value(event.RouteTargetID),
		RouteID:          nullInt64Value(event.RouteID),
		AgentID:          nullInt64Value(event.AgentID),
		ErrorKind:        event.ErrorKind,
		StatusClass:      proxyStatusClass(event.StatusCode),
		Requests:         1,
		Success:          success,
		ClientError:      clientError,
		ServerError:      serverError,
		InternalError:    internalError,
		DurationMsSum:    event.DurationMs,
		RequestBytes:     event.RequestBytes,
		ResponseBytes:    event.ResponseBytes,
	}
	status := db.UpsertProxyRequestStatusRollupMinuteParams{
		BucketUnixMillis: bucketUnixMillis,
		StatusCode:       event.StatusCode,
		Requests:         1,
		Success:          success,
		ClientError:      clientError,
		ServerError:      serverError,
		InternalError:    internalError,
		DurationMsSum:    event.DurationMs,
		RequestBytes:     event.RequestBytes,
		ResponseBytes:    event.ResponseBytes,
	}
	return total, tuple, status
}

func proxyRetryRollupParams(event db.InsertProxyRequestEventAtParams) (db.UpsertProxyRetryRollupMinuteParams, bool) {
	if !event.RetryRuleID.Valid {
		return db.UpsertProxyRetryRollupMinuteParams{}, false
	}
	retried := int64Bool(event.RetryCount > 0)
	retriedDuration := int64(0)
	if retried != 0 {
		retriedDuration = event.DurationMs
	}
	return db.UpsertProxyRetryRollupMinuteParams{
		BucketUnixMillis:     rollupBucketUnixMillis(event.OccurredAt),
		RetryRuleID:          event.RetryRuleID.Int64,
		FailedAgentID:        nullInt64Value(event.RetryFailedAgentID),
		ErrorKind:            event.RetryErrorKind,
		MatchedRequests:      1,
		RetriedRequests:      retried,
		RetryAttempts:        event.RetryCount,
		RecoveredRequests:    int64Bool(event.RetryOutcome == publicRetryOutcomeRecovered),
		ExhaustedRequests:    int64Bool(event.RetryOutcome == publicRetryOutcomeExhausted),
		SkippedRequests:      int64Bool(event.RetryOutcome == publicRetryOutcomeSkipped),
		DurationMsSum:        event.DurationMs,
		RetriedDurationMsSum: retriedDuration,
	}, true
}

func (a *App) observabilityRollupsReady(ctx context.Context) (bool, error) {
	if a.DB == nil {
		return false, nil
	}
	state, err := a.DB.GetObservabilityRollupState(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return state.ProxyBackfilledThroughID >= state.ProxyBackfillUpperID &&
		state.AgentBackfilledThroughID >= state.AgentBackfillUpperID, nil
}

func (a *App) backfillObservabilityRollupBatch(ctx context.Context) (bool, error) {
	if a == nil || a.DB == nil {
		return false, nil
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	state, err := qtx.GetObservabilityRollupState(ctx)
	if err != nil {
		return false, err
	}

	if state.ProxyBackfilledThroughID < state.ProxyBackfillUpperID {
		next, err := qtx.GetNextProxyRollupBackfillThroughID(ctx, db.GetNextProxyRollupBackfillThroughIDParams{
			CurrentID: state.ProxyBackfilledThroughID,
			UpperID:   state.ProxyBackfillUpperID,
			BatchSize: observabilityRollupBackfillBatchRows,
		})
		if err != nil {
			return false, err
		}
		if next <= state.ProxyBackfilledThroughID {
			next = state.ProxyBackfillUpperID
		} else {
			if err := qtx.BackfillProxyRequestRollupMinutesRange(ctx, db.BackfillProxyRequestRollupMinutesRangeParams{
				FromID:    state.ProxyBackfilledThroughID,
				ThroughID: next,
			}); err != nil {
				return false, err
			}
			if err := qtx.BackfillProxyRequestTupleRollupMinutesRange(ctx, db.BackfillProxyRequestTupleRollupMinutesRangeParams{
				FromID:    state.ProxyBackfilledThroughID,
				ThroughID: next,
			}); err != nil {
				return false, err
			}
			if err := qtx.BackfillProxyRequestStatusRollupMinutesRange(ctx, db.BackfillProxyRequestStatusRollupMinutesRangeParams{
				FromID:    state.ProxyBackfilledThroughID,
				ThroughID: next,
			}); err != nil {
				return false, err
			}
		}
		if err := qtx.MarkProxyRollupBackfilledThrough(ctx, next); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	if state.AgentBackfilledThroughID < state.AgentBackfillUpperID {
		next, err := qtx.GetNextAgentRollupBackfillThroughID(ctx, db.GetNextAgentRollupBackfillThroughIDParams{
			CurrentID: state.AgentBackfilledThroughID,
			UpperID:   state.AgentBackfillUpperID,
			BatchSize: observabilityRollupBackfillBatchRows,
		})
		if err != nil {
			return false, err
		}
		if next <= state.AgentBackfilledThroughID {
			next = state.AgentBackfillUpperID
		} else {
			if err := qtx.BackfillAgentStatRollupMinutesRange(ctx, db.BackfillAgentStatRollupMinutesRangeParams{
				FromID:    state.AgentBackfilledThroughID,
				ThroughID: next,
			}); err != nil {
				return false, err
			}
		}
		if err := qtx.MarkAgentRollupBackfilledThrough(ctx, next); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	return false, tx.Commit()
}

func (a *App) StartObservabilityMaintenance(ctx context.Context) {
	if a == nil || a.DB == nil {
		return
	}
	go func() {
		backfillTicker := time.NewTicker(observabilityRollupBackfillInterval)
		cleanupTicker := time.NewTicker(observabilityCleanupInterval)
		defer backfillTicker.Stop()
		defer cleanupTicker.Stop()

		a.runObservabilityBackfillBatch(ctx)
		a.cleanupObservability(ctx, time.Now().UTC())

		for {
			select {
			case <-ctx.Done():
				return
			case <-backfillTicker.C:
				a.runObservabilityBackfillBatch(ctx)
			case now := <-cleanupTicker.C:
				a.cleanupObservability(ctx, now.UTC())
			}
		}
	}()
}

func (a *App) StartObservabilityCleanup(ctx context.Context) {
	a.StartObservabilityMaintenance(ctx)
}

func (a *App) runObservabilityBackfillBatch(ctx context.Context) {
	if _, err := a.backfillObservabilityRollupBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Warn().Err(err).Msg("Failed to backfill observability rollups")
	}
}

func rollupBucketUnixMillis(t time.Time) int64 {
	return t.UTC().Truncate(time.Minute).UnixMilli()
}

func proxyStatusClass(statusCode int64) int64 {
	if statusCode < 200 || statusCode >= 600 {
		return 0
	}
	return statusCode / 100
}

func int64Bool(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
