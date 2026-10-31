package jobs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRaceTargetsGroupChangedFilesByPackageAndModule(t *testing.T) {
	modules := []string{".", ".sparkwing", "internal/agenttrial/testdata/trial-repo"}
	files := []string{
		"internal/orchestrator/orchestrator.go",
		"internal/orchestrator/orchestrator_test.go",
		"internal/store/store.go",
		"main.go",
		".sparkwing/jobs/pre_commit.go",
		".sparkwing/main.go",
		"internal/agenttrial/testdata/trial-repo/main.go",
		"testdata/k8s-e2e/repo/.sparkwing/main.go",
	}
	want := map[string][]string{
		".":          {"./", "./internal/orchestrator", "./internal/store"},
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
	const args = "-race -count=1 ./internal/orchestrator ./internal/store"
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
		".":          {"./internal/orchestrator", "./internal/store"},
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
	if !reflect.DeepEqual(deferred, []string{"./internal/store"}) {
		t.Errorf("deferred %v, want [./internal/store]", deferred)
	}
}

// A module holding nothing but the store drops out rather than running an
// empty race command that would report success over no packages.
func TestGateRaceTargetsDropAModuleLeftEmpty(t *testing.T) {
	kept, deferred := raceTargetsForGate(map[string][]string{".": {"./internal/store"}})
	if len(kept) != 0 {
		t.Errorf("raceTargetsForGate kept %v, want nothing", kept)
	}
	if !reflect.DeepEqual(deferred, []string{"./internal/store"}) {
		t.Errorf("deferred %v, want [./internal/store]", deferred)
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
		t.Fatal("pre-release has no race-store check; the gate defers internal/store to it")
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

func TestNodehostRaceShardsKeepTheHostsPackageBound(t *testing.T) {
	for _, cpus := range []int{1, 2, 3, 4, 5, 8} {
		want := 2
		if cpus < 4 {
			want = 0
		}
		if got := nodehostRaceShardCount(hostShape{cpus: cpus}); got != want {
			t.Errorf("%d CPUs: %d nodehost shards, want %d", cpus, got, want)
		}
	}
	args := nodehostRaceShardArgs([]string{"TestA", "TestB"})
	if slices.Contains(args, "-test.short") || !slices.Contains(args, "-test.count=1") || !slices.Contains(args, "-test.timeout=10m") {
		t.Fatalf("nodehost race shard args=%q, want full class once under 10m", args)
	}
}

func TestRaceModulesShardsNodehostAfterOrdinaryPackages(t *testing.T) {
	t.Setenv("GOMAXPROCS", "8")
	if nodehostRaceShardCount(currentHost()) == 0 {
		t.Skip("host has capacity for only one package process")
	}
	root := t.TempDir()
	t.Chdir(root)
	writeGoFile(t, filepath.Join(root, "go.mod"), "module example.test/shards\n\ngo 1.26.0\n")
	writeGoFile(t, filepath.Join(root, "internal", "ordinary", "ordinary_test.go"), `package ordinary
import ("os"; "testing")
func TestOrdinary(t *testing.T) {
	if err := os.WriteFile("../ordinary-complete", nil, 0600); err != nil { t.Fatal(err) }
}
`)
	writeGoFile(t, filepath.Join(root, "internal", "nodehost", "host_test.go"), `package nodehost
import ("os"; "runtime"; "testing"; "time")
func init() {
	if runtime.GOMAXPROCS(0) != 1 { panic("nodehost startup exceeded one runtime thread") }
}
func check(t *testing.T) {
	if testing.Short() { t.Fatal("nodehost skipped the full class") }
	if runtime.GOMAXPROCS(0) != 1 { t.Fatal("nodehost exceeded one runtime thread") }
	if deadline, ok := t.Deadline(); !ok || time.Until(deadline) < 9*time.Minute || time.Until(deadline) > 10*time.Minute {
		t.Fatalf("deadline=%v, want ten-minute shard bound", deadline)
	}
	if _, err := os.Stat("../ordinary-complete"); err != nil { t.Fatal("ordinary packages did not finish before shards", err) }
	file, err := os.OpenFile(t.Name(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil { t.Fatal("test ran more than once", err) }
	if err := file.Close(); err != nil { t.Fatal(err) }
}
func TestA(t *testing.T) { check(t) }
func TestB(t *testing.T) { check(t) }
`)
	if err := raceModules(t.Context(), map[string][]string{".": {"./internal/nodehost", "./internal/ordinary"}}, t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TestA", "TestB"} {
		if _, err := os.Stat(filepath.Join(root, "internal", "nodehost", name)); err != nil {
			t.Fatalf("shards omitted %s: %v", name, err)
		}
	}
	for _, name := range []string{"TestA", "TestB"} {
		if err := os.Remove(filepath.Join(root, "internal", "nodehost", name)); err != nil {
			t.Fatal(err)
		}
	}
	writeGoFile(t, filepath.Join(root, "internal", "nodehost", "race_test.go"), strings.Replace(racyTest, "package internal", "package nodehost", 1))
	if err := raceModules(t.Context(), map[string][]string{".": {"./internal/nodehost"}}, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "race shard") {
		t.Fatalf("racing shard=%v, want shard failure", err)
	}
}
