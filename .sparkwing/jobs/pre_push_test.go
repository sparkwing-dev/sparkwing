package jobs

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDogfoodPipelineModuleIsTidy(t *testing.T) {
	cmd := exec.Command("go", "mod", "tidy", "-diff")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf(".sparkwing module is not tidy:\n%s", out)
	}
}

// The replace ban and the pre-commit Go steps read one module walk between
// them, so a module added after the fact is checked by both.
func TestReplaceBanReadsEveryCommittedGoMod(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err != nil {
		t.Fatalf("a fixture with no replace lines must pass: %v", err)
	}

	writeGoFile(t, filepath.Join(root, "tools", "go.mod"),
		"module fixture/tools\n\ngo 1.25\n\nreplace example.com/dep => ../dep\n")
	gitAddAll(t, root)

	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err == nil {
		t.Fatal("the replace ban skipped a committed module outside the root and .sparkwing/")
	}
}

// The one replace this repo ships stays allowed: .sparkwing/ redirects the
// sparkwing module to the parent checkout it is dogfooding.
func TestReplaceBanAllowsTheDogfoodSelfReplace(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, ".sparkwing", "go.mod"),
		"module fixture-pipelines\n\ngo 1.25\n\nreplace github.com/sparkwing-dev/sparkwing => ..\n")
	gitAddAll(t, root)

	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err != nil {
		t.Fatalf("the dogfood self-replace must be allowed: %v", err)
	}
}

// A go.mod under testdata/ is a fixture, not part of this repo's build
// surface: it is absent from go.work on purpose, so vetting it fails on
// a tree that is entirely correct.
func TestIsTestdataPath(t *testing.T) {
	inside := []string{
		"testdata/trial-repo/go.mod",
		"internal/agenttrial/testdata/trial-repo/go.mod",
		"a/b/testdata/c/go.mod",
	}
	for _, p := range inside {
		if !isTestdataPath(p) {
			t.Errorf("isTestdataPath(%q) = false, want true", p)
		}
	}

	outside := []string{
		"go.mod",
		".sparkwing/go.mod",
		"web/go.mod",
		"internal/testdatabase/go.mod",
	}
	for _, p := range outside {
		if isTestdataPath(p) {
			t.Errorf("isTestdataPath(%q) = true, want false", p)
		}
	}
}
