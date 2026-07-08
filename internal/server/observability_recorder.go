package server

import (
	"context"
	"database/sql"
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
	observabilityRecorderMaxBatch      = 512
	publicCacheTouchCoalesceInterval   = time.Minute
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
	events         chan db.InsertProxyRequestEventAtParams
	touches        chan string
	control        chan observabilityRecorderRequest
	done           chan struct{}
	mu             sync.Mutex
	startOnce      sync.Once
	stopped        bool
	droppedEvents  atomic.Int64
	droppedTouches atomic.Int64
}

type observabilityRecorderRequest struct {
	ctx  context.Context
	done chan error
	stop bool
}

func newObservabilityRecorder(app *App) *observabilityRecorder {
	return &observabilityRecorder{
		app:     app,
		events:  make(chan db.InsertProxyRequestEventAtParams, observabilityRecorderQueueSize),
		touches: make(chan string, observabilityRecorderQueueSize),
		control: make(chan observabilityRecorderRequest),
		done:    make(chan struct{}),
	}
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
		r.droppedEvents.Add(1)
		log.Warn().Msg("Dropping proxy request event because observability recorder queue is full")
	}
	_ = ctx
}

func (r *observabilityRecorder) touchPublicCacheEntry(keyDigest string) {
	if r == nil || r.app == nil || r.app.DB == nil || keyDigest == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.startLocked()
	select {
	case r.touches <- keyDigest:
	default:
		r.droppedTouches.Add(1)
		log.Warn().Msg("Dropping public cache touch because observability recorder queue is full")
	}
}

func (r *observabilityRecorder) flush(ctx context.Context) error {
	if r == nil || r.app == nil || r.app.DB == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.stopped {
		done := r.done
		r.mu.Unlock()
		select {
		case <-done:
			return nil
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

func (r *observabilityRecorder) close(ctx context.Context) error {
	if r == nil || r.app == nil || r.app.DB == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.stopped {
		done := r.done
		r.mu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.stopped = true
	r.startLocked()
	done := make(chan error, 1)
	req := observabilityRecorderRequest{ctx: ctx, done: done, stop: true}
	select {
	case r.control <- req:
		r.mu.Unlock()
	case <-ctx.Done():
		r.stopped = false
		r.mu.Unlock()
		return ctx.Err()
	}
	select {
	case err := <-done:
		select {
		case <-r.done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *observabilityRecorder) startLocked() {
	r.startOnce.Do(func() {
		go r.run()
	})
}

func (r *observabilityRecorder) run() {
	ticker := time.NewTicker(observabilityRecorderFlushInterval)
	defer ticker.Stop()
	var batch []db.InsertProxyRequestEventAtParams
	touches := make(map[string]struct{})
	lastTouch := make(map[string]time.Time)
	for {
		select {
		case event := <-r.events:
			batch = append(batch, event)
			if len(batch) >= observabilityRecorderMaxBatch {
				r.flushBatch(context.Background(), &batch, touches, lastTouch)
			}
		case keyDigest := <-r.touches:
			if shouldQueuePublicCacheTouch(keyDigest, lastTouch, time.Now().UTC()) {
				touches[keyDigest] = struct{}{}
			}
		case req := <-r.control:
			r.drainAvailable(&batch, touches, lastTouch)
			req.done <- r.flushBatch(req.ctx, &batch, touches, lastTouch)
			if req.stop {
				close(r.done)
				return
			}
		case <-ticker.C:
			r.drainAvailable(&batch, touches, lastTouch)
			r.flushBatch(context.Background(), &batch, touches, lastTouch)
		}
	}
}

func (r *observabilityRecorder) drainAvailable(batch *[]db.InsertProxyRequestEventAtParams, touches map[string]struct{}, lastTouch map[string]time.Time) {
	for {
		select {
		case event := <-r.events:
			*batch = append(*batch, event)
		case keyDigest := <-r.touches:
			if shouldQueuePublicCacheTouch(keyDigest, lastTouch, time.Now().UTC()) {
				touches[keyDigest] = struct{}{}
			}
		default:
			return
		}
	}
}

func (r *observabilityRecorder) flushBatch(ctx context.Context, batch *[]db.InsertProxyRequestEventAtParams, touches map[string]struct{}, lastTouch map[string]time.Time) error {
	if len(*batch) == 0 && len(touches) == 0 {
		return nil
	}
	events := append([]db.InsertProxyRequestEventAtParams(nil), (*batch)...)
	touchKeys := make([]string, 0, len(touches))
	for keyDigest := range touches {
		touchKeys = append(touchKeys, keyDigest)
	}
	if err := r.app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, events, touchKeys); err != nil {
		log.Warn().Err(err).Msg("Failed to flush observability recorder")
		return err
	}
	*batch = (*batch)[:0]
	for keyDigest := range touches {
		delete(touches, keyDigest)
	}
	return nil
}

func shouldQueuePublicCacheTouch(keyDigest string, lastTouch map[string]time.Time, now time.Time) bool {
	if keyDigest == "" {
		return false
	}
	if previous, ok := lastTouch[keyDigest]; ok && now.Sub(previous) < publicCacheTouchCoalesceInterval {
		return false
	}
	lastTouch[keyDigest] = now
	return true
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
