package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/stdlib"
)

// OpenExisting opens a store a controller already created and never creates
// one: a missing SQLite file, or a database without this build's schema, is
// an error rather than a fresh baseline. Break-glass tools use it, so a
// mistyped target fails instead of acting on an empty store.
func OpenExisting(ctx context.Context, dialect Dialect, target string) (*Store, error) {
	driver, dsn := "pgx", target
	if dialect == DialectSQLite {
		if _, err := os.Lstat(target); err != nil {
			return nil, fmt.Errorf("open existing store: %w; run this where the controller keeps its state", err)
		}
		driver, dsn = "sqlite", fmt.Sprintf("%s?mode=rw&_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=foreign_keys(on)", sqliteURI(target), busyTimeoutMS)
	} else if dialect != DialectPostgres || target == "" {
		return nil, errors.New("open existing store: a known dialect and target are required")
	}
	var db *sql.DB
	if dialect == DialectPostgres {
		cfg, err := postgresConfig(dsn)
		if err != nil {
			return nil, err
		}
		db = stdlib.OpenDB(*cfg)
	} else {
		var err error
		db, err = sql.Open(driver, dsn)
		if err != nil {
			return nil, err
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	st := &Store{db: db, conn: conn, dialect: dialect, provisions: nativeProvisionPolicy{}}
	if err := requireCurrentSchema(ctx, conn, driver, target); err != nil {
		return nil, errors.Join(err, st.Close())
	}
	return st, nil
}

func requireCurrentSchema(ctx context.Context, conn *sql.Conn, driver, label string) error {
	exists := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sparkwing_schema_version'`
	if driver == "pgx" {
		exists = `SELECT COUNT(*) FROM (SELECT to_regclass('sparkwing_schema_version') AS t) r WHERE r.t IS NOT NULL`
	}
	var n, version int
	if err := conn.QueryRowContext(ctx, exists).Scan(&n); err != nil {
		return fmt.Errorf("open existing store %s: %w", label, err)
	}
	if n > 0 {
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM sparkwing_schema_version`).Scan(&version); err != nil {
			return fmt.Errorf("open existing store %s: %w", label, err)
		}
	}
	if version != expectedSchemaVersion {
		return fmt.Errorf("open existing store: %s holds no Sparkwing store at schema version %d (found %d); point this at the controller's own store", label, expectedSchemaVersion, version)
	}
	return nil
}
