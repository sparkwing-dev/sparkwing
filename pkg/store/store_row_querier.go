package store

import (
	"context"
)

type storeRowQuerier struct{ s *Store }

func (q storeRowQuerier) QueryRowContext(ctx context.Context, query string, args ...any) rowScanner {
	return q.s.queryRow(ctx, query, args...)
}
