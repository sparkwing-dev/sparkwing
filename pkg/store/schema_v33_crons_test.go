package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: the v32 shape verbatim, so the upgrade is exercised against the
// tables a released binary actually left behind.
const cronSchedulesV32Table = `CREATE TABLE cron_schedules (
    id            TEXT PRIMARY KEY,
    repo_path     TEXT NOT NULL,
    pipeline      TEXT NOT NULL,
    cron          TEXT NOT NULL,
    tz            TEXT NOT NULL,
    overlap       TEXT NOT NULL,
    catch_up_ns   INTEGER NOT NULL,
    paused        INTEGER NOT NULL DEFAULT 0,
    declared      INTEGER NOT NULL DEFAULT 1,
    armed_at      INTEGER NOT NULL,
    armed_by      TEXT NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL,
    cursor_at     INTEGER NOT NULL,
    last_fired_at INTEGER,
    last_run_id   TEXT NOT NULL DEFAULT '',
    last_outcome  TEXT NOT NULL DEFAULT '',
    next_due_at   INTEGER,
    UNIQUE(repo_path, pipeline)
)`

const cronFiresV32Table = `CREATE TABLE cron_fires (
    id          TEXT PRIMARY KEY,
    schedule_id TEXT NOT NULL,
    due_at      INTEGER NOT NULL,
    decided_at  INTEGER NOT NULL,
    outcome     TEXT NOT NULL,
    run_id      TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT ''
)`

func TestSchemaV33FreshDatabaseHasNamedCronColumns(t *testing.T) {
	st := storetest.Open(t)
	assertNamedCronSchemaShape(t, st)
}

func TestSchemaV33UpgradesV32AndKeepsSchedulesAndFires(t *testing.T) {
	target := storetest.New(t)
	ctx := context.Background()
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	armedNS := cronBase.UnixNano()
	statements := []string{
		`DROP TABLE cron_fires`,
		`DROP TABLE cron_schedules`,
		dialectDDL(st, cronSchedulesV32Table),
		dialectDDL(st, cronFiresV32Table),
		`CREATE INDEX idx_cron_fires_schedule_decided ON cron_fires(schedule_id, decided_at DESC)`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 33`,
	}
	for _, statement := range statements {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			_ = st.Close()
			t.Fatalf("downgrade with %q: %v", statement, err)
		}
	}
	for _, sched := range []struct{ id, repo, pipeline string }{
		{"crn_nightly", "/repo/one", "nightly"},
		{"crn_build", "/repo/two", "build"},
	} {
		if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
			`INSERT INTO cron_schedules
             (id, repo_path, pipeline, cron, tz, overlap, catch_up_ns, armed_at, armed_by,
              updated_at, cursor_at, last_run_id, last_outcome)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			sched.id, sched.repo, sched.pipeline, "0 * * * *", "UTC", store.CronOverlapSkip,
			int64(time.Hour), armedNS, "korey", armedNS, armedNS, "run-1", store.CronOutcomeFired,
		); err != nil {
			t.Fatalf("insert v32 schedule %s: %v", sched.id, err)
		}
		if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
			`INSERT INTO cron_fires (id, schedule_id, due_at, decided_at, outcome, run_id, detail)
             VALUES (?, ?, ?, ?, ?, ?, ?)`),
			"crf_"+sched.pipeline, sched.id, armedNS, armedNS, store.CronOutcomeFired, "run-1", "",
		); err != nil {
			t.Fatalf("insert v32 fire for %s: %v", sched.id, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("upgrade v32 to v33: %v", err)
	}
	defer func() { _ = up.Close() }()
	if got, err := up.CurrentSchemaVersion(ctx); err != nil || got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, err = %v, want %d", got, err, store.ExpectedSchemaVersion())
	}
	assertNamedCronSchemaShape(t, up)

	scheds, err := up.ListCronSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheds) != 2 {
		t.Fatalf("schedules after upgrade = %d, want the 2 armed before it", len(scheds))
	}
	for _, sched := range scheds {
		if sched.Name != store.CronScheduleDefaultName {
			t.Errorf("%s name = %q, want %q", sched.ID, sched.Name, store.CronScheduleDefaultName)
		}
		if sched.Where != store.CronWhereLocal {
			t.Errorf("%s where = %q, want %q", sched.ID, sched.Where, store.CronWhereLocal)
		}
		if len(sched.Args) != 0 {
			t.Errorf("%s args = %v, want none", sched.ID, sched.Args)
		}
		if sched.Override != nil {
			t.Errorf("%s carries an override %+v after upgrade", sched.ID, sched.Override)
		}
		if sched.LockedRef != "" || sched.LockedBinary != "" || sched.LockedDigest != "" {
			t.Errorf("%s is locked after upgrade: %+v", sched.ID, sched.Declaration())
		}
		if sched.ArmedBy != "korey" || !sched.ArmedAt.Equal(cronBase) || sched.LastRunID != "run-1" {
			t.Errorf("%s lost pre-migration state: %+v", sched.ID, sched)
		}
		fires, err := up.ListCronFires(ctx, sched.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(fires) != 1 || fires[0].RunID != "run-1" || len(fires[0].Args) != 0 {
			t.Errorf("%s fires after upgrade = %+v", sched.ID, fires)
		}
	}
	assertTwoNamesOnOnePipeline(t, up, "/repo/one", "nightly")
}

// safety: the widening exists so one pipeline carries several names, so an
// upgraded store has to accept a second where v32's key refused it.
func assertTwoNamesOnOnePipeline(t *testing.T, st *store.Store, repo, pipeline string) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{"morning", "evening"} {
		if _, _, err := st.ArmCronSchedule(ctx, store.CronSchedule{
			ID: "crn_upgraded_" + name, RepoPath: repo, Pipeline: pipeline, Name: name,
			Cron: "0 * * * *", TZ: "UTC", CatchUp: time.Hour,
		}, cronBase); err != nil {
			t.Fatalf("arm %s/%s after the upgrade: %v", pipeline, name, err)
		}
	}
}

func TestSchemaV33KeysSchedulesByName(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	for _, name := range []string{"morning", "evening"} {
		if _, _, err := st.ArmCronSchedule(ctx, store.CronSchedule{
			ID: "crn_" + name, RepoPath: "/repo/one", Pipeline: "nightly", Name: name,
			Cron: "0 * * * *", TZ: "UTC", CatchUp: time.Hour,
		}, cronBase); err != nil {
			t.Fatalf("arm %s: %v", name, err)
		}
	}
	scheds, err := st.ListCronSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheds) != 2 {
		t.Fatalf("schedules = %d, want two names on one pipeline", len(scheds))
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`INSERT INTO cron_schedules
         (id, repo_path, pipeline, schedule_name, cron, tz, overlap, catch_up_ns,
          armed_at, updated_at, cursor_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		"crn_clash", "/repo/one", "nightly", "morning", "0 * * * *", "UTC",
		store.CronOverlapSkip, int64(time.Hour),
		cronBase.UnixNano(), cronBase.UnixNano(), cronBase.UnixNano(),
	); err == nil {
		t.Fatal("a second schedule named morning on the same pipeline was accepted")
	}
}

func assertNamedCronSchemaShape(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for table, columns := range map[string]string{
		"cron_schedules": `schedule_name, where_, git_branch, args, locked_ref, locked_binary, locked_digest,
                           override_cron, override_tz, override_overlap, override_catch_up_ns,
                           override_args, override_base, override_set_at`,
		"cron_fires": `args`,
	} {
		if _, err := st.DB().ExecContext(ctx, `SELECT `+columns+` FROM `+table); err != nil {
			t.Fatalf("read %s columns: %v", table, err)
		}
	}
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	if st.Dialect() == store.DialectPostgres {
		query = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
	}
	var indexes int
	if err := st.DB().QueryRowContext(ctx, storetest.Rebind(st, query),
		"idx_cron_schedules_repo_pipeline_name").Scan(&indexes); err != nil {
		t.Fatalf("inspect schedule name index: %v", err)
	}
	if indexes != 1 {
		t.Errorf("idx_cron_schedules_repo_pipeline_name count = %d, want 1", indexes)
	}
}

// safety: nanosecond stamps live in INTEGER on SQLite, which Postgres would
// take as a 32-bit column that truncates them.
func dialectDDL(st *store.Store, ddl string) string {
	if st.Dialect() != store.DialectPostgres {
		return ddl
	}
	return strings.ReplaceAll(ddl, "INTEGER", "BIGINT")
}
