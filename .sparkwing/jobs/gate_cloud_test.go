package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestCloudGateFetchesTheExactPublicBaseForAParentlessSnapshot(t *testing.T) {
	origin := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(origin, "HISTORY.md"), "old "+trackerID+"\n")
	gitCommitAll(t, origin, "base")
	base := strings.TrimSpace(gitRun(t, origin, "rev-parse", "HEAD"))
	snapshot := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(snapshot, "HISTORY.md"), "old "+trackerID+"\n")
	writeGoFile(t, filepath.Join(snapshot, "changed.go"), "package fixture\n\nfunc Changed() {}\n")
	gitCommitAll(t, snapshot, "uploaded")
	ctx := context.Background()
	if err := fetchGateBase(ctx, origin, base); err != nil {
		t.Fatal(err)
	}
	got, scope, err := resolveGateBase(ctx)
	if err != nil || got != base || !strings.Contains(scope, "snapshot change") {
		t.Fatalf("base=%s scope=%s err=%v", got, scope, err)
	}
	if err := checkTrackerIDs(ctx); err != nil {
		t.Fatalf("historical findings escaped the fetched comparison: %v", err)
	}
}

func TestCloudGateBaseIsValidatedBeforeFetching(t *testing.T) {
	for _, base := range []string{"main", "--all", strings.Repeat("a", 39), strings.Repeat("g", 40)} {
		if err := fetchGateBase(t.Context(), "unreachable", base); err == nil || !strings.Contains(err.Error(), "full commit SHA") {
			t.Fatalf("base=%q err=%v", base, err)
		}
	}
}

func TestCloudGatePreparesTheBaselineInEveryJobWorkspace(t *testing.T) {
	doc, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Gate", map[string]string{"full": "true", "base": strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Jobs) != 4 {
		t.Fatalf("missing gate jobs: %+v", doc.Jobs)
	}
	for _, job := range doc.Jobs {
		if (job.ID == "gate" || job.ID == "race-touched" || job.ID == policyJobID) && (job.Cores == nil || *job.Cores != overlappedGoSuiteCPUs) {
			t.Fatalf("%s lost its hosted four-core allocation: %+v", job.ID, job.Cores)
		}
		want := 2
		if job.ID == recordTreeJob {
			want = 1
		} else if len(job.Caches) != 3 {
			t.Fatalf("%s has no tool, Go build and module caches: %+v", job.ID, job.Caches)
		}
		if len(job.Closures.BeforeRun) != want {
			t.Fatalf("%s has no baseline preparation: %+v", job.ID, job.Closures)
		}
	}
}
