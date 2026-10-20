package crons

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTickRefreshesAndEvaluatesThroughEachTenant(t *testing.T) {
	s, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "schedules.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	teststore.SeedTeam(t, s, "other")
	now := time.Unix(1700000000, 37).UTC()
	next := now.Add(time.Hour)
	for _, team := range []store.Team{store.DefaultTeam, "other"} {
		tenant := teststore.ForTeam(t, s, team)
		_, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{
			ID: "schedule-" + string(team), RepoPath: "https://git.example.com/project",
			Pipeline: "nightly", Cron: "0 * * * *", TZ: "UTC", Where: store.CronWhereController, NextDueAt: &next,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, scoped := range []bool{false, true} {
		svc := &Service{Store: s, Side: store.CronWhereController, Now: func() time.Time { return now }}
		want := 2
		if scoped {
			svc.Schedules = teststore.ForTeam(t, s, "other")
			want = 1
		}
		report, err := svc.Tick(t.Context(), false)
		if err != nil || len(report.Errors) != 0 || report.Evaluated != want {
			t.Fatalf("scoped=%t tick: %+v, %v", scoped, report, err)
		}
		for _, tenant := range []store.Team{store.DefaultTeam, "other"} {
			row, err := teststore.ForTeam(t, s, tenant).GetCronSchedule(t.Context(), "schedule-"+string(tenant))
			if err != nil || !row.Declared || !row.CursorAt.Equal(now) {
				t.Fatalf("schedule after tick: %+v, %v", row, err)
			}
		}
	}
}
