package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	assertPreparedObservabilityBatchState(t, app, 3, 3, 2)
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

	invalid := []db.InsertProxyRequestEventAtParams{
		{OccurredAt: storedAt.Add(time.Minute), StatusCode: http.StatusNoContent},
		{OccurredAt: storedAt.Add(time.Minute), StatusCode: http.StatusNoContent, AgentID: sql.NullInt64{Int64: 999_999, Valid: true}},
	}
	if err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, invalid, nil); err == nil {
		t.Fatal("invalid foreign key batch committed")
	}
	assertPreparedObservabilityBatchState(t, app, 3, 3, 2)
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
	database, err := db.Open(filepath.Join(b.TempDir(), "observability-benchmark.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	app := NewApp(nil, database)
	recorder := newObservabilityRecorder(app)
	now := time.Now().UTC()
	events := make([]db.InsertProxyRequestEventAtParams, observabilityRecorderMaxBatch)
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
	}
	batch := make([]db.InsertProxyRequestEventAtParams, 0, len(events))
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	b.ReportMetric(float64(len(events)), "events/op")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		batch = append(batch[:0], events...)
		if err := recorder.flushBatch(context.Background(), &batch, touches, now, true); err != nil {
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
}
