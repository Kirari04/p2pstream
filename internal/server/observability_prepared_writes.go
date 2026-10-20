package server

import (
	"context"
	"database/sql"
	"errors"
)

// observabilityPreparedWrites belongs to one flush transaction and is used only
// for its four generated rollup upserts and cache touches. Each SQL statement
// is prepared once even when the flush contains many distinct rollup keys.
// Embedding the transaction preserves DBTX's other methods for generated code;
// all execution and preparation retain the caller's cancellation context.
type observabilityPreparedWrites struct {
	*sql.Tx
	statements map[string]*sql.Stmt
}

func (w *observabilityPreparedWrites) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	stmt := w.statements[query]
	if stmt == nil {
		var err error
		stmt, err = w.Tx.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		if w.statements == nil {
			w.statements = make(map[string]*sql.Stmt, 5)
		}
		w.statements[query] = stmt
	}
	return stmt.ExecContext(ctx, args...)
}

func (w *observabilityPreparedWrites) Close() error {
	var err error
	for query, stmt := range w.statements {
		err = errors.Join(err, stmt.Close())
		delete(w.statements, query)
	}
	return err
}
