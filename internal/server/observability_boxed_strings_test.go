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

// Identical overlay in both ordinary binaries. The first six cases reuse the
// existing full recorder fixtures, including their production cancellation path.
func BenchmarkPreparedProxyRequestEventStringBoxing(b *testing.B) {
	for _, deadline := range []bool{false, true} {
		name := "diverse512_background"
		if deadline {
			name = "diverse512_deadline"
		}
		b.Run(name, func(b *testing.B) { benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, deadline, 512, true) })
	}
	for _, count := range []int{1, 16, 127, 512} {
		b.Run(fmt.Sprintf("lowcard%d_deadline", count), func(b *testing.B) { benchmarkPreparedProxyRequestEventBatchFlushCardinality(b, true, count, false) })
	}
	for _, kind := range []string{"highshort", "long65"} {
		for _, count := range []int{127, 512} {
			b.Run(fmt.Sprintf("%s%d_deadline", kind, count), func(b *testing.B) { benchmarkPreparedProxyRequestEventStringMissFlush(b, count, kind == "long65") })
		}
	}
}

func benchmarkPreparedProxyRequestEventStringMissFlush(b *testing.B, count int, long bool) {
	database, err := db.Open(filepath.Join(b.TempDir(), "string-misses.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	app := NewApp(nil, database)
	recorder := newObservabilityRecorder(app)
	now := time.Now().UTC()
	events := make([]db.InsertProxyRequestEventAtParams, count)
	for n := range events {
		values := [4]string{}
		for field := range values {
			values[field] = fmt.Sprintf("field%d-event%04d", field, n)
			if long {
				values[field] += strings.Repeat("x", 65-len(values[field]))
			}
		}
		events[n] = db.InsertProxyRequestEventAtParams{OccurredAt: now, StatusCode: 200, DurationMs: int64(n % 250), Method: values[0], Host: values[1], PathPrefix: values[2], CacheStatus: values[3]}
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
