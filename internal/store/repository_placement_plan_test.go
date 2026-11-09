package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

var placementSHA = strings.Repeat("a", 40)

func teamTier(labels ...string) runspec.RepositoryTier {
	return runspec.RepositoryTier{Target: runspec.PlacementTeam, Labels: labels}
}

func cloudTier() runspec.RepositoryTier {
	return runspec.RepositoryTier{Target: runspec.PlacementCloud}
}

func placementRow(t *testing.T, job nodewire.JobSpec, requires ...string) runstate.NodeRow {
	t.Helper()
	spec, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	row := runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(job.ID)}, Spec: spec}
	row.Execution.Requires.Value = requires
	return row
}

func TestJobPlacementBindsIDsAndRefusesUnplaceableJobs(t *testing.T) {
	policy := &runspec.RepositoryPlacement{Revision: placementSHA, Jobs: []runspec.RepositoryJobPlacement{
		{Job: "macos", Prefer: []runspec.RepositoryTier{teamTier("os=darwin")}},
		{Job: "setup", Prefer: []runspec.RepositoryTier{teamTier()}},
		{Job: "docs", Prefer: []runspec.RepositoryTier{cloudTier()}},
		{Job: "cleared", Prefer: []runspec.RepositoryTier{}},
	}}
	for name, test := range map[string]struct {
		row  runstate.NodeRow
		want string
	}{
		"override for a job this plan omits is inert": {placementRow(t, nodewire.JobSpec{ID: "pre-release"}), ""},
		"listed dispatched job":                       {placementRow(t, nodewire.JobSpec{ID: "macos"}, "os=darwin"), ""},
		"inline job cannot move":                      {placementRow(t, nodewire.JobSpec{ID: "setup", Inline: true}), "is inline"},
		"inline job with a cleared list":              {placementRow(t, nodewire.JobSpec{ID: "cleared", Inline: true}), ""},
		"darwin job on a Cloud-only list":             {placementRow(t, nodewire.JobSpec{ID: "docs"}, "os=darwin"), "no tier of its placement"},
		"darwin job on a Windows team tier":           {placementRow(t, nodewire.JobSpec{ID: "macos"}, "os=windows"), "no tier of its placement"},
		"Linux job on a Cloud-only list":              {placementRow(t, nodewire.JobSpec{ID: "docs"}, "os=linux,os=darwin"), ""},
	} {
		err := checkJobPlacement(policy, test.row)
		if (test.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: %v, want %q", name, err, test.want)
		}
	}
}

func TestRepositoryPlacementConflictsWithEveryRunnerPin(t *testing.T) {
	policy := &runspec.RepositoryPlacement{Revision: placementSHA, Prefer: []runspec.RepositoryTier{teamTier()}}
	pin := func(kind runspec.RunnerSelectionKind, value string) runspec.Options {
		return runspec.Options{Runner: runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: kind, Value: value}}}}
	}
	for name, test := range map[string]struct {
		owner   GitHubAppRunner
		options runspec.Options
		burst   bool
		want    string
	}{
		"App runner anywhere":  {GitHubAppRunnerAnywhere, runspec.Options{}, false, ""},
		"runner any":           {"", pin(runspec.RunnerAny, ""), false, ""},
		"App runner cloud":     {gitHubAppRunnerCloud, runspec.Options{}, false, "GitHub App runner setting cloud"},
		"per-run --runner":     {"", pin(runspec.RunnerName, "box"), false, "runner selection name box"},
		"per-run label runner": {"", pin(runspec.RunnerLabel, "gpu"), false, "runner selection label gpu"},
		"burst routing":        {"", runspec.Options{}, true, "burst routing"},
	} {
		input := runspec.Request{Mode: runspec.IntakeExecute, Pipeline: "ci", Options: test.options}
		if test.burst {
			input.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}}
		}
		err := sealRepositoryPlacement(&input, policy, nil, "", test.owner)
		if test.want == "" && (err != nil || input.Placement == nil) || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "placement of pipeline ci")) {
			t.Errorf("%s: sealed=%v err=%v, want %q", name, input.Placement != nil, err, test.want)
		}
	}
}

func TestEmptyRepositoryListSealsNothingAndConflictsWithNothing(t *testing.T) {
	policy := &runspec.RepositoryPlacement{Revision: placementSHA, Prefer: []runspec.RepositoryTier{}, Jobs: []runspec.RepositoryJobPlacement{{Job: "a", Prefer: []runspec.RepositoryTier{}}}}
	input := runspec.Request{Mode: runspec.IntakeExecute, Pipeline: "ci"}
	if err := sealRepositoryPlacement(&input, policy, nil, "", gitHubAppRunnerCloud); err != nil || input.Placement != nil {
		t.Fatalf("an empty list conflicted or sealed: sealed=%v err=%v", input.Placement != nil, err)
	}
}

func TestChildTakesItsParentJobsListAndNeverItsOwn(t *testing.T) {
	parent := &runspec.RepositoryPlacement{Revision: placementSHA, Prefer: []runspec.RepositoryTier{teamTier(), cloudTier()}, Jobs: []runspec.RepositoryJobPlacement{{Job: "free", Prefer: []runspec.RepositoryTier{}}}}
	own := &runspec.RepositoryPlacement{Revision: strings.Repeat("b", 40), Prefer: []runspec.RepositoryTier{teamTier("os=darwin")}}
	for name, test := range map[string]struct {
		own       *runspec.RepositoryPlacement
		parentJob string
		want      int
	}{
		"bound child inherits the parent job's tiers": {nil, "build", 2},
		"bound child ignores its own list":            {own, "build", 2},
		"child of an unbound job has no list":         {own, "free", 0},
		"any parent job without an override binds":    {own, "deploy", 2},
	} {
		input := runspec.Request{Mode: runspec.IntakeExecute, Pipeline: "child"}
		if err := sealRepositoryPlacement(&input, test.own, parent, test.parentJob, GitHubAppRunnerAnywhere); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := len(input.Placement.ForJob("job")); got != test.want || test.want > 0 && input.Placement.Revision != placementSHA {
			t.Errorf("%s: %+v, want %d tiers from the parent", name, input.Placement, test.want)
		}
	}
	input := runspec.Request{Mode: runspec.IntakeExecute, Pipeline: "child"}
	if err := sealRepositoryPlacement(&input, own, nil, "build", ""); err != nil || input.Placement != nil {
		t.Fatalf("a child of a run without a list took its own list: %+v %v", input.Placement, err)
	}
	input = runspec.Request{Mode: runspec.IntakeExecute, Pipeline: "child", Options: runspec.Options{Runner: runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "box"}}}}}
	if err := sealRepositoryPlacement(&input, nil, parent, "build", ""); err == nil {
		t.Fatal("a child's own runner selection escaped its parent's list")
	}
}

func TestChildIntakeIsBoundByItsParentJobsList(t *testing.T) {
	bind := func(t *testing.T, tenant *Tenant) {
		row, err := tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		row.Request.Request.Placement = &runspec.RepositoryPlacement{Revision: placementSHA, Prefer: []runspec.RepositoryTier{teamTier("os=darwin")}}
		body, err := json.Marshal(row.Request)
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
	}
	tenant, request := childIntakeFixture(t)
	bind(t, tenant)
	child, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || child.Request.Request.Placement == nil || len(child.Request.Request.Placement.Prefer) != 1 || child.Request.Request.Placement.Prefer[0].Labels[0] != "os=darwin" {
		t.Fatalf("child did not inherit its parent job's list: %+v %v", child.Request.Request.Placement, err)
	}
	tenant, request = childIntakeFixture(t)
	bind(t, tenant)
	request.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "box"}}}
	if _, err := tenant.AdmitRun(t.Context(), request); err == nil || !strings.Contains(err.Error(), "runner selection name box") {
		t.Fatalf("a child runner selection escaped its parent's list: %v", err)
	}
}
