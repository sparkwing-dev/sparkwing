package jobs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
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
	ctx := newTestContext(t)
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
		if (job.ID == "gate" || job.ID == "race-touched") && (job.Cores == nil || *job.Cores != overlappedGoSuiteCPUs) {
			t.Fatalf("%s lost its hosted four-core allocation: %+v", job.ID, job.Cores)
		}
		if len(job.Closures.BeforeRun) != 1 {
			t.Fatalf("%s has no baseline preparation: %+v", job.ID, job.Closures)
		}
	}
}

func TestCloudGateUsesDedicatedWidthWithoutGitHubActions(t *testing.T) {
	if runtime.NumCPU() < overlappedGoSuiteCPUs {
		t.Skip("needs four visible CPUs")
	}
	previous := runtime.GOMAXPROCS(overlappedGoSuiteCPUs)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	gateFixtureRepo(t)
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
case "$1" in
list) printf 'fixture/internal 1\n' ;;
vet) test "$GOMAXPROCS" = 2 && test "$2" = -p && test "$3" = 2 ;;
*) exit 1 ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Gate{}).Plan(ctx, plan, GateArgs{Full: true, Base: strings.Repeat("a", 40)}, sparkwing.RunContext{Pipeline: "gate"})
	})
	if _, err := host.RunStep(t.Context(), plan, "gate", "vet"); err != nil {
		t.Fatalf("Cloud gate vet must use two Go workers: %v", err)
	}
	if err := gateStep(true, func(ctx context.Context) error {
		shape := gateStepHost(ctx)
		if shards, race := storePostgresShardCount(shape), raceJobCores(shape, gateFull); shards != 4 || race != 4 {
			t.Fatalf("Cloud full gate has %d PostgreSQL shards and %d race workers, want four each", shards, race)
		}
		return nil
	})(t.Context()); err != nil {
		t.Fatal(err)
	}
}
