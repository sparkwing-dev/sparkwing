package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPrePushRunsTheFastStepsAndNothingElse(t *testing.T) {
	want := []string{
		"api-snapshot", "api-spec", "build-touched", "changelog", "comments",
		"docs-mirror", "formatters", "gofmt", "home-resolution", "test-sleeps",
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
	if hints == nil || hints.Cores <= 0 || hints.Cores > 2 {
		t.Fatalf("pre-push reserved cores = %#v, want a pin of at most two so it fits beside a running gate", hints)
	}
}

func TestBuildTouchedCompilesThePackageTheChangeTouches(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")

	writeGoFile(t, filepath.Join(root, "internal", "broken.go"), "package internal\n\nfunc Broken() int { return \"not an int\" }\n")
	gitAddAll(t, root)

	err := runBuildTouched(context.Background())
	if err == nil {
		t.Fatal("build-touched passed a package that does not compile")
	}
	if !strings.Contains(err.Error(), "internal") {
		t.Errorf("the failure names neither the module nor the package: %v", err)
	}
}

func TestBuildTouchedIgnoresChangesGoBuildNeverReads(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "vendor", "example.com", "dep", "dep.go"),
		"package dep\n\nfunc Dep() int { return \"not an int\" }\n")
	writeGoFile(t, filepath.Join(root, "internal", "sound_test.go"),
		"package internal\n\nfunc alsoNotAnInt() int { return \"not an int\" }\n")
	writeGoFile(t, filepath.Join(root, "README.md"), "# fixture\n")
	gitAddAll(t, root)

	if err := runBuildTouched(context.Background()); err != nil {
		t.Fatalf("build-touched judged a vendored, test-only, or non-Go change: %v", err)
	}
}

func TestBuildTouchedLeavesAWideChangeToTheBroadGate(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")

	writeGoFile(t, filepath.Join(root, "wide", "broken", "broken.go"),
		"package broken\n\nfunc Broken() int { return \"not an int\" }\n")
	for i := range touchedBuildPackageCap {
		dir := fmt.Sprintf("p%d", i)
		writeGoFile(t, filepath.Join(root, "wide", dir, dir+".go"),
			fmt.Sprintf("package %s\n\nfunc Sound() int { return %d }\n", dir, i))
	}
	gitAddAll(t, root)

	if err := runBuildTouched(context.Background()); err != nil {
		t.Fatalf("build-touched compiled a change above its %d-package cap: %v", touchedBuildPackageCap, err)
	}
}
