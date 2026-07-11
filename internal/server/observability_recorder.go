package server

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"p2pstream/internal/db"
)

const (
	observabilityRecorderQueueSize     = 8192
	observabilityRecorderFlushInterval = 250 * time.Millisecond
	observabilityRecorderFlushTimeout  = 5 * time.Second
	observabilityRecorderMaxBatch      = 512
	observabilityRecorderMaxTouchKeys  = 8192
	observabilityRecorderLogInterval   = time.Minute
	publicCacheTouchCoalesceInterval   = time.Minute
	sqliteLegacyTimestampLayout        = "2006-01-02 15:04:05"
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
	app            *App
	insertBatch    observabilityRecorderInsertFunc
	closeWaitHook  func()
	events         chan db.InsertProxyRequestEventAtParams
	touches        chan publicCacheTouch
	control        chan observabilityRecorderRequest
	done           chan struct{}
	mu             sync.Mutex
	flushMu        sync.Mutex
	activeFlush    *observabilityRecorderFlushOperation
	startOnce      sync.Once
	stopped        bool
	closeAttempt   chan struct{}
	closing        atomic.Bool
	droppedEvents  atomic.Int64
	droppedTouches atomic.Int64
	pendingEvents  atomic.Int64
	pendingTouches atomic.Int64
	nextDropLog    atomic.Int64
	nextErrorLog   atomic.Int64
}

type observabilityRecorderFlushOperation struct {
	cancel context.CancelFunc
}

type observabilityRecorderInsertFunc func(context.Context, []db.InsertProxyRequestEventAtParams, []db.TouchPublicCacheEntryParams) error

type observabilityRecorderRequest struct {
	ctx          context.Context
	done         chan error
	closeAttempt chan struct{}
	stop         bool
}

type publicCacheTouchState struct {
	hitCount       int64
	lastAccessedAt time.Time
	lastFlushedAt  time.Time
}

type publicCacheTouchKey struct {
	keyDigest string
	storedAt  time.Time
}

type publicCacheTouch struct {
	key        publicCacheTouchKey
	accessedAt time.Time
}

func newPublicCacheTouch(keyDigest string, storedAt time.Time, accessedAt time.Time) publicCacheTouch {
	storedAt = storedAt.UTC().Round(0)
	accessedAt = accessedAt.UTC().Round(0)
	if accessedAt.Before(storedAt) {
		accessedAt = storedAt
	}
	return publicCacheTouch{
		key: publicCacheTouchKey{
			keyDigest: keyDigest,
			storedAt:  storedAt,
		},
		accessedAt: accessedAt,
	}
}

func newObservabilityRecorder(app *App) *observabilityRecorder {
	recorder := &observabilityRecorder{
		app:     app,
		events:  make(chan db.InsertProxyRequestEventAtParams, observabilityRecorderQueueSize),
		touches: make(chan publicCacheTouch, observabilityRecorderQueueSize),
		control: make(chan observabilityRecorderRequest),
		done:    make(chan struct{}),
	}
	if app != nil {
		recorder.insertBatch = app.insertProxyRequestEventsWithRollupsAndCacheTouches
	}
	return recorder
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

func (a *App) flushObservabilityRecorder(ctx context.Context) error {
	return a.FlushObservabilityRecorder(ctx)
}

func (a *App) FlushObservabilityRecorder(ctx context.Context) error {
	return a.observabilityRecorderService().flush(ctx)
}

func (a *App) CloseObservabilityRecorder(ctx context.Context) error {
	return a.observabilityRecorderService().close(ctx)
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.startLocked()
	select {
	case r.events <- db.InsertProxyRequestEventAtParams{
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
	}:
	default:
		r.dropEvent()
	}
	_ = ctx
}

func (r *observabilityRecorder) touchPublicCacheEntry(keyDigest string, storedAt time.Time, accessedAt time.Time) {
	if r == nil || r.app == nil || r.app.DB == nil || keyDigest == "" || storedAt.IsZero() || accessedAt.IsZero() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.startLocked()
	select {
	case r.touches <- newPublicCacheTouch(keyDigest, storedAt, accessedAt):
	default:
		r.dropTouch()
	}
}

func (r *observabilityRecorder) dropEvent() {
	r.droppedEvents.Add(1)
	r.logDrops()
}

func (r *observabilityRecorder) dropTouch() {
	r.droppedTouches.Add(1)
	r.logDrops()
}

func (r *observabilityRecorder) logDrops() {
	now := time.Now().UnixNano()
	for {
		next := r.nextDropLog.Load()
		if now < next {
			return
		}
		if r.nextDropLog.CompareAndSwap(next, now+int64(observabilityRecorderLogInterval)) {
			log.Warn().
				Int64("dropped_events", r.droppedEvents.Load()).
				Int64("dropped_cache_touches", r.droppedTouches.Load()).
				Msg("Dropping observability records because recorder capacity is exhausted")
			return
		}
	}
}

func (r *observabilityRecorder) logFlushError(err error) {
	now := time.Now().UnixNano()
	for {
		next := r.nextErrorLog.Load()
		if now < next {
			return
		}
		if r.nextErrorLog.CompareAndSwap(next, now+int64(observabilityRecorderLogInterval)) {
			log.Warn().Err(err).Msg("Failed to flush observability recorder; pending records will be retried")
			return
		}
	}
}

func (r *observabilityRecorder) flush(ctx context.Context) error {
	if r == nil || r.app == nil || r.app.DB == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		r.mu.Lock()
		if r.stopped {
			done := r.done
			attempt := r.closeAttempt
			r.mu.Unlock()
			select {
			case <-done:
				return nil
			case <-attempt:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		r.startLocked()
		done := make(chan error, 1)
		req := observabilityRecorderRequest{ctx: ctx, done: done}
		select {
		case r.control <- req:
			r.mu.Unlock()
		case <-ctx.Done():
			r.mu.Unlock()
			return ctx.Err()
		}
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *observabilityRecorder) close(ctx context.Context) error {
	if r == nil || r.app == nil || r.app.DB == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		r.mu.Lock()
		if r.stopped {
			done := r.done
			attempt := r.closeAttempt
			if r.closeWaitHook != nil {
				r.closeWaitHook()
			}
			r.mu.Unlock()
			select {
			case <-done:
				return nil
			case <-attempt:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		r.stopped = true
		r.closing.Store(true)
		attempt := make(chan struct{})
		r.closeAttempt = attempt
		r.startLocked()
		r.cancelActiveFlush()
		done := make(chan error, 1)
		req := observabilityRecorderRequest{ctx: ctx, done: done, closeAttempt: attempt, stop: true}
		select {
		case r.control <- req:
			r.mu.Unlock()
		case <-ctx.Done():
			r.restoreCloseAttemptLocked(attempt)
			r.mu.Unlock()
			return ctx.Err()
		}
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *observabilityRecorder) restoreCloseAttemptLocked(attempt chan struct{}) {
	if attempt == nil || r.closeAttempt != attempt {
		return
	}
	r.stopped = false
	r.closing.Store(false)
	r.closeAttempt = nil
	close(attempt)
}

func (r *observabilityRecorder) startLocked() {
	r.startOnce.Do(func() {
		go r.run()
	})
}

func (r *observabilityRecorder) cancelActiveFlush() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	if r.activeFlush != nil {
		r.activeFlush.cancel()
	}
}

func (r *observabilityRecorder) runFlushOperation(ctx context.Context, allowClosing bool, flush func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	flushCtx, cancel := context.WithCancel(ctx)
	operation := &observabilityRecorderFlushOperation{cancel: cancel}

	r.flushMu.Lock()
	if !allowClosing && r.closing.Load() {
		r.flushMu.Unlock()
		cancel()
		return context.Canceled
	}
	r.activeFlush = operation
	r.flushMu.Unlock()

	err := flush(flushCtx)
	cancel()
	r.flushMu.Lock()
	if r.activeFlush == operation {
		r.activeFlush = nil
	}
	r.flushMu.Unlock()
	return err
}

func (r *observabilityRecorder) runBackgroundFlush(flush func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), observabilityRecorderFlushTimeout)
	defer cancel()
	return r.runFlushOperation(ctx, false, flush)
}

func (r *observabilityRecorder) run() {
	ticker := time.NewTicker(observabilityRecorderFlushInterval)
	defer ticker.Stop()
	batch := make([]db.InsertProxyRequestEventAtParams, 0, observabilityRecorderMaxBatch)
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	for {
		events := r.events
		if len(batch) >= observabilityRecorderMaxBatch {
			// A failed batch must stay fixed-size while it is retried. Leaving
			// additional events in the bounded channel prevents an outage from
			// turning the batch into unbounded heap growth.
			events = nil
		}
		select {
		case event := <-events:
			batch = append(batch, event)
			r.pendingEvents.Store(int64(len(batch)))
			if len(batch) >= observabilityRecorderMaxBatch {
				_ = r.runBackgroundFlush(func(ctx context.Context) error {
					return r.flushBatch(ctx, &batch, touches, time.Now().UTC(), false)
				})
			}
		case touch := <-r.touches:
			r.queuePublicCacheTouch(touches, touch)
		case req := <-r.control:
			eventCount := len(r.events)
			touchCount := len(r.touches)
			err := r.runFlushOperation(req.ctx, req.stop, func(ctx context.Context) error {
				return r.flushQueued(ctx, &batch, touches, eventCount, touchCount)
			})
			if !req.stop {
				req.done <- err
				continue
			}
			if err != nil {
				r.mu.Lock()
				r.restoreCloseAttemptLocked(req.closeAttempt)
				r.mu.Unlock()
				req.done <- err
				continue
			}
			r.pendingEvents.Store(0)
			r.pendingTouches.Store(0)
			r.mu.Lock()
			if r.closeAttempt == req.closeAttempt {
				r.closeAttempt = nil
				close(r.done)
				close(req.closeAttempt)
			}
			r.mu.Unlock()
			req.done <- nil
			return
		case <-ticker.C:
			now := time.Now().UTC()
			r.prunePublicCacheTouches(touches, now)
			_ = r.runBackgroundFlush(func(ctx context.Context) error {
				return r.flushBatch(ctx, &batch, touches, now, false)
			})
		}
	}
}

func (r *observabilityRecorder) flushQueued(
	ctx context.Context,
	batch *[]db.InsertProxyRequestEventAtParams,
	touches map[publicCacheTouchKey]*publicCacheTouchState,
	eventCount int,
	touchCount int,
) error {
	for eventCount > 0 {
		if len(*batch) >= observabilityRecorderMaxBatch {
			if err := r.flushBatch(ctx, batch, touches, time.Now().UTC(), true); err != nil {
				return err
			}
		}
		remaining := observabilityRecorderMaxBatch - len(*batch)
		if remaining > eventCount {
			remaining = eventCount
		}
		for i := 0; i < remaining; i++ {
			*batch = append(*batch, <-r.events)
		}
		eventCount -= remaining
		r.pendingEvents.Store(int64(len(*batch)))
	}
	r.prunePublicCacheTouches(touches, time.Now().UTC())
	for i := 0; i < touchCount; i++ {
		r.queuePublicCacheTouch(touches, <-r.touches)
	}
	return r.flushBatch(ctx, batch, touches, time.Now().UTC(), true)
}

func (r *observabilityRecorder) queuePublicCacheTouch(touches map[publicCacheTouchKey]*publicCacheTouchState, queued publicCacheTouch) {
	if queued.key.keyDigest == "" || queued.key.storedAt.IsZero() || queued.accessedAt.IsZero() {
		return
	}
	queued = newPublicCacheTouch(queued.key.keyDigest, queued.key.storedAt, queued.accessedAt)
	if touch := touches[queued.key]; touch != nil {
		if touch.hitCount == math.MaxInt64 {
			r.dropTouch()
			return
		}
		touch.hitCount++
		if queued.accessedAt.After(touch.lastAccessedAt) {
			touch.lastAccessedAt = queued.accessedAt
		}
		return
	}
	if len(touches) >= observabilityRecorderMaxTouchKeys {
		r.dropTouch()
		return
	}
	touches[queued.key] = &publicCacheTouchState{hitCount: 1, lastAccessedAt: queued.accessedAt}
	r.pendingTouches.Store(int64(len(touches)))
}

func (r *observabilityRecorder) prunePublicCacheTouches(touches map[publicCacheTouchKey]*publicCacheTouchState, now time.Time) {
	for key, touch := range touches {
		if touch.hitCount != 0 || touch.lastFlushedAt.IsZero() || now.Sub(touch.lastFlushedAt) < publicCacheTouchCoalesceInterval {
			continue
		}
		delete(touches, key)
	}
	r.pendingTouches.Store(int64(len(touches)))
}

func publicCacheTouchesToFlush(touches map[publicCacheTouchKey]*publicCacheTouchState, now time.Time, force bool) []db.TouchPublicCacheEntryParams {
	var ready []db.TouchPublicCacheEntryParams
	for key, touch := range touches {
		if touch.hitCount <= 0 {
			continue
		}
		if !force && !touch.lastFlushedAt.IsZero() && now.Sub(touch.lastFlushedAt) < publicCacheTouchCoalesceInterval {
			continue
		}
		ready = append(ready, db.TouchPublicCacheEntryParams{
			HitCount:       touch.hitCount,
			KeyDigest:      key.keyDigest,
			StoredAt:       key.storedAt,
			StoredAtLegacy: key.storedAt.UTC().Format(sqliteLegacyTimestampLayout),
			LastAccessedAt: touch.lastAccessedAt,
		})
	}
	return ready
}

func (r *observabilityRecorder) flushBatch(
	ctx context.Context,
	batch *[]db.InsertProxyRequestEventAtParams,
	touches map[publicCacheTouchKey]*publicCacheTouchState,
	now time.Time,
	forceTouches bool,
) error {
	touchBatch := publicCacheTouchesToFlush(touches, now, forceTouches)
	if len(*batch) == 0 && len(touchBatch) == 0 {
		return nil
	}
	if r.insertBatch == nil {
		return nil
	}
	if err := r.insertBatch(ctx, *batch, touchBatch); err != nil {
		if !r.closing.Load() || ctx.Err() != context.Canceled {
			r.logFlushError(err)
		}
		return err
	}
	*batch = (*batch)[:0]
	r.pendingEvents.Store(0)
	for _, flushed := range touchBatch {
		key := newPublicCacheTouch(flushed.KeyDigest, flushed.StoredAt, flushed.LastAccessedAt).key
		touch := touches[key]
		if touch == nil {
			continue
		}
		touch.hitCount -= flushed.HitCount
		touch.lastFlushedAt = now
	}
	r.prunePublicCacheTouches(touches, now)
	return nil
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
