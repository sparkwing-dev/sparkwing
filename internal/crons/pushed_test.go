package crons

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const pushedRepoURL = "https://github.com/acme/widgets.git"

type pushedFixture struct {
	*Service
	Store *store.Store
}

func pushedService(t *testing.T) *pushedFixture {
	t.Helper()
	st, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &pushedFixture{Service: &Service{Operator: st.AsOperator(), Schedules: teststore.ForTeam(t, st, store.DefaultTeam), ArmedBy: "operator@laptop"}, Store: st}
}

func TestDisarmRepoURLRemovesOnlyThatRepository(t *testing.T) {
	svc := pushedService(t)
	for i, repo := range []string{pushedRepoURL, "https://github.com/acme/other.git"} {
		_, _, err := svc.Schedules.ArmCronSchedule(t.Context(), store.CronSchedule{ID: []string{"first", "second"}[i], RepoPath: repo, Pipeline: "ci", Cron: "0 * * * *", Where: store.CronWhereController, GitHubInstallationID: 7, GitHubRepositoryID: int64(701 + i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	removed, err := svc.disarmRepoURL(t.Context(), pushedRepoURL)
	if err != nil || removed != 1 {
		t.Fatalf("removed %d: %v", removed, err)
	}
	rows, err := svc.Schedules.ListCronSchedules(t.Context())
	if err != nil || len(rows) != 1 || rows[0].RepoPath != "https://github.com/acme/other.git" {
		t.Fatalf("remaining rows: %+v %v", rows, err)
	}
}

func TestControllerHealthReportsTimerLoop(t *testing.T) {
	svc := pushedService(t)
	health, err := svc.ControllerHealth(context.Background())
	if err != nil || health.Timer.Detail != controllerTimerDetail {
		t.Fatalf("health: %+v %v", health, err)
	}
}

func TestPushedRepoRecognizesCloneURL(t *testing.T) {
	if !pushedRepo(pushedRepoURL) || pushedRepo("/tmp/checkout") {
		t.Fatal("clone URL classification")
	}
}
