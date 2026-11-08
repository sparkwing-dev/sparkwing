package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

type schemaUpgrade struct {
	from               int
	release            string
	digest             string
	statements         []string
	postgresStatements []string
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
	{
		from: 34, release: "schema 34", digest: "187573d27274e9bf1796a84d502b39ac9e8c0cb7730e41143a1e4f586d5a6012",
		statements: schema35ActionsStatements,
	},
	{
		from: 35, release: "schema 35", digest: "4d4bc4dc64181ee22e7d1bc02925b4587dde2395469658c9b06b6c6ee59a81a2",
		statements: actionsTeamForeignKeyUpgrade(),
		postgresStatements: []string{
			`ALTER TABLE github_actions_approvals ADD CONSTRAINT github_actions_approvals_team_fkey FOREIGN KEY(team) REFERENCES teams(name)`,
			`ALTER TABLE github_actions_dispatches ADD CONSTRAINT github_actions_dispatches_team_fkey FOREIGN KEY(team) REFERENCES teams(name)`,
		},
	},
}

var schema35ActionsStatements = []string{`CREATE TABLE github_actions_approvals (
    team TEXT NOT NULL,
    repository_id BIGINT NOT NULL,
    workflow TEXT NOT NULL,
    runner TEXT NOT NULL,
    workflow_ref TEXT NOT NULL,
    approval_id TEXT NOT NULL UNIQUE,
    PRIMARY KEY(team,repository_id,workflow,runner),
    FOREIGN KEY(team,repository_id) REFERENCES github_runner_bindings(team,repository_id) ON DELETE CASCADE
)`, `CREATE TABLE github_actions_dispatches (
    receipt TEXT NOT NULL PRIMARY KEY,
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    queue_started_at BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    approval_id TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    repository_id BIGINT NOT NULL,
    repository_owner_id BIGINT NOT NULL,
    repository TEXT NOT NULL,
    workflow TEXT NOT NULL,
    workflow_ref TEXT NOT NULL,
    workflow_sha TEXT NOT NULL DEFAULT '',
    runner TEXT NOT NULL,
    policy_revision TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('requested','sending','accepted','refused','uncertain','cancelled')),
    reason TEXT NOT NULL DEFAULT '',
    send_until BIGINT NOT NULL DEFAULT 0,
    provider_run_id TEXT NOT NULL DEFAULT '',
    provider_attempt TEXT NOT NULL DEFAULT '',
    cancel_sent BIGINT NOT NULL DEFAULT 0,
    UNIQUE(team,run_id,node_id,queue_started_at,generation),
    FOREIGN KEY(team,run_id,node_id) REFERENCES nodes(team,run_id,node_id) ON DELETE CASCADE
)`, `CREATE INDEX idx_actions_dispatch_sender ON github_actions_dispatches(state,send_until) WHERE state IN ('requested','sending')`, `CREATE INDEX idx_actions_dispatch_live ON github_actions_dispatches(state) WHERE state<>'cancelled'`, `CREATE INDEX idx_actions_dispatch_cancellation ON github_actions_dispatches(state,cancel_sent) WHERE provider_run_id<>''`}

// safety: SQLite cannot add a foreign key, and these schema 35 tables have no inbound references.
func actionsTeamForeignKeyUpgrade() []string {
	return []string{
		`ALTER TABLE github_actions_approvals RENAME TO github_actions_approvals_schema35`,
		strings.Replace(schema35ActionsStatements[0], "team TEXT NOT NULL,", "team TEXT NOT NULL REFERENCES teams(name),", 1),
		`INSERT INTO github_actions_approvals(team,repository_id,workflow,runner,workflow_ref,approval_id) SELECT team,repository_id,workflow,runner,workflow_ref,approval_id FROM github_actions_approvals_schema35`,
		`DROP TABLE github_actions_approvals_schema35`,
		`ALTER TABLE github_actions_dispatches RENAME TO github_actions_dispatches_schema35`,
		strings.Replace(schema35ActionsStatements[1], "team TEXT NOT NULL,", "team TEXT NOT NULL REFERENCES teams(name),", 1),
		`INSERT INTO github_actions_dispatches(receipt,team,run_id,node_id,queue_started_at,generation,approval_id,installation_id,repository_id,repository_owner_id,repository,workflow,workflow_ref,workflow_sha,runner,policy_revision,state,reason,send_until,provider_run_id,provider_attempt,cancel_sent) SELECT receipt,team,run_id,node_id,queue_started_at,generation,approval_id,installation_id,repository_id,repository_owner_id,repository,workflow,workflow_ref,workflow_sha,runner,policy_revision,state,reason,send_until,provider_run_id,provider_attempt,cancel_sent FROM github_actions_dispatches_schema35`,
		`DROP TABLE github_actions_dispatches_schema35`,
		schema35ActionsStatements[2], schema35ActionsStatements[3], schema35ActionsStatements[4],
	}
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
		statements := step.statements
		if tx.dialect == DialectPostgres && step.postgresStatements != nil {
			statements = step.postgresStatements
		}
		for _, statement := range statements {
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
