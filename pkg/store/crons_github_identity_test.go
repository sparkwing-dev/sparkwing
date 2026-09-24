package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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
	if n, err := st.AsOperator().WithdrawOtherGitHubCronBindings(t.Context(), store.DefaultTeam, 8, 701, time.Now()); err != nil || n != 0 {
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
