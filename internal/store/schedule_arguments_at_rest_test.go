package store_test

import (
	"maps"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestScheduleArgumentValuesNeverReachStoredRows(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			s := dialect.target(t).Open(t, store.WithPolicies(sealStorePolicies))
			tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]string{"token": argSupplied}
			row, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{ID: "argument-schedule", RepoPath: filepath.Join(t.TempDir(), "repo"), Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC", Args: args})
			if err != nil {
				t.Fatal(err)
			}
			if err := tenant.SetCronOverride(t.Context(), row.ID, store.CronOverride{Args: map[string]string{"token": argDefault}, Base: row.Declaration()}); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1700000000, 0)
			if err := tenant.ResolveCronDue(t.Context(), row.ID, now, nil, &store.CronFire{ID: "argument-fire", DueAt: now, DecidedAt: now, Outcome: store.CronOutcomeMissed, Args: args}); err != nil {
				t.Fatal(err)
			}
			got, err := tenant.GetCronSchedule(t.Context(), row.ID)
			if err != nil || !maps.Equal(got.Args, args) || got.Override == nil || got.Override.Args["token"] != argDefault || got.Override.Base.Args["token"] != argSupplied {
				t.Fatalf("schedule did not round trip: %v", err)
			}
			fires, err := tenant.ListCronFires(t.Context(), row.ID, 1)
			if err != nil || len(fires) != 1 || !maps.Equal(fires[0].Args, args) {
				t.Fatalf("fire did not round trip: %v", err)
			}
			if leaks := storedCanaries(t, s, argumentCanaries); len(leaks) > 0 {
				t.Fatalf("schedule argument values stored in plaintext: %v", leaks)
			}
		})
	}
}
