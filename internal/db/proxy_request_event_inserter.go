package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ProxyRequestEventInserter reuses SQLite statements for bounded groups of raw
// events and their remainder. SQL columns come from sql/query.sql; this wrapper
// binds the generated parameter type within the caller's transaction.
type ProxyRequestEventInserter struct {
	tx               *sql.Tx
	stmt             *sql.Stmt
	batchStmt        *sql.Stmt
	neutralBatchStmt *sql.Stmt
}

const proxyRequestEventInsertBatchRows = 16

// Assert the generated column order before assigning literal positions in the
// neutral shape. Counting columns alone would not catch a reordered generator.
const proxyRequestEventColumnContract = "INSERT INTO proxy_request_events ( occurred_at, status_code, duration_ms, error_kind, method, host, path_prefix, listener_id, route_id, route_target_id, waf_rule_id, waf_action, agent_id, request_bytes, response_bytes, cache_rule_id, cache_status, cache_bytes, retry_rule_id, retry_count, retry_outcome, retry_error_kind, retry_failed_agent_id )"

func proxyRequestEventInsertParts(query string) (string, string) {
	prefix, values, ok := strings.Cut(query, " VALUES ")
	statement, headerOK := strings.CutPrefix(prefix, "-- name: InsertProxyRequestEventAt :exec\n")
	if !headerOK {
		panic("generated proxy request event insert header changed")
	}
	if !ok || strings.Join(strings.Fields(statement), " ") != proxyRequestEventColumnContract {
		panic("generated proxy request event insert columns changed")
	}
	tuple := strings.TrimSpace(values)
	if strings.Join(strings.Fields(tuple), " ") != "( ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? )" {
		panic("generated proxy request event insert values changed")
	}
	return prefix, tuple
}

// Both shapes use the same fixed16 rows and generated columns. Only constant
// SQL is constructed here; every request-derived value remains a binding.
var insertProxyRequestEventsBatch = func() string {
	prefix, tuple := proxyRequestEventInsertParts(insertProxyRequestEventAt)
	return prefix + " VALUES " + strings.Repeat(tuple+",", proxyRequestEventInsertBatchRows-1) + tuple
}()

var insertProxyRequestEventsNeutralBatch = func() string {
	prefix, _ := proxyRequestEventInsertParts(insertProxyRequestEventAt)
	const tuple = "(?, ?, ?, '', ?, ?, ?, ?, ?, ?, NULL, '', NULL, ?, ?, NULL, ?, 0, NULL, 0, '', '', NULL)"
	return prefix + " VALUES " + strings.Repeat(tuple+",", proxyRequestEventInsertBatchRows-1) + tuple
}()

func PrepareProxyRequestEventInserter(ctx context.Context, tx *sql.Tx) (*ProxyRequestEventInserter, error) {
	if tx == nil {
		return nil, errors.New("proxy request event transaction is nil")
	}
	stmt, err := tx.PrepareContext(ctx, insertProxyRequestEventAt)
	if err != nil {
		return nil, err
	}
	return &ProxyRequestEventInserter{tx: tx, stmt: stmt}, nil
}

func (i *ProxyRequestEventInserter) Insert(ctx context.Context, arg InsertProxyRequestEventAtParams) error {
	if i == nil || i.stmt == nil {
		return errors.New("proxy request event inserter is not prepared")
	}
	var args [23]any
	_, err := i.stmt.ExecContext(ctx, appendProxyRequestEventArgs(args[:0], arg)...)
	return err
}

// InsertBatch amortizes SQLite execution and cancellation coordination over
// bounded groups. All groups and the short final remainder use the caller's
// existing transaction and cancellation context.
func (i *ProxyRequestEventInserter) InsertBatch(ctx context.Context, events []InsertProxyRequestEventAtParams) error {
	if i == nil || i.stmt == nil {
		return errors.New("proxy request event inserter is not prepared")
	}
	if len(events) >= proxyRequestEventInsertBatchRows {
		var storage [proxyRequestEventInsertBatchRows * 23]any
		var stringSlots [4]proxyRequestEventStringSlot
		for len(events) >= proxyRequestEventInsertBatchRows {
			group := events[:proxyRequestEventInsertBatchRows]
			neutral := proxyRequestEventsAreNeutral(group)
			statement, query := &i.batchStmt, insertProxyRequestEventsBatch
			if neutral {
				statement, query = &i.neutralBatchStmt, insertProxyRequestEventsNeutralBatch
			}
			if *statement == nil {
				stmt, err := i.tx.PrepareContext(ctx, query)
				if err != nil {
					return err
				}
				*statement = stmt
			}
			args := storage[:0]
			for _, event := range group {
				if neutral {
					args = appendProxyRequestEventNeutralBatchArgs(args, event, &stringSlots)
				} else {
					args = appendProxyRequestEventBatchArgs(args, event, &stringSlots)
				}
			}
			if _, err := (*statement).ExecContext(ctx, args...); err != nil {
				return err
			}
			events = events[proxyRequestEventInsertBatchRows:]
		}
	}
	for _, event := range events {
		if err := i.Insert(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// Check every original row without regrouping. Valid nullable zero values must
// use the general shape; invalid nullable payloads keep database/sql NULL semantics.
func proxyRequestEventsAreNeutral(events []InsertProxyRequestEventAtParams) bool {
	for _, event := range events {
		if event.ErrorKind != "" || event.WafRuleID.Valid || event.WafAction != "" || event.AgentID.Valid ||
			event.CacheRuleID.Valid || event.CacheBytes != 0 || event.RetryRuleID.Valid || event.RetryCount != 0 ||
			event.RetryOutcome != "" || event.RetryErrorKind != "" || event.RetryFailedAgentID.Valid {
			return false
		}
	}
	return true
}

func appendProxyRequestEventNeutralBatchArgs(args []any, arg InsertProxyRequestEventAtParams, slots *[4]proxyRequestEventStringSlot) []any {
	return append(args,
		arg.OccurredAt,
		arg.StatusCode,
		arg.DurationMs,
		slots[0].box(arg.Method),
		slots[1].box(arg.Host),
		slots[2].box(arg.PathPrefix),
		proxyRequestEventIDValue(arg.ListenerID),
		proxyRequestEventIDValue(arg.RouteID),
		proxyRequestEventIDValue(arg.RouteTargetID),
		arg.RequestBytes,
		arg.ResponseBytes,
		slots[3].box(arg.CacheStatus),
	)
}

// Each slot reuses only the last short immutable string's interface box. Slots
// belong to this InsertBatch call, never the inserter or a pooled object. The
// length check bounds comparisons even for attacker-controlled host/path data;
// long values neither compare against nor replace the retained short value.
type proxyRequestEventStringSlot struct {
	value string
	boxed any
}

func (s *proxyRequestEventStringSlot) box(value string) any {
	if len(value) > 64 {
		return value
	}
	if s.boxed == nil || value != s.value {
		s.value, s.boxed = value, value
	}
	return s.boxed
}

// Keep the single-row/remainder binder unchanged. Only full fixed-size groups
// share boxes, while every SQL value and its position remains identical.
func appendProxyRequestEventBatchArgs(args []any, arg InsertProxyRequestEventAtParams, slots *[4]proxyRequestEventStringSlot) []any {
	return append(args,
		arg.OccurredAt,
		arg.StatusCode,
		arg.DurationMs,
		arg.ErrorKind,
		slots[0].box(arg.Method),
		slots[1].box(arg.Host),
		slots[2].box(arg.PathPrefix),
		proxyRequestEventIDValue(arg.ListenerID),
		proxyRequestEventIDValue(arg.RouteID),
		proxyRequestEventIDValue(arg.RouteTargetID),
		proxyRequestEventIDValue(arg.WafRuleID),
		arg.WafAction,
		proxyRequestEventIDValue(arg.AgentID),
		arg.RequestBytes,
		arg.ResponseBytes,
		proxyRequestEventIDValue(arg.CacheRuleID),
		slots[3].box(arg.CacheStatus),
		arg.CacheBytes,
		proxyRequestEventIDValue(arg.RetryRuleID),
		arg.RetryCount,
		arg.RetryOutcome,
		arg.RetryErrorKind,
		proxyRequestEventIDValue(arg.RetryFailedAgentID),
	)
}

func appendProxyRequestEventArgs(args []any, arg InsertProxyRequestEventAtParams) []any {
	return append(args,
		arg.OccurredAt,
		arg.StatusCode,
		arg.DurationMs,
		arg.ErrorKind,
		arg.Method,
		arg.Host,
		arg.PathPrefix,
		proxyRequestEventIDValue(arg.ListenerID),
		proxyRequestEventIDValue(arg.RouteID),
		proxyRequestEventIDValue(arg.RouteTargetID),
		proxyRequestEventIDValue(arg.WafRuleID),
		arg.WafAction,
		proxyRequestEventIDValue(arg.AgentID),
		arg.RequestBytes,
		arg.ResponseBytes,
		proxyRequestEventIDValue(arg.CacheRuleID),
		arg.CacheStatus,
		arg.CacheBytes,
		proxyRequestEventIDValue(arg.RetryRuleID),
		arg.RetryCount,
		arg.RetryOutcome,
		arg.RetryErrorKind,
		proxyRequestEventIDValue(arg.RetryFailedAgentID),
	)
}

// proxyRequestEventIDValue preserves sql.NullInt64.Value semantics without
// boxing the nullable struct before database/sql converts it to nil or int64.
func proxyRequestEventIDValue(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func (i *ProxyRequestEventInserter) Close() error {
	if i == nil || i.stmt == nil {
		return nil
	}
	var batchErr, neutralErr error
	if i.batchStmt != nil {
		batchErr = i.batchStmt.Close()
	}
	if i.neutralBatchStmt != nil {
		neutralErr = i.neutralBatchStmt.Close()
	}
	return errors.Join(batchErr, neutralErr, i.stmt.Close())
}
