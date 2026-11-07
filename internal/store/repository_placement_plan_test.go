package store

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestPlacementJobOverridesMustNameDispatchedPlanJobs(t *testing.T) {
	team := []runspec.RepositoryTier{{Target: runspec.PlacementTeam}}
	policy := func(job string, tiers []runspec.RepositoryTier) *runspec.RepositoryPlacement {
		return &runspec.RepositoryPlacement{Revision: strings.Repeat("a", 40), Jobs: []runspec.RepositoryJobPlacement{{Job: job, Prefer: tiers}}}
	}
	jobs := []nodeproto.JobSpec{{ID: "macos"}, {ID: "setup", Inline: true}}
	for name, test := range map[string]struct {
		policy *runspec.RepositoryPlacement
		jobs   []nodeproto.JobSpec
		want   string
	}{
		"no policy":           {nil, jobs, ""},
		"known job":           {policy("macos", team), jobs, ""},
		"unknown job":         {policy("windows", team), jobs, "names no job in this plan"},
		"generated job":       {policy("windows", team), append(jobs, nodeproto.JobSpec{ID: "gen", Closures: nodeproto.ClosureSpec{Generate: []string{"g"}}}), ""},
		"inline job":          {policy("setup", team), jobs, "is inline"},
		"inline job, cleared": {policy("setup", []runspec.RepositoryTier{}), jobs, ""},
	} {
		err := checkPlacementJobs(test.policy, "pre-release", test.jobs)
		if (test.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: %v, want %q", name, err, test.want)
		}
	}
}

func TestSealRepositoryPlacementOnlyForProvenRunsWithoutAPin(t *testing.T) {
	sha := strings.Repeat("a", 40)
	policy := &runspec.RepositoryPlacement{Revision: sha, Prefer: []runspec.RepositoryTier{{Target: runspec.PlacementTeam}}}
	proven, err := runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "d", VerifiedAt: time.Unix(1, 0), Ref: "refs/heads/main", SHA: sha}, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	asserted, err := runspec.AssertedProvenance(runspec.PrincipalWriter("account", "owner"), "refs/heads/main", sha, "ci")
	if err != nil {
		t.Fatal(err)
	}
	pinned := runspec.Options{Runner: runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "box"}}}}
	for name, test := range map[string]struct {
		provenance runspec.Provenance
		owner      GitHubAppRunner
		options    runspec.Options
		sealed     bool
		refused    bool
	}{
		"proven":           {proven, GitHubAppRunnerAnywhere, runspec.Options{}, true, false},
		"asserted":         {asserted, "", runspec.Options{}, false, false},
		"app runner pin":   {proven, gitHubAppRunnerCloud, runspec.Options{}, false, true},
		"per-run --runner": {proven, "", pinned, false, false},
	} {
		input := runspec.Request{Mode: runspec.IntakeExecute, Pipeline: "ci", Options: test.options}
		err := sealRepositoryPlacement(&input, policy, test.owner, test.provenance)
		if (err != nil) != test.refused || (input.Placement != nil) != test.sealed {
			t.Errorf("%s: sealed=%v err=%v", name, input.Placement != nil, err)
		}
	}
}
