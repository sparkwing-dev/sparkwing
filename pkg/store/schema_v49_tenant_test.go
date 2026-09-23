package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: dropping the column and its index is what makes reopening the
// database run the migration against the shape the previous binary left
// behind.
func downgradeTenantKeyToV48(t *testing.T, st *store.Store) {
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
		`DROP INDEX idx_runs_team_started`,
		// safety: v52 leads these keys with the team, so they go back to the
		// v47 keys around the column drop.
		`DROP INDEX IF EXISTS idx_cron_schedules_repo_pipeline_name`,
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
	stmts = append(stmts, `DROP TABLE teams`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 49`,
		`DELETE FROM sparkwing_requirements WHERE name = 'team-scoped-user-keys'`)
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSchemaV49FreshSQLiteTenantShape(t *testing.T) {
	st, err := storetest.NewSQLite(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if got := readSchemaVersion(t, st.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
	teams, err := st.AsOperator().ListTeams(context.Background())
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 || teams[0] != store.DefaultTeam {
		t.Errorf("ListTeams on a fresh store = %v, want only %q", teams, store.DefaultTeam)
	}
}

// The single-tenant install this release upgrades has rows written by a
// binary that never heard of a team. They have to end up in the default
// team, readable through its handle and through the un-ported surface,
// with no configuration asked of whoever is running it.
func TestSchemaV49BackfillsAnExistingSingleTenantInstall(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v47.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeTenantKeyToV48(t, st)
	// safety: the older binary names no team anywhere, so the seed does not either.
	for _, q := range []string{
		`INSERT INTO runs (id, pipeline, status, started_at) VALUES ('run-old', 'build', 'success', 1)`,
		`INSERT INTO nodes (run_id, node_id, status) VALUES ('run-old', 'compile', 'done')`,
		`INSERT INTO events (run_id, seq, node_id, kind, ts) VALUES ('run-old', 1, 'compile', 'log', 1)`,
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatalf("seed the v47 shape (%s): %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v48 to v49: %v", err)
	}
	defer func() { _ = up.Close() }()
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}

	for _, table := range []string{"runs", "nodes", "events"} {
		var n int
		if err := up.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE team = ?`, string(store.DefaultTeam),
		).Scan(&n); err != nil {
			t.Fatalf("count %s rows in the default team: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows in team %q, want 1", table, n, store.DefaultTeam)
		}
	}

	teams, err := up.AsOperator().ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 || teams[0] != store.DefaultTeam {
		t.Errorf("ListTeams after upgrade = %v, want only %q", teams, store.DefaultTeam)
	}

	viaStore, err := up.GetRun(ctx, "run-old")
	if err != nil {
		t.Fatalf("un-ported GetRun after upgrade: %v", err)
	}
	def, err := up.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	viaTenant, err := def.GetRun(ctx, "run-old")
	if err != nil {
		t.Fatalf("default team GetRun after upgrade: %v", err)
	}
	if viaStore.ID != viaTenant.ID || viaStore.Pipeline != viaTenant.Pipeline ||
		viaStore.Status != viaTenant.Status {
		t.Errorf("the same run reads differently: store=%+v tenant=%+v", viaStore, viaTenant)
	}

	if _, err := up.DB().ExecContext(ctx,
		`INSERT INTO runs (id, pipeline, status, started_at) VALUES ('run-after', 'build', 'running', 2)`,
	); err != nil {
		t.Fatalf("an insert naming no team failed after the upgrade: %v", err)
	}
	after, err := def.GetRun(ctx, "run-after")
	if err != nil {
		t.Fatalf("default team GetRun of a post-upgrade unteamed insert: %v", err)
	}
	if after.ID != "run-after" {
		t.Errorf("GetRun = %s, want run-after", after.ID)
	}

	adds, err := up.RequirementsWritingWouldAdd(ctx)
	if err != nil {
		t.Fatalf("RequirementsWritingWouldAdd: %v", err)
	}
	if len(adds) != 0 {
		t.Errorf("v49 declares requirements %v; it is additive and should declare none", adds)
	}
}

// A store whose rows all predate teams keeps answering the way it did,
// which is what a local `sparkwing serve start` depends on.
func TestSchemaV49LeavesTheSingleTenantSurfaceUnchanged(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-local", Pipeline: "build", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.FinishRun(ctx, "run-local", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	got, err := st.GetRun(ctx, "run-local")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("status = %q, want success", got.Status)
	}
	runs, err := st.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-local" {
		t.Errorf("ListRuns = %v, want only run-local", runs)
	}
	n, err := st.CountRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("CountRuns: %v", err)
	}
	if n != 1 {
		t.Errorf("CountRuns = %d, want 1", n)
	}
}
