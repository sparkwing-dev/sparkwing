package client_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const cronsClientRepoURL = "https://github.com/acme/widgets.git"

func newCronsClient(t *testing.T) (*client.Client, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeRunsRead, controller.ScopeRunsWrite}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return client.NewWithToken(srv.URL, srv.Client(), raw), st
}

func cronsClientPush() client.CronRepoRequest {
	return client.CronRepoRequest{
		RepoURL: cronsClientRepoURL,
		Branch:  "main",
		SHA:     "0123456789abcdef0123456789abcdef01234567",
		Schedules: []client.CronRepoSchedule{
			{Pipeline: "nightly", Cron: "0 3 * * *", TZ: "UTC", Args: map[string]string{"depth": "deep"}},
			{Pipeline: "sweep", Name: "quick", Cron: "*/5 * * * *", Overlap: store.CronOverlapQueue, CatchUp: "2h"},
		},
	}
}

func TestClientCronsPushListAndRead(t *testing.T) {
	c, _ := newCronsClient(t)
	ctx := context.Background()

	pushed, err := c.PutCronRepo(ctx, cronsClientPush())
	if err != nil {
		t.Fatalf("PutCronRepo: %v", err)
	}
	if len(pushed.Schedules) != 2 {
		t.Fatalf("pushed %d schedules, want two", len(pushed.Schedules))
	}

	overview, err := c.ListCrons(ctx)
	if err != nil {
		t.Fatalf("ListCrons: %v", err)
	}
	if len(overview.Schedules) != 2 || overview.Health.Armed != 2 {
		t.Fatalf("overview = %+v", overview.Health)
	}

	detail, err := c.GetCron(ctx, "acme/widgets/sweep/quick")
	if err != nil {
		t.Fatalf("GetCron: %v", err)
	}
	if detail.Schedule.Effective.Overlap != store.CronOverlapQueue {
		t.Errorf("overlap = %q, want queue", detail.Schedule.Effective.Overlap)
	}
	if detail.Schedule.Effective.CatchUpNS != int64(2*time.Hour) {
		t.Errorf("catch up = %d, want 2h", detail.Schedule.Effective.CatchUpNS)
	}
	if len(detail.Upcoming) == 0 {
		t.Error("detail names no upcoming instants")
	}
}

func TestClientCronsDrivesOneSchedule(t *testing.T) {
	c, st := newCronsClient(t)
	ctx := context.Background()
	if _, err := c.PutCronRepo(ctx, cronsClientPush()); err != nil {
		t.Fatalf("PutCronRepo: %v", err)
	}
	const name = "acme/widgets/nightly"

	paused, err := c.PauseCron(ctx, name)
	if err != nil {
		t.Fatalf("PauseCron: %v", err)
	}
	if paused.State != crons.StatePaused {
		t.Errorf("state = %q, want paused", paused.State)
	}
	resumed, err := c.ResumeCron(ctx, name)
	if err != nil {
		t.Fatalf("ResumeCron: %v", err)
	}
	if resumed.State != crons.StateArmed {
		t.Errorf("state = %q, want armed", resumed.State)
	}

	overridden, err := c.SetCronOverride(ctx, name, client.CronOverrideRequest{Cron: "0 5 * * *"})
	if err != nil {
		t.Fatalf("SetCronOverride: %v", err)
	}
	if overridden.Effective.Cron != "0 5 * * *" {
		t.Errorf("effective cron = %q", overridden.Effective.Cron)
	}
	cleared, err := c.ClearCronOverride(ctx, name)
	if err != nil {
		t.Fatalf("ClearCronOverride: %v", err)
	}
	if cleared.Effective.Cron != "0 3 * * *" {
		t.Errorf("effective cron = %q, want the declaration back", cleared.Effective.Cron)
	}

	launched, err := c.RunCronNow(ctx, name)
	if err != nil {
		t.Fatalf("RunCronNow: %v", err)
	}
	if launched.RunID == "" {
		t.Fatal("run now named no run")
	}
	if _, err := st.GetRun(ctx, launched.RunID); err != nil {
		t.Errorf("the launch wrote no run: %v", err)
	}

	if _, err := c.DisarmCron(ctx, name); err != nil {
		t.Fatalf("DisarmCron: %v", err)
	}
	removed, err := c.DeleteCronRepo(ctx, cronsClientRepoURL)
	if err != nil {
		t.Fatalf("DeleteCronRepo: %v", err)
	}
	if removed.Removed != 1 {
		t.Errorf("removed = %d, want the one remaining row", removed.Removed)
	}
}

func TestClientCronsReportsAMissingSchedule(t *testing.T) {
	c, _ := newCronsClient(t)
	if _, err := c.GetCron(context.Background(), "crn_nosuchrow"); err == nil {
		t.Fatal("a missing schedule read clean")
	}
}
