package controller

import (
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestLocalRunnerListsTheMachineWithItsLabelsAndCurrentLastSeen(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	team := string(store.DefaultTeam)
	stale := now.Add(-3 * time.Hour)
	snapshot := capability.RunnerSnapshot{Now: now, Runners: []capability.RunnerObservation{
		{ID: "award:old", Team: team, Runner: nodeproto.RunnerIdentity{Name: "holder-1", Type: "local"}, Location: "local", LastSeen: stale, ActiveJobs: []string{"run-b/build"}},
		{ID: "award:new", Team: team, Runner: nodeproto.RunnerIdentity{Name: "holder-2", Type: "local"}, Location: "local", LastSeen: stale, ActiveJobs: []string{"run-a/test"}},
		{ID: "machine:p", Team: team, Runner: nodeproto.RunnerIdentity{Name: "box", Type: "runner"}, LastSeen: stale, ActiveJobs: []string{}},
	}}
	got := mergeLocalRunner(snapshot, team, &LocalRunner{Name: "laptop", Labels: []string{"gpu"}})
	if len(got.Runners) != 2 {
		t.Fatalf("runners = %+v, want the registered runner and one local row", got.Runners)
	}
	local := got.Runners[1]
	if local.Runner.Name != "laptop" || local.Runner.Type != "local" || !slices.Equal(local.Runner.Labels, []string{"gpu"}) {
		t.Errorf("local row identity = %+v, want laptop/local with runner.labels", local.Runner)
	}
	if !local.LastSeen.Equal(now) {
		t.Errorf("local last_seen = %s, want the read time %s", local.LastSeen, now)
	}
	if !slices.Equal(local.ActiveJobs, []string{"run-a/test", "run-b/build"}) {
		t.Errorf("local active jobs = %v, want both awards' jobs", local.ActiveJobs)
	}
	if got.Runners[0].ID != "machine:p" {
		t.Errorf("registered runner changed: %+v", got.Runners[0])
	}

	idle := mergeLocalRunner(capability.RunnerSnapshot{Now: now}, team, &LocalRunner{Name: "laptop"})
	if len(idle.Runners) != 1 || idle.Runners[0].Runner.Name != "laptop" {
		t.Errorf("an idle machine is missing from its own inventory: %+v", idle.Runners)
	}
	if hosted := mergeLocalRunner(snapshot, team, nil); len(hosted.Runners) != 3 {
		t.Errorf("a controller without a local runner rewrote the inventory: %+v", hosted.Runners)
	}
}
