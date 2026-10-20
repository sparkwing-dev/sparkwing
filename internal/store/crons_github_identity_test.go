package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestGitHubCronWithdrawalUsesExactStoredIdentity(t *testing.T) {
	st := storetest.New(t).Open(t)
	tn, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id           string
		installation int64
		repository   int64
	}{
		{"crn_app_a", 7, 701}, {"crn_app_b", 7, 702}, {"crn_app_c", 8, 701},
	} {
		if _, _, err := tn.ArmCronSchedule(t.Context(), store.CronSchedule{
			ID: tc.id, RepoPath: "https://github.com/acme/" + tc.id + ".git",
			GitHubInstallationID: tc.installation, GitHubRepositoryID: tc.repository,
			Pipeline: "nightly", Name: "default", Cron: "0 3 * * *", TZ: "UTC",
			Overlap: store.CronOverlapSkip, CatchUp: time.Hour, Where: store.CronWhereController,
		}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := tn.WithdrawGitHubCronRepository(t.Context(), 7, 701, time.Now()); err != nil || n != 1 {
		t.Fatalf("exact withdrawal = %d, %v; want one", n, err)
	}
	if n, err := tn.WithdrawOtherGitHubCronInstallations(t.Context(), 8, 701, time.Now()); err != nil || n != 0 {
		t.Fatalf("other-binding withdrawal after exact = %d, %v; want zero", n, err)
	}
	rows, err := tn.ListCronSchedules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		want := row.ID != "crn_app_a"
		if row.Declared != want {
			t.Errorf("row %s declared = %t, want %t", row.ID, row.Declared, want)
		}
	}
}

func TestGitHubCronWithdrawalNeverReachesAnotherTeam(t *testing.T) {
	st := storetest.New(t).Open(t)
	if err := st.AsOperator().TestOnlyCreateTeam(t.Context(), "other"); err != nil {
		t.Fatal(err)
	}
	arm := func(team store.Team, id string, installation int64) *store.Tenant {
		tn, err := st.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tn.ArmCronSchedule(t.Context(), store.CronSchedule{
			ID: id, RepoPath: "https://github.com/acme/widgets.git",
			GitHubInstallationID: installation, GitHubRepositoryID: 701,
			Pipeline: "nightly", Name: "default", Cron: "0 3 * * *", TZ: "UTC",
			Overlap: store.CronOverlapSkip, CatchUp: time.Hour, Where: store.CronWhereController,
		}, time.Now()); err != nil {
			t.Fatal(err)
		}
		return tn
	}
	other := arm("other", "crn_other", 7)
	mine := arm(store.DefaultTeam, "crn_mine", 9)
	for installation, want := range map[int64]int64{9: 0, 7: 1} {
		if n, err := mine.WithdrawOtherGitHubCronInstallations(t.Context(), installation, 701, time.Now()); err != nil || n != want {
			t.Fatalf("withdrawal of installations other than %d = %d, %v; want %d", installation, n, err, want)
		}
	}
	if n, err := mine.WithdrawGitHubCronRepository(t.Context(), 7, 701, time.Now()); err != nil || n != 0 {
		t.Fatalf("withdrawal through another team's installation = %d, %v", n, err)
	}
	rows, err := other.ListCronSchedules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Declared {
		t.Fatalf("another team's schedule was withdrawn: %+v", rows)
	}
}
