package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// gateFixtureRepo builds a two-module repo shaped like this one: the product
// module at the root and the pipeline module in .sparkwing/. The gate is
// pointed at it for the duration of the test.
func gateFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitInit(t, root)
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, "internal", "sound.go"),
		"package internal\n\nfunc Sound() int { return 1 }\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing", "go.mod"), "module fixture-pipelines\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs.go"),
		"package pipelines\n\nfunc Jobs() int { return 1 }\n")
	gitAddAll(t, root)
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
	return root
}

// gitInit makes dir a git work tree.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	runTestGit(t, dir, "init")
}

// gitAddAll stages everything under dir, which is what puts the fixture's
// go.mod files where the gate's module walk reads them.
func gitAddAll(t *testing.T, dir string) {
	t.Helper()
	runTestGit(t, dir, "add", "-A")
}

// writeGoFile writes content at path, creating parent directories.
func writeGoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Narrowing the Go steps back to .sparkwing/ passes this broken tree.
func TestGoStepsRefuseAnUnparseableFileInTheProductModule(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	if err := runVet(ctx); err != nil {
		t.Fatalf("clean fixture must pass vet: %v", err)
	}
	if err := runBuild(ctx); err != nil {
		t.Fatalf("clean fixture must pass build: %v", err)
	}
	if err := runTest(ctx); err != nil {
		t.Fatalf("clean fixture must pass test: %v", err)
	}

	broken := filepath.Join(root, "internal", "negative_control.go")
	writeGoFile(t, broken, "package internal\n\nfunc Broken( { this is not go\n")

	if err := runVet(ctx); err == nil {
		t.Error("vet accepted an unparseable file in the product module")
	}
	if err := runBuild(ctx); err == nil {
		t.Error("build accepted an unparseable file in the product module")
	}
	if err := runTest(ctx); err == nil {
		t.Error("test accepted an unparseable file in the product module")
	}

	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	if err := runBuild(ctx); err != nil {
		t.Fatalf("removing the broken file must clear the gate: %v", err)
	}
}

// A product module that compiles but whose tests fail must still be refused,
// so the test step is doing work the build step cannot.
func TestTestStepRefusesAFailingProductTest(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "internal", "sound_test.go"),
		"package internal\n\nimport \"testing\"\n\nfunc TestSound(t *testing.T) { t.Fatal(\"deliberate\") }\n")

	if err := runBuild(ctx); err != nil {
		t.Fatalf("a failing test must still build: %v", err)
	}
	if err := runTest(ctx); err == nil {
		t.Fatal("test accepted a failing test in the product module")
	}
}

// The pipeline module stays covered alongside the product module: a break in
// .sparkwing/ must fail the same steps.
func TestGoStepsStillCoverThePipelineModule(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, ".sparkwing", "negative_control.go"),
		"package pipelines\n\nfunc Broken( {\n")

	if err := runBuild(ctx); err == nil {
		t.Fatal("build accepted an unparseable file in the pipeline module")
	}
}

// A module added after the fact is covered the day its go.mod lands.
func TestGoStepsCoverEveryCommittedModule(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "tools", "go.mod"), "module fixture/tools\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, "tools", "broken.go"), "package tools\n\nfunc Broken( {\n")
	gitAddAll(t, root)

	if err := runBuild(ctx); err == nil {
		t.Fatal("build skipped a committed module outside the root and .sparkwing/")
	}
}

// The gate's own bindings must be absent from the test step's children, not
// merely empty: a reader that asks whether a variable is set still sees an
// empty one, and git acts on an empty GIT_INDEX_FILE.
func TestTheTestStepStripsTheGatesOwnBindingsFromItsChildren(t *testing.T) {
	ctx := context.Background()
	var probe string
	for _, name := range productTestUnset {
		t.Setenv(name, "gate")
		probe += fmt.Sprintf(`printf '%%s,' "${%s+present}"; `, name)
	}

	inherited, err := sparkwing.Bash(ctx, probe).String()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("present,", len(productTestUnset)); inherited != want {
		t.Fatalf("the fixture must export every binding to a child: got %q, want %q", inherited, want)
	}

	cleared, err := sparkwing.Bash(ctx, withoutInherited(probe, productTestUnset)).String()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat(",", len(productTestUnset)); cleared != want {
		t.Errorf("the test step's child saw a binding still set: got %q, want %q", cleared, want)
	}
}

// A suite the test step runs stages into its own repository. While the gate's
// index is bound, every git the suite spawns reads and writes that one
// instead, which is how a real repository's paths surface inside a temp-dir
// fixture.
func TestTheTestStepDoesNotHandTheGateIndexToTheSuitesItRuns(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "internal", "index_probe_test.go"), gitIndexProbe)
	gitAddAll(t, root)
	t.Setenv("GIT_INDEX_FILE", gateIndexSnapshot(t, root))

	if err := forEachGoModule(ctx, "go test", "go test ./...", nil); err == nil {
		t.Fatal("the probe must fail while the gate's index reaches it, or the pass below proves nothing")
	}
	if err := runTest(ctx); err != nil {
		t.Fatalf("the test step handed its suites the gate's index: %v", err)
	}
}

// gitIndexProbe is a product test that stages a file in a repository of its
// own. Its index holds that one file until something binds git elsewhere.
const gitIndexProbe = `package internal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStagesIntoThisSuitesOwnIndex(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init")
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "marker.txt")
	if staged := strings.TrimSpace(git("ls-files")); staged != "marker.txt" {
		t.Fatalf("staged into another repository's index: %q", staged)
	}
}
`

// gateIndexSnapshot stages root into a throwaway index, which is what
// `bitwing ticket verify` hands the gate through `sparkwing run --sw-index`.
func gateIndexSnapshot(t *testing.T, root string) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "gate.index")
	cmd := exec.Command("git", "-C", root, "add", "-A")
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage the gate's index: %v: %s", err, out)
	}
	return index
}

// A module can hold a committed go.mod and no buildable packages -- every
// package behind a build tag, or none written yet. vet and test exit 1 there
// while build exits 0, so the walk has to step over it.
func TestGoStepsSkipACommittedModuleWithNoPackages(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "empty", "go.mod"), "module fixture/empty\n\ngo 1.25\n")
	gitAddAll(t, root)

	if err := runVet(ctx); err != nil {
		t.Errorf("vet failed on a module with no packages: %v", err)
	}
	if err := runTest(ctx); err != nil {
		t.Errorf("test failed on a module with no packages: %v", err)
	}
}

// A failure in any mandatory step must land before the steps that cost more
// than it does, so the author waits on the cheapest verdict a broken tree can
// produce.
func TestEachMandatoryStepWaitsOnTheOneBeforeIt(t *testing.T) {
	w := sparkwing.NewWork()
	if _, err := (&PreCommit{}).Work(w); err != nil {
		t.Fatal(err)
	}
	chain := []string{"gofmt", "vet", "build", "test"}
	for i := 1; i < len(chain); i++ {
		if w.StepByID(chain[i]) == nil {
			t.Errorf("the gate must run step %q", chain[i])
			continue
		}
		if !stepWaitsOn(w, chain[i], chain[i-1]) {
			t.Errorf("step %q does not wait on %q", chain[i], chain[i-1])
		}
	}
}

// stepWaitsOn reports whether step depends on dep, directly or through
// another step.
func stepWaitsOn(w *sparkwing.Work, step, dep string) bool {
	s := w.StepByID(step)
	if s == nil {
		return false
	}
	for _, id := range s.DepIDs() {
		if id == dep || stepWaitsOn(w, id, dep) {
			return true
		}
	}
	return false
}
