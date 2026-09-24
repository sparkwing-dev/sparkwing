package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestV71GitHubCronIdentityUpgradesSQLiteWithoutAdoptingManualRows(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	seedManualCronForV71(t, st)
	downgradeV71CronColumns(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertV71CronIdentity(t, up)
}

func TestV71GitHubCronIdentityUpgradesPostgresWithoutAdoptingManualRows(t *testing.T) {
	ctx := context.Background()
	scoped := pgTestSchemaDSN(t)
	st, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	seedManualCronForV71(t, st)
	downgradeV71CronColumns(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := store.OpenPostgres(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	assertV71CronIdentity(t, up)
	for _, column := range []string{"github_installation_id", "github_repository_id"} {
		var dataType string
		if err := up.DB().QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'cron_schedules' AND column_name = $1`, column).Scan(&dataType); err != nil {
			t.Fatal(err)
		}
		if dataType != "bigint" {
			t.Errorf("cron_schedules.%s is %s, want bigint", column, dataType)
		}
	}
}

func seedManualCronForV71(t *testing.T, st *store.Store) {
	t.Helper()
	if _, _, err := st.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "crn_manual_v70", RepoPath: "https://github.com/acme/widgets.git", Pipeline: "nightly", Name: "default",
		Cron: "0 3 * * *", TZ: "UTC", Overlap: store.CronOverlapSkip, CatchUp: time.Hour,
		Where: store.CronWhereController,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func downgradeV71CronColumns(t *testing.T, st *store.Store) {
	t.Helper()
	for _, statement := range []string{
		`DROP INDEX idx_cron_schedules_github_identity`,
		`ALTER TABLE cron_schedules DROP COLUMN github_installation_id`,
		`ALTER TABLE cron_schedules DROP COLUMN github_repository_id`,
		`DELETE FROM sparkwing_requirements WHERE name = 'github-app-cron-identity-v1'`,
		`DELETE FROM sparkwing_schema_version WHERE version = 71`,
	} {
		if _, err := st.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("downgrade v71 with %q: %v", statement, err)
		}
	}
}

func assertV71CronIdentity(t *testing.T, st *store.Store) {
	t.Helper()
	if version, err := st.CurrentSchemaVersion(t.Context()); err != nil || version != 71 {
		t.Fatalf("schema version = %d, %v; want 71", version, err)
	}
	var installation, repository int64
	if err := st.DB().QueryRowContext(t.Context(), `SELECT github_installation_id, github_repository_id
		FROM cron_schedules WHERE id = 'crn_manual_v70'`).Scan(&installation, &repository); err != nil {
		t.Fatal(err)
	}
	if installation != 0 || repository != 0 {
		t.Fatalf("manual row inferred App identity %d/%d", installation, repository)
	}
	var requirements int
	if err := st.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sparkwing_requirements
		WHERE name = 'github-app-cron-identity-v1'`).Scan(&requirements); err != nil || requirements != 1 {
		t.Fatalf("v71 requirement count = %d, %v; want one", requirements, err)
	}
}
