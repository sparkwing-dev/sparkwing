package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPrePushRunsTheFastStepsAndNothingElse(t *testing.T) {
	want := []string{
		"api-snapshot", "api-spec", "budget", "build-touched", "changelog",
		"lint-touched", "vet-touched",
	}
	if got := stepIDs(t, &PrePush{}); !slices.Equal(got, want) {
		t.Fatalf("pre-push steps = %v, want %v", got, want)
	}
}

func TestPrePushRunsNothingThatJudgesTheWholeTree(t *testing.T) {
	got := stepIDs(t, &PrePush{})
	for _, broad := range []string{
		"vet", "build", "test", "lint", "race-touched", "store-postgres",
		"frontend-unit", "frontend-lint", "frontend-build", "frontend-browser",
	} {
		if slices.Contains(got, broad) {
			t.Errorf("pre-push runs %q; the tier promises a verdict in seconds, and every one of these judges the whole tree", broad)
		}
	}
}

func TestPrePushStepsAllRunInParallel(t *testing.T) {
	w := sparkwing.NewWork()
	if _, err := (&PrePush{}).Work(w); err != nil {
		t.Fatal(err)
	}
	if w.ParallelFailurePolicy() != sparkwing.FailFast {
		t.Error("pre-push must fail fast: the author is holding a push open")
	}
	for _, s := range w.Steps() {
		if s.ID() == budgetStepID {
			continue
		}
		if deps := s.DepIDs(); len(deps) != 0 {
			t.Errorf("step %q waits on %v; every step here answers in about a second, so serializing them only delays the verdict", s.ID(), deps)
		}
	}
}

func TestPrePushAdmitsAheadOfTheBroadGate(t *testing.T) {
	prepush := sparkwing.NewPlan()
	if err := (&PrePush{}).Plan(t.Context(), prepush, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"}); err != nil {
		t.Fatal(err)
	}
	gate := sparkwing.NewPlan()
	if err := (&Gate{}).Plan(t.Context(), gate, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "gate"}); err != nil {
		t.Fatal(err)
	}
	if prepush.PriorityValue() <= gate.PriorityValue() {
		t.Fatalf("pre-push priority %d, gate priority %d: a tier a human waits on, queued behind a ten-minute one, is not a fast tier",
			prepush.PriorityValue(), gate.PriorityValue())
	}
	hints := prepush.ResourceHints()
	if hints == nil || hints.Cores != float64(prePushCores(runtime.NumCPU())) {
		t.Fatalf("pre-push reserved cores = %#v, want the %v its compiles are bounded to",
			hints, float64(prePushCores(runtime.NumCPU())))
	}
}

// TestPrePushFitsBesideAGateOnEveryMachine judges the reservation at fixed core
// counts rather than at this machine's, so a runner smaller than the author's
// box reaches the same verdict.
func TestPrePushFitsBesideAGateOnEveryMachine(t *testing.T) {
	for _, cpuCount := range []int{1, 2, 4, 8, 14, 64} {
		reserved := float64(prePushCores(cpuCount))
		if reserved > gateCoreReservation(cpuCount) {
			t.Errorf("on %d cores pre-push reserves %v, more than the gate's %v, so the fast tier no longer fits beside a running gate",
				cpuCount, reserved, gateCoreReservation(cpuCount))
		}
		if reserved < 1 {
			t.Errorf("on %d cores pre-push reserves %v, which schedules nothing", cpuCount, reserved)
		}
		if reserved > prePushCoreCap {
			t.Errorf("on %d cores pre-push reserves %v, past the %d its compiles need", cpuCount, reserved, prePushCoreCap)
		}
		want := fmt.Sprintf("GOMAXPROCS=%d", prePushCores(cpuCount))
		if got := goCommandAt(prePushCores(cpuCount), "build", "./x"); !strings.Contains(got, want) {
			t.Errorf("on %d cores the touched compile is not bounded to the reservation: %q, want %s", cpuCount, got, want)
		}
	}
}

func TestBuildTouchedCompilesThePackageTheChangeTouches(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	writeGoFile(t, filepath.Join(root, "internal", "broken.go"), "package internal\n\nfunc Broken() int { return \"not an int\" }\n")
	gitCommitAll(t, root, "a package that does not compile")

	err := runBuildTouched(context.Background())
	if err == nil {
		t.Fatal("build-touched passed a package that does not compile")
	}
	if !strings.Contains(err.Error(), "internal") {
		t.Errorf("the failure names neither the module nor the package: %v", err)
	}
}

func TestBuildTouchedRejectsBrokenPackagesInAWidePush(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	for i := range 9 {
		pkg := fmt.Sprintf("wide%d", i)
		writeGoFile(t, filepath.Join(root, pkg, "broken.go"),
			fmt.Sprintf("package %s\n\nfunc Broken() int { return \"not an int\" }\n", pkg))
	}
	gitCommitAll(t, root, "a push wider than the tier fits")

	if err := runBuildTouched(t.Context()); err == nil {
		t.Fatal("build-touched passed nine packages that do not compile")
	}
}

func TestBuildTouchedIgnoresChangesGoBuildNeverReads(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	writeGoFile(t, filepath.Join(root, "vendor", "example.com", "dep", "dep.go"),
		"package dep\n\nfunc Dep() int { return \"not an int\" }\n")
	writeGoFile(t, filepath.Join(root, "internal", "sound_test.go"),
		"package internal\n\nfunc alsoNotAnInt() int { return \"not an int\" }\n")
	writeGoFile(t, filepath.Join(root, "README.md"), "# fixture\n")
	gitCommitAll(t, root, "a vendored, test-only and non-Go change")

	if err := runBuildTouched(context.Background()); err != nil {
		t.Fatalf("build-touched judged a vendored, test-only, or non-Go change: %v", err)
	}
}

func TestThePushTierJudgesThePushedCommitsWhateverIsStaged(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	writeGoFile(t, filepath.Join(root, "internal", "broken.go"),
		"package internal\n\nfunc Broken() int { return \"not an int\" }\n")
	gitCommitAll(t, root, "the commit being pushed")

	writeGoFile(t, filepath.Join(root, "internal", "unrelated.go"),
		"package internal\n\nfunc Unrelated() int { return 7 }\n")
	gitAddAll(t, root)

	if err := runBuildTouched(context.Background()); err == nil {
		t.Fatal("a staged unrelated file narrowed the push tier, so the pushed commit went unjudged")
	}
	if err := runVetTouched(context.Background()); err == nil {
		t.Error("vet-touched read the index instead of the push range")
	}
}

func TestVetTouchedJudgesTheTestFilesBuildNeverReads(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	writeGoFile(t, filepath.Join(root, "internal", "broken_test.go"),
		"package internal\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) { var n int = \"not an int\"; _ = n }\n")
	gitCommitAll(t, root, "a test that does not compile")

	if err := runBuildTouched(context.Background()); err != nil {
		t.Fatalf("build-touched judged a test file: %v", err)
	}
	if err := runVetTouched(context.Background()); err == nil {
		t.Error("vet-touched passed a test file that does not compile")
	}
}

func TestThePushTierRepeatsNothingTheCommitTierAlreadyRan(t *testing.T) {
	commit := stepIDs(t, &PreCommit{})
	for _, id := range stepIDs(t, &PrePush{}) {
		if id == budgetStepID {
			continue
		}
		if slices.Contains(commit, id) {
			t.Errorf("pre-push runs %q, which pre-commit already ran over the same files: the tiers shift left, and a push pays only for what the whole change since main can answer", id)
		}
	}
}
