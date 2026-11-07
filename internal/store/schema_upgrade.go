package store

import (
	"context"
	"fmt"
	"slices"
	"time"
)

type schemaUpgrade struct {
	from       int
	release    string
	digest     string
	statements []string
}

// safety: only a store at a listed version whose digest matches that release is changed; every step runs inside
// the open transaction, which holds the migrate advisory lock on PostgreSQL, so concurrent openers upgrade once.
var schemaUpgrades = []schemaUpgrade{
	{
		from: 30, release: "v0.69.2", digest: "d0f42d8e52dd4ce999ace44a3767b6302d87e54c3b79dd544dfc5cd1ff8c0cf4",
		statements: []string{`CREATE TABLE local_machine_admission (
    singleton BIGINT NOT NULL PRIMARY KEY CHECK(singleton=1),
    state_json BLOB NOT NULL
)`},
	},
	{
		from: 31, release: "schema 31", digest: "c09c6196b47e8c6561edd4f6274c105e5c8d00a8cbe9ec8925c5e0f23a68d236",
		statements: []string{`CREATE TABLE local_run_secrets (
    team TEXT NOT NULL REFERENCES teams(name),
    run_id TEXT NOT NULL,
    envelope TEXT NOT NULL,
    PRIMARY KEY(team,run_id),
    FOREIGN KEY(team,run_id) REFERENCES runs(team,id) ON DELETE CASCADE
)`},
	},
	{
		from: 32, release: "schema 32", digest: "8229dac48bd08544fb5b2cddfe2f9c9d0f0f3c41968eda4db1591aff9f247f61",
		statements: []string{`CREATE TABLE local_runner_instances (
    id TEXT NOT NULL PRIMARY KEY CHECK(id <> ''),
    capability_prefix TEXT NOT NULL UNIQUE REFERENCES capabilities(prefix) ON DELETE CASCADE,
    machine TEXT NOT NULL CHECK(machine <> ''),
    pid BIGINT NOT NULL CHECK(pid > 0),
    process_birth TEXT NOT NULL CHECK(process_birth <> ''),
    version TEXT NOT NULL CHECK(version <> ''),
    build_identity TEXT NOT NULL CHECK(build_identity <> ''),
    state TEXT NOT NULL CHECK(state IN ('registered','active','draining','stopped'))
)`, `CREATE UNIQUE INDEX idx_local_runner_active ON local_runner_instances(machine) WHERE state='active'`},
	},
	{
		from: 33, release: "schema 33", digest: "b4ac52448bafd4ea378d1ed2e1d9aec0c610d3a5e27df4a7a780c178f425a47b",
		statements: []string{`CREATE INDEX idx_runs_live_children ON runs(team,id) WHERE parent_run_id <> '' AND status IN ('pending','running')`},
	},
}

func schemaUpgradeFrom(version int) (schemaUpgrade, bool) {
	i := slices.IndexFunc(schemaUpgrades, func(u schemaUpgrade) bool { return u.from == version })
	if i < 0 {
		return schemaUpgrade{}, false
	}
	return schemaUpgrades[i], true
}

func upgradeSchemaTx(ctx context.Context, tx *storeTx, version int, path string) error {
	start := version
	for version < expectedSchemaVersion {
		step, ok := schemaUpgradeFrom(version)
		if !ok {
			return schemaMismatchError(version, path)
		}
		recorded, err := recordedBaselineDigestTx(ctx, tx, version)
		if err != nil {
			return err
		}
		if recorded != step.digest {
			return schemaUpgradeDigestError(step, recorded, path)
		}
		if version == start && tx.dialect == DialectSQLite && path != "" {
			if err := backupSQLiteBeforeUpgrade(ctx, path, version); err != nil {
				return err
			}
		}
		for _, statement := range step.statements {
			translated, err := translateSchema(statement, tx.dialect)
			if err == nil {
				_, err = tx.execContext(ctx, translated)
			}
			if err != nil {
				return fmt.Errorf("upgrade store schema %d to %d: %w; the store is unchanged", version, version+1, err)
			}
		}
		next := schemaBaselineDigest()
		if version+1 < expectedSchemaVersion {
			following, ok := schemaUpgradeFrom(version + 1)
			if !ok {
				return schemaMismatchError(version+1, path)
			}
			next = following.digest
		}
		if _, err := tx.execContext(ctx, `UPDATE sparkwing_schema_version SET version = ?, applied_at = ?, baseline_digest = ? WHERE version = ?`,
			version+1, time.Now().UnixNano(), next, version); err != nil {
			return fmt.Errorf("record store schema %d: %w", version+1, err)
		}
		version++
	}
	return nil
}

// safety: a store without the digest column predates every release this binary upgrades, so it reads as missing.
func recordedBaselineDigestTx(ctx context.Context, tx *storeTx, version int) (string, error) {
	hasDigest, err := hasBaselineDigestColumnTx(ctx, tx)
	if err != nil || !hasDigest {
		return "", err
	}
	var recorded string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(baseline_digest, '') FROM sparkwing_schema_version WHERE version = ?`, version).Scan(&recorded); err != nil {
		return "", fmt.Errorf("read schema baseline digest: %w", err)
	}
	return recorded, nil
}
