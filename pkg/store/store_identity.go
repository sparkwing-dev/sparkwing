package store

import (
	"context"
	"fmt"
)

// StoreIdentity names the database this store keeps its state in: the instant
// its baseline was created and, on PostgreSQL, the database and schema. A
// copy restored from a backup shares it; any other database does not. A
// break-glass tool compares it with what the serving controller recorded,
// so it acts on that controller's store and no other.
func (s *Store) StoreIdentity(ctx context.Context) (string, error) {
	var created int64
	if err := s.db.QueryRowContext(ctx, `SELECT applied_at FROM sparkwing_schema_version WHERE version = ?`, expectedSchemaVersion).Scan(&created); err != nil {
		return "", fmt.Errorf("read store identity: %w", err)
	}
	if s.dialect != DialectPostgres {
		return fmt.Sprintf("sqlite/%d", created), nil
	}
	var where string
	if err := s.db.QueryRowContext(ctx, `SELECT current_database() || '/' || current_schema()`).Scan(&where); err != nil {
		return "", fmt.Errorf("read store identity: %w", err)
	}
	return fmt.Sprintf("postgres/%s/%d", where, created), nil
}
