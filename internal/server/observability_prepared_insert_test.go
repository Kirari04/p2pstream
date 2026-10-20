package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"p2pstream/internal/db"
)

func TestPreparedProxyRequestEventBatchPreservesAtomicRowsRollupsAndTouches(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	storedAt := time.Now().UTC().Round(0)
	if _, err := app.DB.ExecContext(ctx, `INSERT INTO public_cache_rules (id, name) VALUES (1, 'prepared-batch-rule')`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO public_listeners (id, name, bind_address, port, protocol) VALUES
			(11, 'prepared-batch-listener', '127.0.0.1', 18111, 'http');
		INSERT INTO public_routes (id, listener_id, priority, host_pattern, path_prefix) VALUES
			(12, 11, 10, 'all-fields.example', '/all-fields');
		INSERT INTO public_route_targets (id, route_id, name, position) VALUES
			(13, 12, 'prepared-batch-target', 0);
		INSERT INTO public_waf_rules (id, name) VALUES
			(14, 'prepared-batch-waf');
		INSERT INTO agents (id, public_id, name, token_hash) VALUES
			(15, 'prepared-batch-agent', 'prepared-batch-agent', 'prepared-batch-agent-hash'),
			(16, 'prepared-batch-failed-agent', 'prepared-batch-failed-agent', 'prepared-batch-failed-agent-hash');
		INSERT INTO public_retry_rules (id, name) VALUES
			(17, 'prepared-batch-retry');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO public_cache_entries (
			key_digest, rule_id, scope, listener_protocol, host, path, query_key, method,
			vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at
		) VALUES ('prepared-batch-key', 1, 'selected_backend', 'http', 'example.test', '/asset', '', 'GET',
			'[]', '{}', 200, '/tmp/body', 10, ?, ?)
	`, storedAt, storedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	events := []db.InsertProxyRequestEventAtParams{
		{
			OccurredAt:         storedAt,
			StatusCode:         http.StatusTeapot,
			DurationMs:         1234,
			ErrorKind:          "binding-error",
			Method:             "PATCH",
			Host:               "all-fields.example",
			PathPrefix:         "/all-fields",
			ListenerID:         sql.NullInt64{Int64: 11, Valid: true},
			RouteID:            sql.NullInt64{Int64: 12, Valid: true},
			RouteTargetID:      sql.NullInt64{Int64: 13, Valid: true},
			WafRuleID:          sql.NullInt64{Int64: 14, Valid: true},
			WafAction:          "challenge",
			AgentID:            sql.NullInt64{Int64: 15, Valid: true},
			RequestBytes:       12345,
			ResponseBytes:      67890,
			CacheRuleID:        sql.NullInt64{Int64: 1, Valid: true},
			CacheStatus:        "stale",
			CacheBytes:         2222,
			RetryRuleID:        sql.NullInt64{Int64: 17, Valid: true},
			RetryCount:         3,
			RetryOutcome:       "recovered",
			RetryErrorKind:     "connection-reset",
			RetryFailedAgentID: sql.NullInt64{Int64: 16, Valid: true},
		},
		{OccurredAt: storedAt, StatusCode: http.StatusNotFound, DurationMs: 20, ErrorKind: "not_found", Method: "GET", Host: "example.test", PathPrefix: "/b", ResponseBytes: 20},
		{OccurredAt: storedAt, StatusCode: http.StatusBadGateway, DurationMs: 30, ErrorKind: "upstream", Method: "POST", Host: "example.test", PathPrefix: "/c", RequestBytes: 5},
	}
	// Cross both full 16-row groups and the short final remainder while
	// retaining distinct values, valid foreign keys, NULLs and timestamps.
	examples := append([]db.InsertProxyRequestEventAtParams(nil), events...)
	for index := len(events); index < 35; index++ {
		event := examples[index%len(examples)]
		event.OccurredAt = storedAt.Add(time.Duration(index) * time.Millisecond)
		event.DurationMs += int64(index)
		event.PathPrefix = fmt.Sprintf("%s/%d", event.PathPrefix, index)
		events = append(events, event)
	}
	touch := db.TouchPublicCacheEntryParams{
		KeyDigest: "prepared-batch-key", StoredAt: storedAt,
		LastAccessedAt: storedAt.Add(time.Minute), HitCount: 2,
	}

	if _, err := app.DB.ExecContext(ctx, `
		CREATE TRIGGER fail_prepared_batch_touch
		BEFORE UPDATE OF hit_count ON public_cache_entries
		BEGIN SELECT RAISE(ABORT, 'forced cache touch failure'); END
	`); err != nil {
		t.Fatal(err)
	}
	if err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, events, []db.TouchPublicCacheEntryParams{touch}); err == nil || !strings.Contains(err.Error(), "forced cache touch failure") {
		t.Fatalf("cache-touch failure = %v, want forced rollback", err)
	}
	assertPreparedObservabilityBatchState(t, app, 0, 0, 0)
	if _, err := app.DB.ExecContext(ctx, `DROP TRIGGER fail_prepared_batch_touch`); err != nil {
		t.Fatal(err)
	}

	if err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, events, []db.TouchPublicCacheEntryParams{touch}); err != nil {
		t.Fatalf("insert prepared event batch: %v", err)
	}
	assertPreparedObservabilityBatchState(t, app, int64(len(events)), int64(len(events)), 2)
	rows, err := app.DB.QueryContext(ctx, `
		SELECT occurred_at, status_code, duration_ms, error_kind, method, host, path_prefix,
			listener_id, route_id, route_target_id, waf_rule_id, waf_action, agent_id,
			request_bytes, response_bytes, cache_rule_id, cache_status, cache_bytes,
			retry_rule_id, retry_count, retry_outcome, retry_error_kind, retry_failed_agent_id
		FROM proxy_request_events
		ORDER BY id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for index, want := range events {
		if !rows.Next() {
			t.Fatalf("raw event %d is missing", index)
		}
		var got db.InsertProxyRequestEventAtParams
		if err := rows.Scan(
			&got.OccurredAt,
			&got.StatusCode,
			&got.DurationMs,
			&got.ErrorKind,
			&got.Method,
			&got.Host,
			&got.PathPrefix,
			&got.ListenerID,
			&got.RouteID,
			&got.RouteTargetID,
			&got.WafRuleID,
			&got.WafAction,
			&got.AgentID,
			&got.RequestBytes,
			&got.ResponseBytes,
			&got.CacheRuleID,
			&got.CacheStatus,
			&got.CacheBytes,
			&got.RetryRuleID,
			&got.RetryCount,
			&got.RetryOutcome,
			&got.RetryErrorKind,
			&got.RetryFailedAgentID,
		); err != nil {
			t.Fatal(err)
		}
		if !got.OccurredAt.Equal(want.OccurredAt) {
			t.Fatalf("raw event %d occurred_at = %s, want %s", index, got.OccurredAt, want.OccurredAt)
		}
		got.OccurredAt = want.OccurredAt
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("raw event %d = %+v, want %+v", index, got, want)
		}
	}
	if rows.Next() {
		t.Fatal("prepared batch wrote extra raw events")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, invalidIndex := range []int{0, 17, 32} {
		invalid := make([]db.InsertProxyRequestEventAtParams, 33)
		for index := range invalid {
			invalid[index] = db.InsertProxyRequestEventAtParams{OccurredAt: storedAt.Add(time.Minute), StatusCode: http.StatusNoContent}
		}
		invalid[invalidIndex].AgentID = sql.NullInt64{Int64: 999_999, Valid: true}
		if err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, invalid, nil); err == nil {
			t.Fatalf("invalid foreign key at row %d committed", invalidIndex)
		}
		assertPreparedObservabilityBatchState(t, app, int64(len(events)), int64(len(events)), 2)
	}
}

func TestPreparedProxyRequestEventBatchCancellationRollsBack(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	// Keep the registered trigger callback on the same physical connection as
	// the flush transaction. No custom process-wide SQLite driver is needed.
	app.DB.SetMaxOpenConns(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	conn, err := app.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(driverConn any) error {
		return driverConn.(*sqlite3.SQLiteConn).RegisterFunc("block_proxy_batch", func() int64 {
			close(entered)
			select {
			case <-release:
			case <-time.After(3 * time.Second):
			}
			return 0
		}, false)
	})
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	storedAt := time.Now().UTC().Round(0)
	if _, err := app.DB.ExecContext(t.Context(), `
		INSERT INTO public_cache_rules (id, name) VALUES (1, 'cancel-batch-rule');
		CREATE TRIGGER block_second_proxy_batch AFTER INSERT ON proxy_request_events
		WHEN NEW.duration_ms = 17
		BEGIN SELECT block_proxy_batch(); END;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.ExecContext(t.Context(), `
		INSERT INTO public_cache_entries (
			key_digest, rule_id, scope, listener_protocol, host, path, query_key, method,
			vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at
		) VALUES ('prepared-batch-key', 1, 'selected_backend', 'http', 'example.test', '/asset', '', 'GET',
			'[]', '{}', 200, '/tmp/body', 10, ?, ?)
	`, storedAt, storedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	events := make([]db.InsertProxyRequestEventAtParams, 35)
	for index := range events {
		events[index] = db.InsertProxyRequestEventAtParams{
			OccurredAt: storedAt, StatusCode: http.StatusOK, DurationMs: int64(index + 1), Method: "GET", Host: "cancel.example",
		}
	}
	touch := db.TouchPublicCacheEntryParams{KeyDigest: "prepared-batch-key", StoredAt: storedAt, LastAccessedAt: storedAt.Add(time.Minute), HitCount: 2}
	ctx, cancel := context.WithTimeout(t.Context(), observabilityRecorderFlushTimeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, events, []db.TouchPublicCacheEntryParams{touch})
	}()
	select {
	case <-entered:
		// The first full group has executed; cancel while the second group
		// is inside SQLite, then let the bounded callback finish.
		cancel()
		unblock()
	case err := <-result:
		t.Fatalf("flush returned before second group: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("flush did not reach second group")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled batch error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled batch did not return")
	}
	assertPreparedObservabilityBatchState(t, app, 0, 0, 0)
	for _, table := range []string{"proxy_request_tuple_rollup_minutes", "proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes"} {
		var rows int
		if err := app.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("cancelled batch left %s rows=%d err=%v", table, rows, err)
		}
	}
}

func TestPreparedProxyRequestEventNullableIDsMatchGeneratedInsert(t *testing.T) {
	database := newServerTestDB(t)
	probe := func(event db.InsertProxyRequestEventAtParams, mode string) (string, [][8]sql.NullInt64) {
		t.Helper()
		tx, err := database.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		events := make([]db.InsertProxyRequestEventAtParams, 16)
		for index := range events {
			events[index] = event
		}
		if mode == "generated" {
			for _, event := range events {
				if err = database.WithTx(tx).InsertProxyRequestEventAt(t.Context(), event); err != nil {
					break
				}
			}
		} else {
			var inserter *db.ProxyRequestEventInserter
			inserter, err = db.PrepareProxyRequestEventInserter(t.Context(), tx)
			if err == nil {
				if mode == "batch" {
					err = inserter.InsertBatch(t.Context(), events)
				} else {
					for _, event := range events {
						if err = inserter.Insert(t.Context(), event); err != nil {
							break
						}
					}
				}
				if closeErr := inserter.Close(); err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			return fmt.Sprintf("%T:%v", err, err), nil
		}
		rows, err := tx.QueryContext(t.Context(), `SELECT listener_id, route_id, route_target_id, waf_rule_id,
			agent_id, cache_rule_id, retry_rule_id, retry_failed_agent_id FROM proxy_request_events ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var persisted [][8]sql.NullInt64
		for rows.Next() {
			var ids [8]sql.NullInt64
			if err := rows.Scan(&ids[0], &ids[1], &ids[2], &ids[3], &ids[4], &ids[5], &ids[6], &ids[7]); err != nil {
				t.Fatal(err)
			}
			persisted = append(persisted, ids)
		}
		if err := rows.Err(); err != nil || len(persisted) != len(events) {
			t.Fatalf("persisted rows=%d err=%v, want %d", len(persisted), err, len(events))
		}
		return "", persisted
	}
	for field := range 8 {
		for _, payload := range []sql.NullInt64{{}, {Int64: 999_999}, {Int64: 0, Valid: true}, {Int64: -1, Valid: true}, {Int64: -1 << 63, Valid: true}, {Int64: 1<<63 - 1, Valid: true}} {
			event := db.InsertProxyRequestEventAtParams{OccurredAt: time.Now().UTC(), StatusCode: http.StatusOK}
			ids := []*sql.NullInt64{&event.ListenerID, &event.RouteID, &event.RouteTargetID, &event.WafRuleID, &event.AgentID, &event.CacheRuleID, &event.RetryRuleID, &event.RetryFailedAgentID}
			*ids[field] = payload
			// Generated SQL supplies the database oracle; zero is not assumed to
			// be a valid FK, and invalid nonzero payloads must remain SQL NULL.
			wantError, wantRows := probe(event, "generated")
			for _, mode := range []string{"single", "batch"} {
				gotError, gotRows := probe(event, mode)
				if gotError != wantError || !reflect.DeepEqual(gotRows, wantRows) {
					t.Fatalf("field %d payload=%+v mode=%s got=(%q,%+v), generated=(%q,%+v)", field, payload, mode, gotError, gotRows, wantError, wantRows)
				}
			}
		}
	}
	var rows int
	if err := database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM proxy_request_events`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("nullable-ID probes did not roll back: rows=%d err=%v", rows, err)
	}
}

func assertPreparedObservabilityBatchState(t *testing.T, app *App, rawEvents, rollupRequests, cacheHits int64) {
	t.Helper()
	ctx := context.Background()
	var gotRaw, gotRollup, gotHits int64
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&gotRaw); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(requests), 0) FROM proxy_request_rollup_minutes`).Scan(&gotRollup); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.QueryRowContext(ctx, `SELECT hit_count FROM public_cache_entries WHERE key_digest = 'prepared-batch-key'`).Scan(&gotHits); err != nil {
		t.Fatal(err)
	}
	if gotRaw != rawEvents || gotRollup != rollupRequests || gotHits != cacheHits {
		t.Fatalf("observability state = raw %d/rollup %d/hits %d, want %d/%d/%d", gotRaw, gotRollup, gotHits, rawEvents, rollupRequests, cacheHits)
	}
}

func BenchmarkPreparedProxyRequestEventBatchFlush(b *testing.B) {
	for _, productionDeadline := range []bool{false, true} {
		name := "background"
		if productionDeadline {
			name = "production_deadline"
		}
		b.Run(name, func(b *testing.B) {
			benchmarkPreparedProxyRequestEventBatchFlush(b, productionDeadline)
		})
	}
}

func benchmarkPreparedProxyRequestEventBatchFlush(b *testing.B, productionDeadline bool) {
	benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, productionDeadline, observabilityRecorderMaxBatch, true)
}

func BenchmarkPreparedProxyRequestEventLowCardinalityFlush(b *testing.B) {
	for _, count := range []int{1, 16, observabilityRecorderMaxBatch} {
		b.Run(fmt.Sprintf("events_%d", count), func(b *testing.B) {
			benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, true, count, false)
		})
	}
}

func benchmarkPreparedProxyRequestEventBatchFlushCardinality(b *testing.B, productionDeadline bool, eventCount int, diverse bool) {
	database, err := db.Open(filepath.Join(b.TempDir(), "observability-benchmark.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	app := NewApp(nil, database)
	recorder := newObservabilityRecorder(app)
	now := time.Now().UTC()
	events := make([]db.InsertProxyRequestEventAtParams, eventCount)
	for index := range events {
		events[index] = db.InsertProxyRequestEventAtParams{
			OccurredAt: now,
			StatusCode: int64(200 + index%5),
			DurationMs: int64(index % 250),
			ErrorKind:  fmt.Sprintf("kind-%d", index%16),
			Method:     "GET",
			Host:       "benchmark.example",
			PathPrefix: fmt.Sprintf("/route-%d", index%64),
		}
		if !diverse {
			events[index].StatusCode = 200
			events[index].ErrorKind = ""
			events[index].PathPrefix = "/route"
		}
	}
	batch := make([]db.InsertProxyRequestEventAtParams, 0, len(events))
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		batch = append(batch[:0], events...)
		var err error
		if productionDeadline {
			// Match the production five-second timeout and cancellable flush
			// operation. SQLite's context-aware execution takes a different
			// path when Done is non-nil, even if no cancellation occurs.
			err = recorder.runBackgroundFlush(func(ctx context.Context) error {
				return recorder.flushBatch(ctx, &batch, touches, now, true)
			})
		} else {
			err = recorder.flushBatch(context.Background(), &batch, touches, now, true)
		}
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		for _, table := range []string{
			"proxy_request_events",
			"proxy_request_rollup_minutes",
			"proxy_request_tuple_rollup_minutes",
			"proxy_request_status_rollup_minutes",
			"proxy_retry_rollup_minutes",
		} {
			if _, err := database.ExecContext(context.Background(), "DELETE FROM "+table); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
	}
	b.ReportMetric(float64(len(events)), "events/op")
}
