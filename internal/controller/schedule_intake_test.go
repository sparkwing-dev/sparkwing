package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestScheduleIntakeRequiresStoredConsent(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := teststore.ForTeam(t, st, store.DefaultTeam)
	now := time.Now().UTC()
	row, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{ID: "raw-nightly", RepoPath: "https://gitlab.com/acme/repo.git", Pipeline: "nightly", Cron: "* * * * *", Where: store.CronWhereController, GitBranch: "main", LockedRef: "0123456789abcdef0123456789abcdef01234567", ArmedBy: "owner@example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	launcher := cronLauncher{server: New(st, nil)}
	if run, err := launcher.Launch(t.Context(), row, now); err == nil || run != "" {
		t.Fatalf("raw schedule manufactured consent: %s %v", run, err)
	}
	runs, err := tenant.ListRuns(t.Context(), store.RunFilter{})
	if err != nil || len(runs) != 0 {
		t.Fatalf("unconsented runs: %+v %v", runs, err)
	}
}
