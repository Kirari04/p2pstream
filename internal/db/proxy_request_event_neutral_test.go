package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

func proxyRequestNeutralEvents(count int) []InsertProxyRequestEventAtParams {
	events := proxyRequestStringEvents(count, "edges")
	for n := range events {
		e := &events[n]
		e.ErrorKind, e.WafAction, e.RetryOutcome, e.RetryErrorKind = "", "", "", ""
		e.WafRuleID, e.AgentID, e.CacheRuleID, e.RetryRuleID, e.RetryFailedAgentID = sql.NullInt64{Int64: 991}, sql.NullInt64{Int64: -992}, sql.NullInt64{Int64: 993}, sql.NullInt64{Int64: -994}, sql.NullInt64{Int64: 995}
		e.CacheBytes, e.RetryCount = 0, 0
	}
	return events
}

func proxyRequestNeutralParity(t *testing.T, database *DB, calls ...[]InsertProxyRequestEventAtParams) {
	t.Helper()
	probe := func(batch bool) (string, [][]any) {
		tx, err := database.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var inserter *ProxyRequestEventInserter
		if batch {
			inserter, err = PrepareProxyRequestEventInserter(t.Context(), tx)
			if err != nil {
				t.Fatal(err)
			}
			defer inserter.Close()
		}
		for range 2 {
			for _, events := range calls {
				if batch {
					err = inserter.InsertBatch(t.Context(), events)
				} else {
					for _, event := range events {
						if err = database.WithTx(tx).InsertProxyRequestEventAt(t.Context(), event); err != nil {
							break
						}
					}
				}
				if err != nil {
					return fmt.Sprintf("%T:%v", err, err), nil
				}
			}
		}
		return "", proxyRequestStringSnapshot(t, tx)
	}
	wantErr, want := probe(false)
	gotErr, got := probe(true)
	if gotErr != wantErr || !reflect.DeepEqual(got, want) {
		t.Fatalf("all-column ordered SQL parity differs: generated=(%q,%v), inserter=(%q,%v)", wantErr, want, gotErr, got)
	}
}

func TestProxyRequestEventNeutralBatchMatchesGenerated(t *testing.T) {
	database := newProxyRequestStringDB(t)
	for _, count := range []int{0, 1, 15, 16, 17, 31, 32, 35, 127, 512} {
		for _, shape := range []string{"neutral", "fallback", "alternating-groups", "last-in-group"} {
			t.Run(fmt.Sprintf("%s/%d", shape, count), func(t *testing.T) {
				events := proxyRequestNeutralEvents(count)
				for n := range events {
					if shape == "fallback" || shape == "alternating-groups" && n/16%2 == 1 || shape == "last-in-group" && n%16 == 15 {
						events[n].ErrorKind = fmt.Sprintf("error-%d", n)
					}
				}
				before := append([]InsertProxyRequestEventAtParams(nil), events...)
				proxyRequestNeutralParity(t, database, events)
				if len(events) > 0 && !reflect.DeepEqual(events, before) {
					t.Fatal("input rows changed")
				}
			})
		}
	}
	neutral, fallback := proxyRequestNeutralEvents(16), proxyRequestNeutralEvents(16)
	fallback[15].ErrorKind = "fallback"
	t.Run("neutral-fallback-neutral-remainder-calls", func(t *testing.T) {
		proxyRequestNeutralParity(t, database, neutral, fallback, neutral, proxyRequestNeutralEvents(15))
	})
}

func TestProxyRequestEventNeutralBatchDisqualifiersMatchGenerated(t *testing.T) {
	database := newProxyRequestStringDB(t)
	disqualifiers := []struct {
		name   string
		change func(*InsertProxyRequestEventAtParams)
	}{
		{"error_kind", func(e *InsertProxyRequestEventAtParams) { e.ErrorKind = "failure" }},
		{"waf_rule", func(e *InsertProxyRequestEventAtParams) { e.WafRuleID = sql.NullInt64{Int64: 14, Valid: true} }},
		{"waf_action", func(e *InsertProxyRequestEventAtParams) { e.WafAction = "challenge" }},
		{"agent", func(e *InsertProxyRequestEventAtParams) { e.AgentID = sql.NullInt64{Int64: 15, Valid: true} }},
		{"cache_rule", func(e *InsertProxyRequestEventAtParams) { e.CacheRuleID = sql.NullInt64{Int64: 17, Valid: true} }},
		{"cache_bytes", func(e *InsertProxyRequestEventAtParams) { e.CacheBytes = -1 }},
		{"retry_rule", func(e *InsertProxyRequestEventAtParams) { e.RetryRuleID = sql.NullInt64{Int64: 18, Valid: true} }},
		{"retry_count", func(e *InsertProxyRequestEventAtParams) { e.RetryCount = -1 }},
		{"retry_outcome", func(e *InsertProxyRequestEventAtParams) { e.RetryOutcome = "exhausted" }},
		{"retry_error", func(e *InsertProxyRequestEventAtParams) { e.RetryErrorKind = "reset" }},
		{"retry_agent", func(e *InsertProxyRequestEventAtParams) { e.RetryFailedAgentID = sql.NullInt64{Int64: 16, Valid: true} }},
	}
	for _, d := range disqualifiers {
		for position := range 16 {
			t.Run(fmt.Sprintf("%s/%d", d.name, position), func(t *testing.T) {
				events := proxyRequestNeutralEvents(16)
				d.change(&events[position])
				proxyRequestNeutralParity(t, database, events)
			})
		}
	}
	for field := range 5 {
		for _, payload := range []sql.NullInt64{{Int64: 999999}, {Int64: 0, Valid: true}, {Int64: -1, Valid: true}, {Int64: 999999, Valid: true}} {
			for position := range 16 {
				t.Run(fmt.Sprintf("nullable_%d/%d_%v/%d", field, payload.Int64, payload.Valid, position), func(t *testing.T) {
					events := proxyRequestNeutralEvents(16)
					e := &events[position]
					ids := []*sql.NullInt64{&e.WafRuleID, &e.AgentID, &e.CacheRuleID, &e.RetryRuleID, &e.RetryFailedAgentID}
					*ids[field] = payload
					proxyRequestNeutralParity(t, database, events)
				})
			}
		}
	}
}

func TestProxyRequestEventNeutralBatchTransitionsRollbackAndRecover(t *testing.T) {
	for _, transition := range []string{"neutral-to-fallback", "fallback-to-neutral"} {
		for _, canceled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel_%v", transition, canceled), func(t *testing.T) {
				database := newProxyRequestStringDB(t)
				database.SetMaxOpenConns(1)
				events := proxyRequestNeutralEvents(35)
				for n := range events {
					events[n].DurationMs = int64(n + 1)
					if transition == "fallback-to-neutral" && n < 16 || transition == "neutral-to-fallback" && n >= 16 && n < 32 {
						events[n].ErrorKind = "fallback"
					}
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				trigger := `CREATE TRIGGER fail_neutral_batch AFTER INSERT ON proxy_request_events WHEN NEW.duration_ms=17 BEGIN SELECT RAISE(ABORT,'neutral batch failure'); END;`
				if canceled {
					conn, err := database.Conn(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					err = conn.Raw(func(raw any) error {
						return raw.(*sqlite3.SQLiteConn).RegisterFunc("block_neutral_batch", func() int64 {
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
					trigger = `CREATE TRIGGER fail_neutral_batch AFTER INSERT ON proxy_request_events WHEN NEW.duration_ms=17 BEGIN SELECT block_neutral_batch(); END;`
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
						t.Fatalf("returned before row17: %v", err)
					case <-time.After(2 * time.Second):
						t.Fatal("row17 not reached")
					}
				}
				select {
				case err = <-result:
				case <-time.After(4 * time.Second):
					t.Fatal("batch did not join")
				}
				if canceled {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("error=%v want cancellation", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "neutral batch failure") {
					t.Fatalf("error=%v want trigger failure", err)
				}
				_ = inserter.Close()
				_ = tx.Rollback()
				var count int
				if err := database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rollback rows=%d err=%v", count, err)
				}
				if _, err := database.ExecContext(t.Context(), `DROP TRIGGER fail_neutral_batch`); err != nil {
					t.Fatal(err)
				}
				proxyRequestNeutralParity(t, database, events)
			})
		}
	}
}

// Per-test Connector avoids process-wide registration. This driver observes only
// statement ownership/errors; the real SQLite tests above supply the SQL oracle.
type neutralStatementConnector struct{ statements []*neutralOwnedStatement }

func (c *neutralStatementConnector) Connect(context.Context) (driver.Conn, error) {
	return &neutralStatementConn{owner: c}, nil
}
func (c *neutralStatementConnector) Driver() driver.Driver { return neutralStatementDriver{} }

type neutralStatementDriver struct{}

func (neutralStatementDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector only")
}

type neutralStatementConn struct{ owner *neutralStatementConnector }

func (c *neutralStatementConn) Prepare(query string) (driver.Stmt, error) {
	s := &neutralOwnedStatement{query: query, closeErr: fmt.Errorf("close-%d", len(c.owner.statements))}
	c.owner.statements = append(c.owner.statements, s)
	return s, nil
}
func (*neutralStatementConn) Close() error              { return nil }
func (*neutralStatementConn) Begin() (driver.Tx, error) { return neutralStatementTx{}, nil }

type neutralStatementTx struct{}

func (neutralStatementTx) Commit() error   { return nil }
func (neutralStatementTx) Rollback() error { return nil }

type neutralOwnedStatement struct {
	query      string
	closeErr   error
	closes     int
	executions int
}

func (s *neutralOwnedStatement) Close() error  { s.closes++; return s.closeErr }
func (s *neutralOwnedStatement) NumInput() int { return strings.Count(s.query, "?") }
func (s *neutralOwnedStatement) Exec(args []driver.Value) (driver.Result, error) {
	s.executions++
	return driver.RowsAffected(1), nil
}
func (*neutralOwnedStatement) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("query not expected")
}

func TestProxyRequestEventNeutralBatchStatementOwnership(t *testing.T) {
	connector := &neutralStatementConnector{}
	database := sql.OpenDB(connector)
	defer database.Close()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	inserter, err := PrepareProxyRequestEventInserter(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(connector.statements) != 1 {
		t.Fatal("batch prepared eagerly")
	}
	if err := inserter.InsertBatch(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(connector.statements) != 1 {
		t.Fatal("empty batch prepared a group")
	}
	neutral, fallback := proxyRequestNeutralEvents(16), proxyRequestNeutralEvents(16)
	fallback[15].ErrorKind = "fallback"
	for _, events := range [][]InsertProxyRequestEventAtParams{neutral, fallback, neutral, proxyRequestNeutralEvents(1)} {
		if err := inserter.InsertBatch(t.Context(), events); err != nil {
			t.Fatal(err)
		}
	}
	count := len(connector.statements)
	if count < 2 || count > 3 {
		t.Fatalf("owned statements=%d want2 baseline or3 candidate", count)
	}
	err = inserter.Close()
	for _, statement := range connector.statements {
		if statement.closes != 1 || !errors.Is(err, statement.closeErr) {
			t.Fatalf("close count=%d joined=%v query args=%d", statement.closes, err, statement.NumInput())
		}
	}
	if err := inserter.Close(); err != nil {
		t.Fatalf("second close=%v", err)
	}
	for _, statement := range connector.statements {
		if statement.closes != 1 {
			t.Fatal("driver statement closed twice")
		}
	}
	t.Logf("owned SQL shapes=%d, all close errors joined once", count)
}
