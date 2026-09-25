package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: the two migrations landed on branches off the same base, so nothing
// had ever run them back to back. v48 renames `runs.repo` and `secrets.repo`
// and v49 adds the tenant key to both tables, and a deployment upgrading
// across the pair runs them in one open.
func downgradeToV47(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	db := st.DB()
	// safety: the team leads seven primary keys since v51, and a key
	// column cannot be dropped, so the keys go back first.
	for table, key := range store.UserKeyTablesForTest() {
		if err := store.RekeyForTest(ctx, st, table, key); err != nil {
			t.Fatalf("narrow %s back to the v49 key: %v", table, err)
		}
	}
	stmts := []string{
		`DROP INDEX IF EXISTS idx_runs_team_started`,
		// safety: v52 leads these keys with the team, so they go back to the
		// v47 keys around the column drop.
		`DROP INDEX IF EXISTS idx_cron_schedules_repo_pipeline_name`,
		`DROP INDEX IF EXISTS idx_cron_schedules_github_identity`,
		`ALTER TABLE cron_schedules DROP COLUMN github_installation_id`,
		`ALTER TABLE cron_schedules DROP COLUMN github_repository_id`,
		`DROP INDEX IF EXISTS ` + store.TriggerIdempotencyIndexName,
		`DROP INDEX IF EXISTS ` + store.TriggerWebhookDeliveryIndexName,
		`DROP INDEX IF EXISTS ` + store.TriggerWebhookReplayKeyIndexName,
		`DROP INDEX IF EXISTS idx_credit_grants_team_reference`,
		`DROP INDEX IF EXISTS idx_triggers_team_created`,
	}
	for _, table := range store.TenantTablesForTest() {
		stmts = append(stmts, `ALTER TABLE `+table+` DROP COLUMN team`)
	}
	stmts = append(stmts, `CREATE UNIQUE INDEX idx_cron_schedules_repo_pipeline_name
    ON cron_schedules(repo_path, pipeline, schedule_name)`,
		`CREATE UNIQUE INDEX `+store.TriggerIdempotencyIndexName+`
    ON triggers(pipeline, idempotency_key) WHERE idempotency_key != ''`,
		`CREATE UNIQUE INDEX `+store.TriggerWebhookDeliveryIndexName+`
    ON triggers(webhook_delivery) WHERE webhook_delivery != ''`,
		`CREATE UNIQUE INDEX `+store.TriggerWebhookReplayKeyIndexName+`
    ON triggers(webhook_replay_key) WHERE webhook_replay_key != ''`)
	stmts = append(stmts, `DROP TABLE teams`)
	for _, rename := range []struct{ table, from, to string }{
		{"runs", "declared_repo", "repo"},
		{"secrets", "pipeline", "repo"},
	} {
		// safety: a fresh Postgres store carries both names, because the v22
		// migration adds `secrets.repo` to a table already created with
		// `pipeline`, so a rename onto the taken name fails. Dropping the new
		// name leaves the v47 shape, because the seed comes after this.
		stmt := `ALTER TABLE ` + rename.table + ` RENAME COLUMN ` + rename.from + ` TO ` + rename.to
		if columnReadable(t, db, rename.table, rename.to) {
			stmt = `ALTER TABLE ` + rename.table + ` DROP COLUMN ` + rename.from
		}
		stmts = append(stmts, stmt)
	}
	stmts = append(stmts,
		`DELETE FROM sparkwing_schema_version WHERE version >= 48`,
		`DELETE FROM sparkwing_requirements WHERE name IN ('pipeline-scoped-secrets','declared-run-repo','team-scoped-user-keys')`,
	)
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, rename := range []struct{ table, gone string }{
		{"runs", "declared_repo"},
		{"secrets", "pipeline"},
	} {
		if columnReadable(t, db, rename.table, rename.gone) {
			t.Fatalf("%s still has %s after the downgrade; the fixture is not at v47", rename.table, rename.gone)
		}
	}
}

func columnReadable(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT `+column+` FROM `+table+` WHERE 1 = 0`)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	return rows.Err() == nil
}

// A v47 store reaches the newest version in one open, running v48, v49 and
// every version since. The ladder refuses a gap, so they either compose or no
// deployment on v47 can upgrade at all.
func TestSchemaV47ReachesTheNewestVersionInOneOpen(t *testing.T) {
	ctx := context.Background()
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV47(t, st)
	if got := readSchemaVersion(t, st.DB()); got != 47 {
		t.Fatalf("schema version after the downgrade = %d, want 47", got)
	}

	// safety: the seed names the v47 columns, because what the composition
	// has to preserve is the bytes a v47 binary wrote.
	for _, q := range []string{
		`INSERT INTO runs (id, pipeline, status, started_at, repo) VALUES ('run-47', 'build', 'success', 1, 'acme/widget')`,
		`INSERT INTO secrets (name, value, principal, created_at, updated_at, masked, repo) VALUES ('DEPLOY_KEY', 'v47-value', 'alice', 1, 1, 1, 'acme/widget')`,
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("seed the v47 shape (%s): %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("upgrade v47 to the newest version: %v", err)
	}
	defer func() { _ = up.Close() }()

	// safety: the assertion is that the ladder composed from v47 to the
	// head in one open, not that the head is still 49, because a later
	// migration must not silently stop being covered by this test.
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
	if store.ExpectedSchemaVersion() < 49 {
		t.Fatalf("ExpectedSchemaVersion = %d, want at least the tenant key's 49",
			store.ExpectedSchemaVersion())
	}

	if !columnReadable(t, up.DB(), "runs", "declared_repo") {
		t.Error("runs has no declared_repo after the upgrade; v48 did not run")
	}
	if columnReadable(t, up.DB(), "runs", "repo") {
		t.Error("runs still has repo after the upgrade; v48 renamed nothing")
	}
	if !columnReadable(t, up.DB(), "secrets", "pipeline") {
		t.Error("secrets has no pipeline after the upgrade; v48 did not run")
	}
	if columnReadable(t, up.DB(), "secrets", "repo") {
		t.Error("secrets still has repo after the upgrade; v48 renamed nothing")
	}

	for _, table := range store.TenantTablesForTest() {
		if !columnReadable(t, up.DB(), table, "team") {
			t.Errorf("%s has no team column after the upgrade; v49 did not run", table)
		}
	}
	teams, err := up.AsOperator().ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 || teams[0] != store.DefaultTeam {
		t.Errorf("ListTeams after the upgrade = %v, want only %q", teams, store.DefaultTeam)
	}

	var declared, runTeam string
	if err := up.DB().QueryRowContext(ctx, storetest.Rebind(up,
		`SELECT declared_repo, team FROM runs WHERE id = ?`), "run-47",
	).Scan(&declared, &runTeam); err != nil {
		t.Fatalf("read the migrated run: %v", err)
	}
	if declared != "acme/widget" {
		t.Errorf("declared_repo = %q, want acme/widget", declared)
	}
	if store.Team(runTeam) != store.DefaultTeam {
		t.Errorf("run team = %q, want %q", runTeam, store.DefaultTeam)
	}

	var pipeline, secretValue, secretTeam string
	if err := up.DB().QueryRowContext(ctx, storetest.Rebind(up,
		`SELECT pipeline, value, team FROM secrets WHERE name = ?`), "DEPLOY_KEY",
	).Scan(&pipeline, &secretValue, &secretTeam); err != nil {
		t.Fatalf("read the migrated secret: %v", err)
	}
	if pipeline != "acme/widget" || secretValue != "v47-value" {
		t.Errorf("migrated secret = (pipeline %q, value %q), want (acme/widget, v47-value)", pipeline, secretValue)
	}
	if store.Team(secretTeam) != store.DefaultTeam {
		t.Errorf("secret team = %q, want %q", secretTeam, store.DefaultTeam)
	}

	// safety: a team that cannot see what it owns reads a migrated install as
	// an empty one, so the scoped handle has to reach the backfilled row.
	def, err := up.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	run, err := def.GetRun(ctx, "run-47")
	if err != nil {
		t.Fatalf("default team GetRun after the upgrade: %v", err)
	}
	if run.DeclaredRepo != "acme/widget" {
		t.Errorf("scoped GetRun declared repo = %q, want acme/widget", run.DeclaredRepo)
	}
}
