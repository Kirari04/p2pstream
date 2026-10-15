package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
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
		INSERT INTO public_cache_entries (
			key_digest, rule_id, scope, listener_protocol, host, path, query_key, method,
			vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at
		) VALUES ('prepared-batch-key', 1, 'selected_backend', 'http', 'example.test', '/asset', '', 'GET',
			'[]', '{}', 200, '/tmp/body', 10, ?, ?)
	`, storedAt, storedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	events := []db.InsertProxyRequestEventAtParams{
		{OccurredAt: storedAt, StatusCode: http.StatusOK, DurationMs: 10, Method: "GET", Host: "example.test", PathPrefix: "/a", ResponseBytes: 10},
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
	rows, err := app.DB.QueryContext(ctx, `SELECT status_code, method, path_prefix FROM proxy_request_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for index, want := range events {
		if !rows.Next() {
			t.Fatalf("raw event %d is missing", index)
		}
		var status int64
		var method, path string
		if err := rows.Scan(&status, &method, &path); err != nil {
			t.Fatal(err)
		}
		if status != want.StatusCode || method != want.Method || path != want.PathPrefix {
			t.Fatalf("raw event %d = %d/%q/%q, want %d/%q/%q", index, status, method, path, want.StatusCode, want.Method, want.PathPrefix)
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
