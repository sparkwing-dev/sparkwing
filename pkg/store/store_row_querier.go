package store

import (
	"context"
	"database/sql"
)

type storeRowQuerier struct{ s *Store }

func (q storeRowQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.s.queryRow(ctx, query, args...)
}
