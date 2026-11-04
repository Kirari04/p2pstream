package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"p2pstream/internal/db"
)

func TestPreparedObservabilityWritesMatchGeneratedQueries(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		events       int
		validTouches int
	}{
		{name: "all_singleton", events: 1, validTouches: 1},
		{name: "multiple_keys_and_touches", events: 67, validTouches: 3},
		{name: "touches_only", validTouches: 3},
		{name: "singleton_with_invalid_touches", events: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			testPreparedObservabilityWritesMatchGeneratedQueries(t, scenario.events, scenario.validTouches)
		})
	}
}

func testPreparedObservabilityWritesMatchGeneratedQueries(t *testing.T, eventCount, validTouchCount int) {
	t.Helper()
	prepared := NewApp(nil, newServerTestDB(t))
	generated := NewApp(nil, newServerTestDB(t))
	storedAt := time.Date(2026, 10, 17, 9, 0, 0, 123456789, time.UTC)
	for _, app := range []*App{prepared, generated} {
		seedPreparedWritesFixture(t, app, storedAt)
	}
	events := preparedWritesFixtureEvents(storedAt, eventCount+1)[1:]
	touches := []db.TouchPublicCacheEntryParams{
		{KeyDigest: "prepared-writes-key", StoredAt: storedAt, LastAccessedAt: storedAt.Add(4 * time.Minute), HitCount: 3},
		{KeyDigest: "prepared-writes-key", StoredAt: storedAt, LastAccessedAt: storedAt.Add(time.Minute), HitCount: 2},
		{KeyDigest: "prepared-writes-key", StoredAt: storedAt.Add(time.Nanosecond), LastAccessedAt: storedAt.Add(5 * time.Minute), HitCount: 100},
	}
	touches = append(touches[:validTouchCount:validTouchCount],
		db.TouchPublicCacheEntryParams{KeyDigest: "prepared-writes-key", StoredAt: storedAt, LastAccessedAt: storedAt.Add(6 * time.Minute), HitCount: -1},
		db.TouchPublicCacheEntryParams{StoredAt: storedAt, LastAccessedAt: storedAt.Add(6 * time.Minute), HitCount: 100},
		db.TouchPublicCacheEntryParams{KeyDigest: "prepared-writes-key", LastAccessedAt: storedAt.Add(6 * time.Minute), HitCount: 100},
		db.TouchPublicCacheEntryParams{KeyDigest: "prepared-writes-key", StoredAt: storedAt, HitCount: 100},
	)
	// Repeat across fresh transactions to exercise both insert and conflict
	// updates, including monotone cache access times and exact stored_at guards.
	for round := range 2 {
		if err := prepared.insertProxyRequestEventsWithRollupsAndCacheTouches(t.Context(), events, touches); err != nil {
			t.Fatal(err)
		}
		if err := insertGeneratedObservabilityFixture(t.Context(), generated, events, touches); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{
			"proxy_request_events", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes",
			"proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes", "public_cache_entries",
		} {
			got := preparedWritesTableSnapshot(t, prepared, table)
			want := preparedWritesTableSnapshot(t, generated, table)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d table %s differs:\nprepared %v\ngenerated %v", round, table, got, want)
			}
		}
	}
}

func TestPreparedObservabilityWritesCancellationDuringRollupRollsBack(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	app.DB.SetMaxOpenConns(1)
	storedAt := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	seedPreparedWritesFixture(t, app, storedAt)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	conn, err := app.DB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(driverConn any) error {
		return driverConn.(*sqlite3.SQLiteConn).RegisterFunc("block_prepared_rollup", func() int64 {
			select {
			case <-entered:
			default:
				close(entered)
			}
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
	if _, err := app.DB.ExecContext(t.Context(), `
		CREATE TRIGGER block_prepared_rollup_insert AFTER INSERT ON proxy_request_tuple_rollup_minutes
		BEGIN SELECT block_prepared_rollup(); END;
	`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), observabilityRecorderFlushTimeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, preparedWritesFixtureEvents(storedAt, 67), []db.TouchPublicCacheEntryParams{
			{KeyDigest: "prepared-writes-key", StoredAt: storedAt, LastAccessedAt: storedAt.Add(time.Minute), HitCount: 3},
		})
	}()
	select {
	case <-entered:
		// Raw inserts and total rollups have executed on this transaction;
		// cancel while the prepared tuple statement is inside SQLite.
		cancel()
		unblock()
	case err := <-result:
		t.Fatalf("flush returned before rollup callback: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("flush did not reach rollup callback")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled rollup error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled rollup did not return")
	}
	for _, table := range []string{
		"proxy_request_events", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes",
		"proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes",
	} {
		var count int
		if err := app.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cancelled flush left %s rows=%d err=%v", table, count, err)
		}
	}
	var hits int
	if err := app.DB.QueryRowContext(t.Context(), `SELECT hit_count FROM public_cache_entries WHERE key_digest = 'prepared-writes-key'`).Scan(&hits); err != nil || hits != 0 {
		t.Fatalf("cancelled flush changed cache hits=%d err=%v", hits, err)
	}
}

func TestPreparedObservabilityWritesErrorRollsBackAndAllowsNextFlush(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	app.DB.SetMaxOpenConns(1)
	storedAt := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	seedPreparedWritesFixture(t, app, storedAt)
	events := preparedWritesFixtureEvents(storedAt, 67)
	touch := db.TouchPublicCacheEntryParams{
		KeyDigest: "prepared-writes-key", StoredAt: storedAt,
		LastAccessedAt: storedAt.Add(time.Minute), HitCount: 1,
	}
	touches := []db.TouchPublicCacheEntryParams{touch, touch}
	// Fail the second execution of the same cached statement, after every
	// event/rollup and the first touch have executed inside the transaction.
	if _, err := app.DB.ExecContext(t.Context(), `
		CREATE TRIGGER fail_reused_prepared_touch BEFORE UPDATE OF hit_count ON public_cache_entries
		WHEN NEW.hit_count > 1
		BEGIN SELECT RAISE(ABORT, 'forced reused statement failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(t.Context(), events, touches)
	var sqliteError sqlite3.Error
	if !errors.As(err, &sqliteError) || sqliteError.Code != sqlite3.ErrConstraint || !strings.Contains(err.Error(), "forced reused statement failure") {
		t.Fatalf("reused statement error = %v, want injected SQLite constraint failure", err)
	}
	for _, table := range []string{
		"proxy_request_events", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes",
		"proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes",
	} {
		var count int
		if err := app.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed flush left %s rows=%d err=%v", table, count, err)
		}
	}
	var hits int
	if err := app.DB.QueryRowContext(t.Context(), `SELECT hit_count FROM public_cache_entries WHERE key_digest = 'prepared-writes-key'`).Scan(&hits); err != nil || hits != 0 {
		t.Fatalf("failed flush left cache hits=%d err=%v", hits, err)
	}
	if _, err := app.DB.ExecContext(t.Context(), `DROP TRIGGER fail_reused_prepared_touch`); err != nil {
		t.Fatal(err)
	}
	if err := app.insertProxyRequestEventsWithRollupsAndCacheTouches(t.Context(), events, touches); err != nil {
		t.Fatalf("next flush after rollback: %v", err)
	}
	oracle := NewApp(nil, newServerTestDB(t))
	seedPreparedWritesFixture(t, oracle, storedAt)
	if err := insertGeneratedObservabilityFixture(t.Context(), oracle, events, touches); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"proxy_request_events", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes",
		"proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes", "public_cache_entries",
	} {
		if got, want := preparedWritesTableSnapshot(t, app, table), preparedWritesTableSnapshot(t, oracle, table); !reflect.DeepEqual(got, want) {
			t.Fatalf("recovered flush differs from generated queries for %s:\ngot %v\nwant %v", table, got, want)
		}
	}
}

func seedPreparedWritesFixture(t *testing.T, app *App, storedAt time.Time) {
	t.Helper()
	if _, err := app.DB.ExecContext(t.Context(), `
		INSERT INTO agents (id, public_id, name, token_hash) VALUES (1, 'prepared-1', 'prepared-1', 'hash-1'), (2, 'prepared-2', 'prepared-2', 'hash-2');
		INSERT INTO public_cache_rules (id, name) VALUES (1, 'prepared-writes-rule');
		INSERT INTO public_listeners (id, name, bind_address, port, protocol) VALUES (11, 'prepared-writes-listener', '127.0.0.1', 18112, 'http');
		INSERT INTO public_routes (id, listener_id, priority, host_pattern, path_prefix) VALUES (12, 11, 10, 'prepared.example', '/prepared');
		INSERT INTO public_route_targets (id, route_id, name, position) VALUES (13, 12, 'prepared-writes-target', 0);
		INSERT INTO public_waf_rules (id, name) VALUES (14, 'prepared-writes-waf');
		INSERT INTO public_retry_rules (id, name) VALUES (17, 'prepared-writes-retry');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.ExecContext(t.Context(), `
		INSERT INTO public_cache_entries (
			key_digest, rule_id, scope, listener_protocol, host, path, query_key, method,
			vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at, last_accessed_at
		) VALUES ('prepared-writes-key', 1, 'selected_backend', 'http', 'prepared.example', '/asset', '', 'GET',
			'[]', '{}', 200, '/tmp/body', 10, ?, ?, ?)
	`, storedAt, storedAt.Add(time.Hour), storedAt); err != nil {
		t.Fatal(err)
	}
}

func preparedWritesFixtureEvents(now time.Time, count int) []db.InsertProxyRequestEventAtParams {
	events := make([]db.InsertProxyRequestEventAtParams, count)
	statuses := []int64{200, 302, 404, 429, 500, 503}
	caches := []string{publicCacheStatusHit, publicCacheStatusMiss, publicCacheStatusBypass, publicCacheStatusStored, publicCacheStatusStoreFailed, ""}
	outcomes := []string{publicRetryOutcomeRecovered, publicRetryOutcomeExhausted, publicRetryOutcomeSkipped}
	for index := range events {
		event := db.InsertProxyRequestEventAtParams{
			OccurredAt: now.Add(time.Duration(index%3)*time.Minute + time.Duration(index)*time.Microsecond),
			StatusCode: statuses[index%len(statuses)], DurationMs: int64(1001 + index),
			ErrorKind: fmt.Sprintf("kind-%d", index%8), Method: "GET", Host: "prepared.example", PathPrefix: fmt.Sprintf("/path/%d", index),
			ListenerID: sql.NullInt64{Int64: 11, Valid: true}, RouteID: sql.NullInt64{Int64: 12, Valid: true},
			RouteTargetID: sql.NullInt64{Int64: 13, Valid: true}, WafRuleID: sql.NullInt64{Int64: 14, Valid: true}, WafAction: "allow",
			AgentID: sql.NullInt64{Int64: 1, Valid: true}, RequestBytes: int64(index * 3), ResponseBytes: int64(index * 17),
			CacheRuleID: sql.NullInt64{Int64: 1, Valid: true}, CacheStatus: caches[index%len(caches)], CacheBytes: int64(index * 11),
			RetryRuleID: sql.NullInt64{Int64: 17, Valid: true}, RetryCount: int64(index % 3), RetryOutcome: outcomes[index%len(outcomes)],
			RetryErrorKind: fmt.Sprintf("retry-%d", index%5), RetryFailedAgentID: sql.NullInt64{Int64: 2, Valid: true},
		}
		if index%4 == 0 {
			for _, id := range []*sql.NullInt64{&event.ListenerID, &event.RouteID, &event.RouteTargetID, &event.WafRuleID, &event.AgentID, &event.CacheRuleID, &event.RetryRuleID, &event.RetryFailedAgentID} {
				*id = sql.NullInt64{Int64: 999999}
			}
		}
		events[index] = event
	}
	return events
}

func insertGeneratedObservabilityFixture(ctx context.Context, app *App, events []db.InsertProxyRequestEventAtParams, touches []db.TouchPublicCacheEntryParams) error {
	tx, err := app.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := app.DB.WithTx(tx)
	// This oracle deliberately uses generated queries directly, with no
	// statement cache or in-memory aggregation.
	for _, event := range events {
		if err := q.InsertProxyRequestEventAt(ctx, event); err != nil {
			return err
		}
		total, tuple, status := proxyRequestRollupParams(event)
		if err := q.UpsertProxyRequestRollupMinute(ctx, total); err != nil {
			return err
		}
		if err := q.UpsertProxyRequestTupleRollupMinute(ctx, tuple); err != nil {
			return err
		}
		if err := q.UpsertProxyRequestStatusRollupMinute(ctx, status); err != nil {
			return err
		}
		if retry, ok := proxyRetryRollupParams(event); ok {
			if err := q.UpsertProxyRetryRollupMinute(ctx, retry); err != nil {
				return err
			}
		}
	}
	for _, touch := range touches {
		if touch.KeyDigest == "" || touch.StoredAt.IsZero() || touch.LastAccessedAt.IsZero() || touch.HitCount <= 0 {
			continue
		}
		if err := q.TouchPublicCacheEntry(ctx, touch); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func preparedWritesTableSnapshot(t *testing.T, app *App, table string) []string {
	t.Helper()
	rows, err := app.DB.QueryContext(t.Context(), "SELECT * FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		var row strings.Builder
		for index, column := range columns {
			// These timestamps use SQLite's wall clock independently in the
			// two databases; compare every other persisted column exactly.
			if column == "created_at" || column == "updated_at" {
				continue
			}
			value := values[index]
			if date, ok := value.(time.Time); ok {
				value = date.UTC().Format(time.RFC3339Nano)
			}
			fmt.Fprintf(&row, "%s=%#v;", column, value)
		}
		snapshot = append(snapshot, row.String())
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(snapshot)
	return snapshot
}
