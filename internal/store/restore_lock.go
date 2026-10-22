package store

import (
	"context"
	"errors"
)

// OpenRestore opens an existing store with exclusion from serving PostgreSQL controllers.
// SQLite callers must also hold the controller's filesystem lock.
func OpenRestore(ctx context.Context, dialect Dialect, target string, opts ...Option) (*Store, error) {
	s, err := OpenExisting(ctx, dialect, target, opts...)
	if err != nil || dialect != DialectPostgres {
		return s, err
	}
	var held bool
	err = s.conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended('sparkwing.controller:' || current_database() || '/' || `+postgresStoreSchema+`, 0))`).Scan(&held)
	if err == nil && !held {
		err = errors.New("restore requires stopping every controller connected to this database")
	}
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}
