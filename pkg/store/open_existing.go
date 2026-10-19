package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// OpenExisting opens a store a controller already created and never creates
// one: a missing SQLite file, or a database without this build's schema, is
// an error rather than a fresh baseline. Break-glass tools use it, so a
// mistyped target fails instead of acting on an empty store.
func OpenExisting(ctx context.Context, dialect Dialect, target string) (*Store, error) {
	switch dialect {
	case DialectSQLite:
		if _, err := os.Lstat(target); err != nil {
			return nil, fmt.Errorf("open existing store: %w; run this where the controller keeps its state", err)
		}
		if err := requireCurrentSchema(ctx, "sqlite", sqliteURI(target)+"?mode=ro", target); err != nil {
			return nil, err
		}
		return Open(target) //nolint:contextcheck // Open owns its bounded migration context, and the schema is already current so it creates nothing.
	case DialectPostgres:
		if target == "" {
			return nil, errors.New("open existing store: no PostgreSQL connection string")
		}
		if err := requireCurrentSchema(ctx, "pgx", target, "the PostgreSQL database"); err != nil {
			return nil, err
		}
		return OpenPostgres(ctx, target)
	}
	return nil, fmt.Errorf("open existing store: unknown dialect %v", dialect)
}

func requireCurrentSchema(ctx context.Context, driver, dsn, label string) (err error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	exists := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sparkwing_schema_version'`
	if driver == "pgx" {
		exists = `SELECT COUNT(*) FROM (SELECT to_regclass('sparkwing_schema_version') AS t) r WHERE r.t IS NOT NULL`
	}
	var n, version int
	if err := db.QueryRowContext(ctx, exists).Scan(&n); err != nil {
		return fmt.Errorf("open existing store %s: %w", label, err)
	}
	if n > 0 {
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`).Scan(&version); err != nil {
			return fmt.Errorf("open existing store %s: %w", label, err)
		}
	}
	if version != expectedSchemaVersion {
		return fmt.Errorf("open existing store: %s holds no Sparkwing store at schema version %d (found %d); point this at the controller's own store", label, expectedSchemaVersion, version)
	}
	return nil
}
