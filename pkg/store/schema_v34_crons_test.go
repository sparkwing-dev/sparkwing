package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: the shape v33 had before it gained git_branch, which is what every
// store an earlier commit on this branch stamped 33 actually carries: the
// widened key is absent as well, because that commit only created the index
// when it rebuilt the table.
const cronSchedulesV33NoBranchTable = `CREATE TABLE cron_schedules (
    id            TEXT PRIMARY KEY,
    repo_path     TEXT NOT NULL,
    pipeline      TEXT NOT NULL,
    schedule_name TEXT NOT NULL DEFAULT 'default',
    cron          TEXT NOT NULL,
    tz            TEXT NOT NULL,
    overlap       TEXT NOT NULL,
    catch_up_ns   INTEGER NOT NULL,
    where_        TEXT NOT NULL DEFAULT 'local',
    args          TEXT NOT NULL DEFAULT '{}',
    locked_ref    TEXT NOT NULL DEFAULT '',
    locked_binary TEXT NOT NULL DEFAULT '',
    locked_digest TEXT NOT NULL DEFAULT '',
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
    override_cron        TEXT,
    override_tz          TEXT,
    override_overlap     TEXT,
    override_catch_up_ns INTEGER,
    override_args        TEXT,
    override_base        TEXT,
    override_set_at      INTEGER
)`

func TestSchemaV34RepairsAStoreStampedV33WithoutGitBranch(t *testing.T) {
	target := storetest.New(t)
	ctx := context.Background()
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	armedNS := cronBase.UnixNano()
	statements := []string{
		`DROP TABLE cron_schedules`,
		dialectDDL(st, cronSchedulesV33NoBranchTable),
		`DELETE FROM sparkwing_schema_version WHERE version >= 34`,
	}
	for _, statement := range statements {
		if _, err := st.DB().ExecContext(ctx, statement); err != nil {
			_ = st.Close()
			t.Fatalf("downgrade with %q: %v", statement, err)
		}
	}
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
		`INSERT INTO cron_schedules
         (id, repo_path, pipeline, schedule_name, cron, tz, overlap, catch_up_ns,
          armed_at, armed_by, updated_at, cursor_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		"crn_pushed", "https://example.com/acme/app.git", "nightly", "default",
		"0 3 * * *", "UTC", store.CronOverlapSkip, int64(time.Hour),
		armedNS, "korey", armedNS, armedNS,
	); err != nil {
		_ = st.Close()
		t.Fatalf("seed a v33 schedule: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatalf("open a store stamped 33 without git_branch: %v", err)
	}
	defer func() { _ = up.Close() }()
	if got, err := up.CurrentSchemaVersion(ctx); err != nil || got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, err = %v, want %d", got, err, store.ExpectedSchemaVersion())
	}
	scheds, err := up.ListCronSchedules(ctx)
	if err != nil {
		t.Fatalf("list schedules after the repair: %v", err)
	}
	if len(scheds) != 1 || scheds[0].ID != "crn_pushed" {
		t.Fatalf("schedules after the repair = %+v, want the seeded crn_pushed", scheds)
	}
	assertNamedCronSchemaShape(t, up)

	// safety: the widened key has to hold afterwards, not just exist by name.
	if _, _, err := up.ArmCronSchedule(ctx, store.CronSchedule{
		ID: "crn_second", RepoPath: "https://example.com/acme/app.git", Pipeline: "nightly",
		Name: "evening", Cron: "0 20 * * *", TZ: "UTC", CatchUp: time.Hour,
	}, cronBase); err != nil {
		t.Fatalf("arm a second name on the repaired pipeline: %v", err)
	}
}

func TestSchemaV34StampsTheScheduleNameRequirement(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	listed, err := st.Requirements(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range listed {
		if name == "cron-schedule-names-v1" {
			found = true
		}
	}
	if !found {
		t.Errorf("requirements = %v, want cron-schedule-names-v1 among them", listed)
	}
}
