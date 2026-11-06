package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

func newProxyRequestStringDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "strings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(t.Context(), `
INSERT INTO public_listeners (id, name, bind_address, port, protocol) VALUES (11, 'strings', '127.0.0.1', 18111, 'http');
INSERT INTO public_routes (id, listener_id, priority, host_pattern, path_prefix) VALUES (12, 11, 10, '*', '/');
INSERT INTO public_route_targets (id, route_id, name, position) VALUES (13, 12, 'strings', 0);
INSERT INTO public_waf_rules (id, name) VALUES (14, 'strings');
INSERT INTO agents (id, public_id, name, token_hash) VALUES (15, 'strings', 'strings', 'hash'), (16, 'strings-failed', 'strings-failed', 'hash');
INSERT INTO public_cache_rules (id, name) VALUES (17, 'strings');
INSERT INTO public_retry_rules (id, name) VALUES (18, 'strings');`)
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func proxyRequestStringEvents(count int, mode string) []InsertProxyRequestEventAtParams {
	events := make([]InsertProxyRequestEventAtParams, count)
	at := time.Date(2026, 10, 18, 11, 0, 0, 123456789, time.UTC)
	for n := range events {
		value := "repeat"
		switch mode {
		case "changing":
			value = fmt.Sprintf("value-%d", n)
		case "alternating":
			value = []string{"alpha", "beta"}[n%2]
		case "edges":
			value = []string{"", strings.Repeat("x", 64), strings.Repeat("y", 65), strings.Repeat("界", 21) + "!", strings.Repeat("界", 21) + "!!", "a\x00b", "'); DROP TABLE proxy_request_events; --"}[n%7]
		case "long_between_short":
			if n%3 == 1 {
				value = strings.Repeat("z", 65)
			}
		}
		// Clone equivalent strings independently, as parsed public requests do.
		e := InsertProxyRequestEventAtParams{OccurredAt: at.Add(time.Duration(n) * time.Nanosecond), StatusCode: int64(200 + n%5), DurationMs: int64(n + 1), ErrorKind: fmt.Sprintf("error-%d", n%3), Method: strings.Clone(value), Host: strings.Clone(value), PathPrefix: strings.Clone(value), CacheStatus: strings.Clone(value), WafAction: "challenge", RequestBytes: int64(n + 123), ResponseBytes: int64(n + 456), CacheBytes: int64(n + 789), RetryCount: int64(n % 4), RetryOutcome: "recovered", RetryErrorKind: "reset"}
		if n%2 == 0 {
			e.ListenerID = sql.NullInt64{Int64: 11, Valid: true}
			e.RouteID = sql.NullInt64{Int64: 12, Valid: true}
			e.RouteTargetID = sql.NullInt64{Int64: 13, Valid: true}
			e.WafRuleID = sql.NullInt64{Int64: 14, Valid: true}
			e.AgentID = sql.NullInt64{Int64: 15, Valid: true}
			e.RetryFailedAgentID = sql.NullInt64{Int64: 16, Valid: true}
			e.CacheRuleID = sql.NullInt64{Int64: 17, Valid: true}
			e.RetryRuleID = sql.NullInt64{Int64: 18, Valid: true}
		} else {
			e.ListenerID = sql.NullInt64{Int64: 99999}
		}
		// Independently varying fields catch accidental sharing between slots.
		if n%5 == 0 {
			e.Host = strings.Clone("host-only")
		}
		if n%7 == 0 {
			e.CacheStatus = ""
		}
		if n%11 == 0 {
			e.PathPrefix = "path-only"
		}
		events[n] = e
	}
	return events
}

func proxyRequestStringSnapshot(t *testing.T, tx *sql.Tx) [][]any {
	t.Helper()
	rows, err := tx.QueryContext(t.Context(), `SELECT * FROM proxy_request_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := make([][]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for n := range values {
			pointers[n] = &values[n]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for n, v := range values {
			if b, ok := v.([]byte); ok {
				values[n] = append([]byte(nil), b...)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProxyRequestEventBatchStringsMatchGenerated(t *testing.T) {
	database := newProxyRequestStringDB(t)
	for _, mode := range []string{"repeated", "changing", "alternating", "edges", "long_between_short"} {
		for _, count := range []int{0, 1, 15, 16, 17, 31, 32, 35, 127, 128, 512} {
			t.Run(fmt.Sprintf("%s/%d", mode, count), func(t *testing.T) {
				events := proxyRequestStringEvents(count, mode)
				probe := func(batch bool) [][]any {
					tx, err := database.BeginTx(t.Context(), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					if batch {
						inserter, err := PrepareProxyRequestEventInserter(t.Context(), tx)
						if err != nil {
							t.Fatal(err)
						}
						defer inserter.Close()
						// Two calls on the same inserter ensure cached strings do not
						// become statement lifetime state. Include single Insert too.
						for range 2 {
							if err := inserter.InsertBatch(t.Context(), events); err != nil {
								t.Fatal(err)
							}
						}
						if len(events) > 0 {
							if err := inserter.Insert(t.Context(), events[0]); err != nil {
								t.Fatal(err)
							}
						}
					} else {
						for range 2 {
							for _, e := range events {
								if err := database.WithTx(tx).InsertProxyRequestEventAt(t.Context(), e); err != nil {
									t.Fatal(err)
								}
							}
						}
						if len(events) > 0 {
							if err := database.WithTx(tx).InsertProxyRequestEventAt(t.Context(), events[0]); err != nil {
								t.Fatal(err)
							}
						}
					}
					return proxyRequestStringSnapshot(t, tx)
				}
				want := probe(false)
				got := probe(true)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("all-column ordered batch rows differ from generated SQL: got %v; want %v", got, want)
				}
				wantCount := count * 2
				if count > 0 {
					wantCount++
				}
				if len(got) != wantCount {
					t.Fatalf("rows=%d want=%d", len(got), wantCount)
				}
			})
		}
	}
}

func TestProxyRequestEventBatchStringsRollbackAndRecover(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_%v", canceled), func(t *testing.T) {
			database := newProxyRequestStringDB(t)
			database.SetMaxOpenConns(1)
			events := proxyRequestStringEvents(35, "long_between_short")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			trigger := `CREATE TRIGGER fail_string_batch AFTER INSERT ON proxy_request_events WHEN NEW.duration_ms = 17 BEGIN SELECT RAISE(ABORT,'string batch failure'); END;`
			if canceled {
				conn, err := database.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(*sqlite3.SQLiteConn).RegisterFunc("block_string_batch", func() int64 {
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
				trigger = `CREATE TRIGGER fail_string_batch AFTER INSERT ON proxy_request_events WHEN NEW.duration_ms = 17 BEGIN SELECT block_string_batch(); END;`
			}
			if _, err := database.ExecContext(t.Context(), trigger); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tx, err := database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			inserter, err := PrepareProxyRequestEventInserter(ctx, tx)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { result <- inserter.InsertBatch(ctx, events) }()
			if canceled {
				select {
				case <-entered:
					cancel()
					unblock()
				case err := <-result:
					t.Fatalf("returned before blocked second group: %v", err)
				case <-time.After(2 * time.Second):
					t.Fatal("second group not reached")
				}
			}
			select {
			case err = <-result:
			case <-time.After(4 * time.Second):
				t.Fatal("batch did not return")
			}
			if canceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v want cancellation", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "string batch failure") {
				t.Fatalf("error=%v want trigger failure", err)
			}
			_ = inserter.Close()
			_ = tx.Rollback()
			var count int
			if err := database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rollback rows=%d err=%v", count, err)
			}
			if _, err := database.ExecContext(t.Context(), `DROP TRIGGER fail_string_batch`); err != nil {
				t.Fatal(err)
			}
			tx, err = database.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			inserter, err = PrepareProxyRequestEventInserter(t.Context(), tx)
			if err != nil {
				t.Fatal(err)
			}
			defer inserter.Close()
			if err := inserter.InsertBatch(t.Context(), events); err != nil {
				t.Fatal(err)
			}
			if got := proxyRequestStringSnapshot(t, tx); len(got) != 35 {
				t.Fatalf("recovery rows=%d want35", len(got))
			}
		})
	}
}
