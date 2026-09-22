package store

import (
	"context"
	"slices"
)

// TenantTablesForTest names the tenant-owned tables for the external
// test package, which needs them to reproduce the shape a store had
// before the tenant key was added.
func TenantTablesForTest() []string { return slices.Clone(tenantTables) }

// UserKeyTablesForTest names the tables v51 rebuilds, with the key each
// one carried before it.
func UserKeyTablesForTest() map[string][]string {
	return map[string][]string{
		"secrets":                 {"name", "pipeline"},
		"github_webhook_bindings": {"pipeline", "repo"},
		"pipeline_profiles":       {"pipeline", "node_id"},
		"concurrency_entries":     {"key"},
		"concurrency_holders":     {"key", "holder_id"},
		"concurrency_waiters":     {"key", "run_id", "node_id"},
		"concurrency_cache":       {"key", "cache_key_hash"},
	}
}

// RekeyForTest puts one table's primary key back to key, so a migration
// test can reproduce the shape v49 left behind and run v51 over rows
// that already exist. It is the widening helper driven backwards, so the
// test cannot pass against a rebuild the migration does not perform.
func RekeyForTest(ctx context.Context, s *Store, table string, key []string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	spec := teamScopedKey{table: table, key: key, replaceIndexes: preV51Indexes[table]}
	if s.dialect == DialectPostgres {
		err = widenPrimaryKeyPostgres(ctx, tx, spec)
	} else {
		err = widenPrimaryKeySQLite(ctx, tx, spec)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// safety: names the index definitions v51 replaced, so a test rekeying a
// table backwards leaves no index standing on a column it drops next.
var preV51Indexes = map[string]map[string]string{
	"concurrency_holders": {
		"idx_concurrency_holders_key_claimed": `CREATE INDEX IF NOT EXISTS idx_concurrency_holders_key_claimed
    ON concurrency_holders(key, claimed_at)`,
	},
	"concurrency_waiters": {
		"idx_concurrency_waiters_arrived": `CREATE INDEX IF NOT EXISTS idx_concurrency_waiters_arrived
    ON concurrency_waiters(key, arrived_at)`,
	},
}

// ApplyUserKeyMigrationForTest runs v51 against s as the ladder would.
func ApplyUserKeyMigrationForTest(ctx context.Context, s *Store) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if s.dialect == DialectPostgres {
		err = applyUserKeyTeamScopeMigrationPostgres(ctx, tx)
	} else {
		err = applyUserKeyTeamScopeMigrationSQLite(ctx, tx)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
