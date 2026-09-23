package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The usage metrics count active teams, runs, sign-ups and time to first
// green per UTC week, and the excluded operator team counts toward nothing.
func TestOperatorUsageMetrics_WeeklyTractionFromOwnTables(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Date(2030, 1, 16, 12, 0, 0, 0, time.UTC)
	thisWeek := store.UsageWeekStart(now)
	lastWeek := thisWeek.AddDate(0, 0, -7)
	if thisWeek.Weekday() != time.Monday || !thisWeek.Equal(time.Date(2030, 1, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("UsageWeekStart(%s) = %s, want Monday 2030-01-14", now, thisWeek)
	}

	alpha := teamHandle(t, s, "alpha")
	bravo := teamHandle(t, s, "bravo")
	operator := teamHandle(t, s, "operator")
	for team, at := range map[store.Team]time.Time{
		"alpha":    lastWeek.Add(time.Hour),
		"bravo":    thisWeek.Add(time.Hour),
		"operator": lastWeek.Add(time.Hour),
	} {
		if err := s.TestOnlySetTeamCreatedAt(ctx, team, at); err != nil {
			t.Fatalf("set %s created_at: %v", team, err)
		}
	}
	for i, at := range []time.Time{lastWeek.Add(2 * time.Hour), thisWeek.Add(3 * time.Hour)} {
		if err := s.TestOnlyInsertAccount(ctx, "acct-"+string(rune('a'+i)), at); err != nil {
			t.Fatalf("insert account: %v", err)
		}
	}

	run := func(tn *store.Tenant, id, status string, started time.Time) {
		t.Helper()
		finished := started.Add(time.Minute)
		if err := tn.CreateRun(ctx, store.Run{ID: id, Pipeline: "build", Status: status, StartedAt: started, FinishedAt: &finished}); err != nil {
			t.Fatalf("CreateRun %s: %v", id, err)
		}
	}
	// alpha: failed last week, first green 2 days after creation, then two
	// more runs this week, one of them on a cloud runner.
	run(alpha, "a1", "failed", lastWeek.Add(90*time.Minute))
	run(alpha, "a2", "success", lastWeek.Add(49*time.Hour-time.Minute))
	run(alpha, "a3", "success", thisWeek.Add(4*time.Hour))
	run(alpha, "a4", "failed", thisWeek.Add(5*time.Hour))
	if err := s.TestOnlyRecordCloudAttempt(ctx, "alpha", "a4", "build"); err != nil {
		t.Fatalf("record cloud attempt: %v", err)
	}
	// bravo: green 30 minutes after creation.
	run(bravo, "b1", "success", thisWeek.Add(89*time.Minute))
	// operator: busy, and must not count.
	for i := range 5 {
		run(operator, "op"+string(rune('0'+i)), "success", thisWeek.Add(time.Duration(i+1)*time.Hour))
	}
	// Outside the window: before it.
	run(alpha, "old", "failed", lastWeek.AddDate(0, 0, -7))

	got, err := s.AsOperator().UsageMetrics(ctx, now, 2, []store.Team{"operator"})
	if err != nil {
		t.Fatalf("UsageMetrics: %v", err)
	}
	if len(got.Weeks) != 2 || !got.Weeks[0].WeekStart.Equal(lastWeek) || !got.Weeks[1].WeekStart.Equal(thisWeek) {
		t.Fatalf("weeks = %+v, want last week then this week", got.Weeks)
	}
	prev, cur := got.Weeks[0], got.Weeks[1]
	if prev.ActiveTeams != 1 || prev.Runs != 2 || prev.CloudRuns != 0 || prev.ConnectedRuns != 2 {
		t.Errorf("last week = %+v, want 1 active team, 2 connected runs", prev)
	}
	if prev.TeamsByRuns.TwoToNine != 1 || prev.MedianRunsPerActiveTeam != 2 {
		t.Errorf("last week distribution = %+v median %v, want one team in 2-9, median 2", prev.TeamsByRuns, prev.MedianRunsPerActiveTeam)
	}
	if prev.NewTeams != 1 || prev.NewAccounts != 1 {
		t.Errorf("last week sign-ups = %d teams %d accounts, want 1 and 1", prev.NewTeams, prev.NewAccounts)
	}
	if cur.ActiveTeams != 2 || cur.Runs != 3 || cur.CloudRuns != 1 || cur.ConnectedRuns != 2 {
		t.Errorf("this week = %+v, want 2 active teams, 3 runs, 1 cloud", cur)
	}
	if cur.TeamsByRuns.One != 1 || cur.TeamsByRuns.TwoToNine != 1 || cur.MedianRunsPerActiveTeam != 1.5 {
		t.Errorf("this week distribution = %+v median %v, want one team each in 1 and 2-9, median 1.5", cur.TeamsByRuns, cur.MedianRunsPerActiveTeam)
	}
	if cur.NewTeams != 1 || cur.NewAccounts != 1 {
		t.Errorf("this week sign-ups = %d teams %d accounts, want 1 and 1", cur.NewTeams, cur.NewAccounts)
	}
	fg := got.FirstGreen
	if fg.TeamsCreated != 2 || fg.TeamsGreen != 2 || fg.WithinHour != 1 || fg.WithinWeek != 1 {
		t.Errorf("first green = %+v, want 2 created, 2 green, one within the hour, one within the week", fg)
	}
	if fg.MedianSeconds != int64((30 * time.Minute).Seconds()) {
		t.Errorf("first green median = %ds, want %ds", fg.MedianSeconds, int64((30 * time.Minute).Seconds()))
	}

	// Negative control: without the exclusion the operator team's runs are
	// there to be counted, so their absence above is the exclusion.
	all, err := s.AsOperator().UsageMetrics(ctx, now, 2, nil)
	if err != nil {
		t.Fatalf("UsageMetrics unexcluded: %v", err)
	}
	if all.Weeks[1].ActiveTeams != 3 || all.Weeks[1].Runs != 8 {
		t.Errorf("unexcluded this week = %+v, want 3 active teams and 8 runs", all.Weeks[1])
	}
}
