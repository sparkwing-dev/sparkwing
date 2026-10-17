package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestScheduleIntakeUsesControllerPolicyAndSealedQueue(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := teststore.ForTeam(t, st, store.DefaultTeam)
	now := time.Unix(1700000000, 0).UTC()
	sched, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "controller-nightly", RepoPath: "https://git.example.com/acme/repo.git", Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC",
		Where: store.CronWhereController, GitBranch: "main", ArmedBy: "owner@example.com", Paused: true,
		Args: map[string]string{"target": "production"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	server := New(st, nil)
	launcher := cronLauncher{server: server}
	first, err := launcher.Launch(t.Context(), sched, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := launcher.Launch(t.Context(), sched, now)
	if err != nil || second != first {
		t.Fatalf("same instant=%s err=%v, want %s", second, err, first)
	}
	run, err := tenant.ReadRun(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	request := run.Request.Request
	if run.Number != 1 || request.Team != string(tenant.Team()) || request.Origin != runspec.OriginSchedule || request.Source.Location.RemoteURL != sched.RepoPath || request.RequestedArgs["target"] != "production" || run.Request.AuthorityRef != (capability.AuthorityRef{Kind: "schedule", ID: sched.ID}) || request.Git.App.Set || request.Git.DefaultBranch.Set || request.Options.Runner.Value.Kind == runspec.RunnerHere {
		t.Fatalf("controller schedule changed sealed inputs: %+v", run)
	}
	queue, err := tenant.ListTriggers(t.Context(), store.TriggerFilter{Pipelines: []string{"nightly"}})
	if err != nil || len(queue) != 1 || queue[0].ID != first || queue[0].TriggerSource != "schedule" || queue[0].IdempotencyKey != sched.ID+"@"+now.Format(time.RFC3339) {
		t.Fatalf("sealed queue=%+v err=%v", queue, err)
	}
}
