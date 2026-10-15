package db

import (
	"context"
	"database/sql"
	"errors"
)

// ProxyRequestEventInserter reuses one SQLite statement for every raw event in
// an observability batch. The SQL remains generated from sql/query.sql; this
// wrapper only binds the generated parameter type to a transaction statement.
type ProxyRequestEventInserter struct {
	stmt *sql.Stmt
}

func PrepareProxyRequestEventInserter(ctx context.Context, tx *sql.Tx) (*ProxyRequestEventInserter, error) {
	if tx == nil {
		return nil, errors.New("proxy request event transaction is nil")
	}
	stmt, err := tx.PrepareContext(ctx, insertProxyRequestEventAt)
	if err != nil {
		return nil, err
	}
	return &ProxyRequestEventInserter{stmt: stmt}, nil
}

func (i *ProxyRequestEventInserter) Insert(ctx context.Context, arg InsertProxyRequestEventAtParams) error {
	if i == nil || i.stmt == nil {
		return errors.New("proxy request event inserter is not prepared")
	}
	_, err := i.stmt.ExecContext(ctx,
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
	return err
}

func (i *ProxyRequestEventInserter) Close() error {
	if i == nil || i.stmt == nil {
		return nil
	}
	return i.stmt.Close()
}
