package server

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"p2pstream/internal/db"
)

func TestObservabilityRecorderReleasesCommittedBatchReferences(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	seedDashboardRollupDimensionFixtures(t, app.DB)
	if _, err := app.DB.ExecContext(t.Context(), `
		INSERT INTO public_waf_rules (id, name) VALUES (1, 'batch-retention-waf');
		INSERT INTO public_cache_rules (id, name) VALUES (1, 'batch-retention-cache');
		INSERT INTO public_retry_rules (id, name) VALUES (1, 'batch-retention-retry');
	`); err != nil {
		t.Fatal(err)
	}
	recorder := newObservabilityRecorder(app)
	at := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	newEvent := func(index int) db.InsertProxyRequestEventAtParams {
		return db.InsertProxyRequestEventAtParams{
			OccurredAt: at.Add(time.Duration(index) * time.Millisecond),
			StatusCode: 503, DurationMs: int64(index + 1),
			ErrorKind: "retained-error", Method: "PATCH",
			Host:       fmt.Sprintf("event-%d.example", index),
			PathPrefix: fmt.Sprintf("/event-%d", index),
			ListenerID: sqlNullInt64(1), RouteID: sqlNullInt64(1), RouteTargetID: sqlNullInt64(1),
			WafRuleID: sqlNullInt64(1), WafAction: "block", AgentID: sqlNullInt64(1),
			RequestBytes: int64(index + 2), ResponseBytes: int64(index + 3),
			CacheRuleID: sqlNullInt64(1), CacheStatus: publicCacheStatusStoreFailed, CacheBytes: int64(index + 4),
			RetryRuleID: sqlNullInt64(1), RetryCount: 1, RetryOutcome: publicRetryOutcomeExhausted,
			RetryErrorKind: "retained-retry-error", RetryFailedAgentID: sqlNullInt64(1),
		}
	}
	batch := make([]db.InsertProxyRequestEventAtParams, observabilityRecorderMaxBatch)
	for index := range batch {
		batch[index] = newEvent(index)
	}
	backing := batch[:cap(batch)]
	first := &backing[0]
	want := append([]db.InsertProxyRequestEventAtParams(nil), batch...)
	recorder.pendingEvents.Store(int64(len(batch)))
	failure := errors.New("injected first batch failure")
	insertBatch := recorder.insertBatch
	attempts := 0
	recorder.insertBatch = func(ctx context.Context, events []db.InsertProxyRequestEventAtParams, touches []db.TouchPublicCacheEntryParams) error {
		attempts++
		if attempts <= 2 && !reflect.DeepEqual(events, want) {
			t.Fatal("failed batch payload changed before retry")
		}
		if attempts == 1 {
			return failure
		}
		return insertBatch(ctx, events, touches)
	}
	assertPersisted := func(expected []db.InsertProxyRequestEventAtParams) {
		t.Helper()
		rows, err := app.DB.QueryContext(t.Context(), `
			SELECT occurred_at, status_code, duration_ms, error_kind, method, host, path_prefix,
				listener_id, route_id, route_target_id, waf_rule_id, waf_action, agent_id,
				request_bytes, response_bytes, cache_rule_id, cache_status, cache_bytes,
				retry_rule_id, retry_count, retry_outcome, retry_error_kind, retry_failed_agent_id
			FROM proxy_request_events ORDER BY id
		`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var duration, requestBytes, responseBytes int64
		for index, event := range expected {
			if !rows.Next() {
				t.Fatalf("missing raw event %d", index)
			}
			var got db.InsertProxyRequestEventAtParams
			if err := rows.Scan(
				&got.OccurredAt, &got.StatusCode, &got.DurationMs, &got.ErrorKind, &got.Method, &got.Host, &got.PathPrefix,
				&got.ListenerID, &got.RouteID, &got.RouteTargetID, &got.WafRuleID, &got.WafAction, &got.AgentID,
				&got.RequestBytes, &got.ResponseBytes, &got.CacheRuleID, &got.CacheStatus, &got.CacheBytes,
				&got.RetryRuleID, &got.RetryCount, &got.RetryOutcome, &got.RetryErrorKind, &got.RetryFailedAgentID,
			); err != nil {
				t.Fatal(err)
			}
			if !got.OccurredAt.Equal(event.OccurredAt) {
				t.Fatalf("raw event %d timestamp = %s, want %s", index, got.OccurredAt, event.OccurredAt)
			}
			got.OccurredAt = event.OccurredAt
			if !reflect.DeepEqual(got, event) {
				t.Fatalf("raw event %d = %+v, want %+v", index, got, event)
			}
			duration += event.DurationMs
			requestBytes += event.RequestBytes
			responseBytes += event.ResponseBytes
		}
		if rows.Next() {
			t.Fatal("unexpected extra raw event")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		var gotRequests, gotDuration, gotRequestBytes, gotResponseBytes int64
		if err := app.DB.QueryRowContext(t.Context(), `
			SELECT COALESCE(SUM(requests), 0), COALESCE(SUM(duration_ms_sum), 0),
				COALESCE(SUM(request_bytes), 0), COALESCE(SUM(response_bytes), 0)
			FROM proxy_request_rollup_minutes
		`).Scan(&gotRequests, &gotDuration, &gotRequestBytes, &gotResponseBytes); err != nil {
			t.Fatal(err)
		}
		if gotRequests != int64(len(expected)) || gotDuration != duration || gotRequestBytes != requestBytes || gotResponseBytes != responseBytes {
			t.Fatalf("rollup totals = %d/%d/%d/%d, want %d/%d/%d/%d", gotRequests, gotDuration, gotRequestBytes, gotResponseBytes, len(expected), duration, requestBytes, responseBytes)
		}
		for _, table := range []string{"proxy_request_tuple_rollup_minutes", "proxy_request_status_rollup_minutes"} {
			var requests int64
			if err := app.DB.QueryRowContext(t.Context(), "SELECT COALESCE(SUM(requests), 0) FROM "+table).Scan(&requests); err != nil || requests != int64(len(expected)) {
				t.Fatalf("%s requests = %d, want %d: %v", table, requests, len(expected), err)
			}
		}
		var matched, retryAttempts int64
		if err := app.DB.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(matched_requests), 0), COALESCE(SUM(retry_attempts), 0) FROM proxy_retry_rollup_minutes`).Scan(&matched, &retryAttempts); err != nil || matched != int64(len(expected)) || retryAttempts != int64(len(expected)) {
			t.Fatalf("retry rollups = %d/%d, want %d/%d: %v", matched, retryAttempts, len(expected), len(expected), err)
		}
	}
	assertReleased := func() {
		t.Helper()
		if len(batch) != 0 || cap(batch) != observabilityRecorderMaxBatch || &batch[:cap(batch)][0] != first || recorder.pendingEvents.Load() != 0 {
			t.Fatalf("committed batch was not reset with reusable capacity: len=%d cap=%d pending=%d", len(batch), cap(batch), recorder.pendingEvents.Load())
		}
		for index, event := range backing {
			if event != (db.InsertProxyRequestEventAtParams{}) {
				t.Fatalf("committed backing slot %d still holds event data", index)
			}
		}
	}
	if err := recorder.flushBatch(t.Context(), &batch, nil, at, true); !errors.Is(err, failure) {
		t.Fatalf("first flush error = %v, want injected failure", err)
	}
	if attempts != 1 || !reflect.DeepEqual(batch, want) || cap(batch) != len(backing) || &batch[0] != first || recorder.pendingEvents.Load() != int64(len(want)) {
		t.Fatal("failed flush changed the retry batch, capacity or pending count")
	}
	assertPersisted(nil)
	if err := recorder.flushBatch(t.Context(), &batch, nil, at, true); err != nil {
		t.Fatalf("retry flush: %v", err)
	}
	assertReleased()
	assertPersisted(want)

	later := []db.InsertProxyRequestEventAtParams{newEvent(512), newEvent(513), newEvent(514)}
	batch = append(batch, later...)
	recorder.pendingEvents.Store(int64(len(batch)))
	if err := recorder.flushBatch(t.Context(), &batch, nil, at.Add(time.Second), true); err != nil {
		t.Fatalf("smaller later batch: %v", err)
	}
	assertReleased()
	assertPersisted(append(want, later...))
	if attempts != 3 {
		t.Fatalf("insert attempts = %d, want 3", attempts)
	}
}
