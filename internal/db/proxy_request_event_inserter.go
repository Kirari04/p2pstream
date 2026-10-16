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
	tx        *sql.Tx
	stmt      *sql.Stmt
	batchStmt *sql.Stmt
}

const proxyRequestEventInsertBatchRows = 16

// Derive the fixed multirow statement from the generated single-row SQL so
// the column contract remains in sql/query.sql. Only the fixed VALUES tuple
// is repeated; request data is always bound through statement parameters.
var insertProxyRequestEventsBatch = func() string {
	prefix, values, ok := strings.Cut(insertProxyRequestEventAt, " VALUES ")
	if !ok {
		panic("generated proxy request event insert is missing VALUES")
	}
	tuple := strings.TrimSpace(values)
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
	if len(events) >= proxyRequestEventInsertBatchRows && i.batchStmt == nil {
		stmt, err := i.tx.PrepareContext(ctx, insertProxyRequestEventsBatch)
		if err != nil {
			return err
		}
		i.batchStmt = stmt
	}
	if len(events) >= proxyRequestEventInsertBatchRows {
		var storage [proxyRequestEventInsertBatchRows * 23]any
		for len(events) >= proxyRequestEventInsertBatchRows {
			args := storage[:0]
			for _, event := range events[:proxyRequestEventInsertBatchRows] {
				args = appendProxyRequestEventArgs(args, event)
			}
			if _, err := i.batchStmt.ExecContext(ctx, args...); err != nil {
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

func appendProxyRequestEventArgs(args []any, arg InsertProxyRequestEventAtParams) []any {
	return append(args,
		arg.OccurredAt,
		arg.StatusCode,
		arg.DurationMs,
		arg.ErrorKind,
		arg.Method,
		arg.Host,
		arg.PathPrefix,
		arg.ListenerID,
		arg.RouteID,
		arg.RouteTargetID,
		arg.WafRuleID,
		arg.WafAction,
		arg.AgentID,
		arg.RequestBytes,
		arg.ResponseBytes,
		arg.CacheRuleID,
		arg.CacheStatus,
		arg.CacheBytes,
		arg.RetryRuleID,
		arg.RetryCount,
		arg.RetryOutcome,
		arg.RetryErrorKind,
		arg.RetryFailedAgentID,
	)
}

func (i *ProxyRequestEventInserter) Close() error {
	if i == nil || i.stmt == nil {
		return nil
	}
	var batchErr error
	if i.batchStmt != nil {
		batchErr = i.batchStmt.Close()
	}
	return errors.Join(batchErr, i.stmt.Close())
}
