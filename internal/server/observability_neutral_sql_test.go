package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/db"
)

func TestPreparedProxyRequestNeutralBatchAtomicRowsRollupsAndTouches(t *testing.T) {
	for _, transition := range []string{"neutral-to-fallback", "fallback-to-neutral"} {
		t.Run(transition, func(t *testing.T) {
			app := NewApp(nil, newServerTestDB(t))
			at := time.Now().UTC().Round(0)
			if _, err := app.DB.ExecContext(t.Context(), `INSERT INTO public_cache_rules(id,name) VALUES(1,'neutral');
INSERT INTO public_cache_entries(key_digest,rule_id,scope,listener_protocol,host,path,query_key,method,vary_headers_json,response_headers_json,status_code,body_path,size_bytes,stored_at,expires_at)
VALUES('prepared-batch-key',1,'selected_backend','http','example.test','/asset','','GET','[]','{}',200,'/tmp/body',10,?,?);`, at, at.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			events := make([]db.InsertProxyRequestEventAtParams, 35)
			for n := range events {
				events[n] = db.InsertProxyRequestEventAtParams{OccurredAt: at, StatusCode: 200, DurationMs: int64(n + 1), Method: "GET", Host: "neutral.example", PathPrefix: fmt.Sprintf("/path/%d", n), RequestBytes: int64(n), ResponseBytes: int64(n + 100)}
				if transition == "neutral-to-fallback" && n >= 16 && n < 32 || transition == "fallback-to-neutral" && n < 16 {
					events[n].ErrorKind = "failure"
				}
			}
			touch := db.TouchPublicCacheEntryParams{KeyDigest: "prepared-batch-key", StoredAt: at, LastAccessedAt: at.Add(time.Minute), HitCount: 2}
			if _, err := app.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_neutral_touch BEFORE UPDATE OF hit_count ON public_cache_entries BEGIN SELECT RAISE(ABORT,'neutral touch failure'); END;`); err != nil {
				t.Fatal(err)
			}
			err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(t.Context(), events, []db.TouchPublicCacheEntryParams{touch})
			if err == nil || !strings.Contains(err.Error(), "neutral touch failure") {
				t.Fatalf("touch error=%v", err)
			}
			assertPreparedObservabilityBatchState(t, app, 0, 0, 0)
			for _, table := range []string{"proxy_request_tuple_rollup_minutes", "proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes"} {
				var count int
				if err := app.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rollback %s rows=%d err=%v", table, count, err)
				}
			}
			if _, err := app.DB.ExecContext(t.Context(), `DROP TRIGGER fail_neutral_touch`); err != nil {
				t.Fatal(err)
			}
			if err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(t.Context(), events, []db.TouchPublicCacheEntryParams{touch}); err != nil {
				t.Fatal(err)
			}
			assertPreparedObservabilityBatchState(t, app, 35, 35, 2)
			rows, err := app.DB.QueryContext(t.Context(), `SELECT duration_ms,error_kind,request_bytes,response_bytes FROM proxy_request_events ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for n, e := range events {
				if !rows.Next() {
					t.Fatalf("row%d missing", n)
				}
				var duration, request, response int64
				var kind string
				if err := rows.Scan(&duration, &kind, &request, &response); err != nil {
					t.Fatal(err)
				}
				if duration != e.DurationMs || kind != e.ErrorKind || request != e.RequestBytes || response != e.ResponseBytes {
					t.Fatalf("row%d differs", n)
				}
			}
			if rows.Next() || rows.Err() != nil {
				t.Fatalf("extra/error rows: %v", rows.Err())
			}
		})
	}
}

// Identical new benchmark overlay in both variants. Existing string-boxing
// benchmark and workloads remain unchanged and run alongside these fixed cases.
func BenchmarkPreparedProxyRequestEventNeutralSQL(b *testing.B) {
	for _, count := range []int{16, 127, 512} {
		b.Run(fmt.Sprintf("neutral%d_background", count), func(b *testing.B) { benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, false, count, false) })
	}
	b.Run("neutral32_deadline", func(b *testing.B) { benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, true, 32, false) })
	for _, count := range []int{16, 127} {
		b.Run(fmt.Sprintf("fallback%d_deadline", count), func(b *testing.B) { benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, true, count, true) })
	}
	for _, shape := range []string{"alternating", "last_disqualifier"} {
		for _, count := range []int{127, 512} {
			b.Run(fmt.Sprintf("%s%d_deadline", shape, count), func(b *testing.B) { benchmarkPreparedProxyRequestNeutralMixedFlush(b, count, shape) })
		}
	}
	b.Run("sparse127_deadline", func(b *testing.B) { benchmarkPreparedProxyRequestNeutralMixedFlush(b, 127, "sparse") })
	for _, count := range []int{15, 17, 31} {
		b.Run(fmt.Sprintf("remainder%d_deadline", count), func(b *testing.B) { benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, true, count, false) })
	}
}

func benchmarkPreparedProxyRequestNeutralMixedFlush(b *testing.B, count int, shape string) {
	database, err := db.Open(filepath.Join(b.TempDir(), "neutral-mixed.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	recorder := newObservabilityRecorder(NewApp(nil, database))
	now := time.Now().UTC()
	events := make([]db.InsertProxyRequestEventAtParams, count)
	for n := range events {
		events[n] = db.InsertProxyRequestEventAtParams{OccurredAt: now, StatusCode: 200, DurationMs: int64(n % 250), Method: "GET", Host: "benchmark.example", PathPrefix: "/route"}
		if shape == "alternating" && n/16%2 == 1 || shape == "last_disqualifier" && n%16 == 15 || shape == "sparse" && n == 63 {
			events[n].ErrorKind = "kind-0"
		}
	}
	batch := make([]db.InsertProxyRequestEventAtParams, 0, count)
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		batch = append(batch[:0], events...)
		if err := recorder.runBackgroundFlush(func(ctx context.Context) error { return recorder.flushBatch(ctx, &batch, touches, now, true) }); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		for _, table := range []string{"proxy_request_events", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes", "proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes"} {
			if _, err := database.ExecContext(context.Background(), "DELETE FROM "+table); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
	}
	b.ReportMetric(float64(count), "events/op")
}
