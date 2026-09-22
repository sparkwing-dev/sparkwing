package jobs

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRaceTargetsGroupChangedFilesByPackageAndModule(t *testing.T) {
	modules := []string{".", ".sparkwing", "internal/agenttrial/testdata/trial-repo"}
	files := []string{
		"internal/orchestrator/orchestrator.go",
		"internal/orchestrator/orchestrator_test.go",
		"pkg/store/store.go",
		"main.go",
		".sparkwing/jobs/pre_commit.go",
		".sparkwing/main.go",
		"internal/agenttrial/testdata/trial-repo/main.go",
		"testdata/k8s-e2e/repo/.sparkwing/main.go",
	}
	want := map[string][]string{
		".":          {"./", "./internal/orchestrator", "./pkg/store"},
		".sparkwing": {"./", "./jobs"},
	}
	if got := touchedPackageTargets(files, modules); !reflect.DeepEqual(got, want) {
		t.Fatalf("touchedPackageTargets = %v, want %v", got, want)
	}
}

func TestRaceTargetsAreEmptyWhenNothingChanged(t *testing.T) {
	if got := touchedPackageTargets(nil, []string{".", ".sparkwing"}); len(got) != 0 {
		t.Fatalf("touchedPackageTargets(nil) = %v, want none", got)
	}
}

func TestRaceCommandBoundsPackageOverlapOnFourCPUs(t *testing.T) {
	const args = "-race -count=1 ./internal/orchestrator ./pkg/store"
	for _, tc := range []struct {
		cpus int
		want string
	}{
		{3, "GOMAXPROCS=1 go test -p 1 " + args},
		{4, "GOMAXPROCS=1 go test -p 2 " + args},
		{8, "GOMAXPROCS=3 go test -p 3 " + args},
	} {
		if got := raceGoCommand(hostShape{cpus: tc.cpus}, args); got != tc.want {
			t.Errorf("race command on %d CPUs = %q, want %q", tc.cpus, got, tc.want)
		}
	}
}

func TestGateRaceTargetsDeferTheStore(t *testing.T) {
	targets := map[string][]string{
		".":          {"./internal/orchestrator", "./pkg/store"},
		".sparkwing": {"./jobs"},
	}
	kept, deferred := raceTargetsForGate(targets)
	want := map[string][]string{
		".":          {"./internal/orchestrator"},
		".sparkwing": {"./jobs"},
	}
	if !reflect.DeepEqual(kept, want) {
		t.Errorf("raceTargetsForGate kept %v, want %v", kept, want)
	}
	if !reflect.DeepEqual(deferred, []string{"./pkg/store"}) {
		t.Errorf("deferred %v, want [./pkg/store]", deferred)
	}
}

// A module holding nothing but the store drops out rather than running an
// empty race command that would report success over no packages.
func TestGateRaceTargetsDropAModuleLeftEmpty(t *testing.T) {
	kept, deferred := raceTargetsForGate(map[string][]string{".": {"./pkg/store"}})
	if len(kept) != 0 {
		t.Errorf("raceTargetsForGate kept %v, want nothing", kept)
	}
	if !reflect.DeepEqual(deferred, []string{"./pkg/store"}) {
		t.Errorf("deferred %v, want [./pkg/store]", deferred)
	}
}

// safety: the gate stops racing the store only because pre-release starts. A
// release that dropped the step would ship a store race nothing looked for.
func TestPreReleaseRacesTheStore(t *testing.T) {
	var found string
	for _, check := range preReleaseChecks() {
		if check.id == "race-store" {
			found = check.id
		}
	}
	if found == "" {
		t.Fatal("pre-release has no race-store check; the gate defers pkg/store to it")
	}
}

func TestRaceTouchedPassesWhenNoGoFileChanged(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	if err := runRaceTouched(context.Background()); err != nil {
		t.Fatalf("race-touched failed with nothing changed: %v", err)
	}
}

func TestRaceTouchedRunsTheRaceDetectorOnTheChangedPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")

	writeGoFile(t, filepath.Join(root, "internal", "racy_test.go"), racyTest)
	gitAddAll(t, root)

	err := runRaceTouched(context.Background())
	if err == nil {
		t.Fatal("race-touched passed a package whose test races")
	}
	if !strings.Contains(err.Error(), "internal") {
		t.Errorf("the failure does not name the module or package: %v", err)
	}
}

const racyTest = `package internal

import (
	"sync"
	"testing"
)

func TestRaces(t *testing.T) {
	n := 0
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n++
		}()
	}
	wg.Wait()
	if n < 0 {
		t.Fatal("unreachable")
	}
}
`
