package store

import (
	"context"
	"fmt"
)

// Connection names the database a store connection reached. Target is where
// the connection actually landed: the server address, port, database and
// schema on PostgreSQL, the file on SQLite. Created is when the store's
// baseline was written, which a restored copy shares. A break-glass tool
// compares both with what the serving controller recorded, so it refuses a
// store reached at another target, or one created at another time.
type Connection struct {
	Target  string `json:"target"`
	Created int64  `json:"created"`
}

// Connection reads where this store's connection landed.
func (s *Store) Connection(ctx context.Context) (Connection, error) {
	var id Connection
	if err := s.queryRow(ctx, `SELECT applied_at FROM sparkwing_schema_version WHERE version = ?`, expectedSchemaVersion).Scan(&id.Created); err != nil {
		return Connection{}, fmt.Errorf("read store identity: %w", err)
	}
	query := `SELECT 'sqlite:' || file FROM pragma_database_list WHERE name = 'main'`
	if s.dialect == DialectPostgres {
		query = `SELECT 'postgres://' || COALESCE(host(inet_server_addr()), 'local') || ':' || COALESCE(inet_server_port()::text, '') || '/' || current_database() || '/' || current_schema()`
	}
	if err := s.queryRow(ctx, query).Scan(&id.Target); err != nil {
		return Connection{}, fmt.Errorf("read store identity: %w", err)
	}
	return id, nil
}
