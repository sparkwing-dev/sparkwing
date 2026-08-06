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

// gateIndexSnapshot stages root into the throwaway index accepted by
// `sparkwing run --sw-index`.
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
	chain := []string{"gofmt", "formatters", "vet", "build", "test", "lint"}
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

// The sweeps read this file too, so the fixtures spell their patterns with an
// escape and a join rather than literally. Written out, they are the real
// thing; read as source, neither trips the check under test.
const (
	emDash    = "\u2014"
	trackerID = "TOD" + "-42"
)

// gitCommitAll stages and commits everything under dir, so later staging
// produces a diff against a real HEAD rather than against an empty tree.
func gitCommitAll(t *testing.T, dir, message string) {
	t.Helper()
	gitAddAll(t, dir)
	runTestGit(t, dir, "-c", "user.name=gate", "-c", "user.email=gate@example.com",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
}

// A commit is judged on what it changes. The sweeps once read the whole
// tracked tree, so history nobody touched could refuse an unrelated commit.
func TestRegexSweepsIgnoreAFileTheCommitDoesNotTouch(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "FEEDBACK.md"), "a dash "+emDash+" and a "+trackerID+" id\n")
	gitCommitAll(t, root, "history the commit does not touch")

	writeGoFile(t, filepath.Join(root, "internal", "clean.go"),
		"package internal\n\nfunc Clean() int { return 2 }\n")
	gitAddAll(t, root)

	if err := checkEmDashes(ctx); err != nil {
		t.Errorf("em-dash sweep charged the commit for untouched history: %v", err)
	}
	if err := checkTrackerIDs(ctx); err != nil {
		t.Errorf("tracker-id sweep charged the commit for untouched history: %v", err)
	}
}

// The narrowing must not disarm the sweeps: content the commit actually
// introduces is still refused.
func TestRegexSweepsRefuseWhatTheStagedChangeIntroduces(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		check func(context.Context) error
	}{
		{"em dash", "package internal\n\n// Note " + emDash + " here.\nfunc Bad() int { return 3 }\n", checkEmDashes},
		{"tracker id", "package internal\n\n// See " + trackerID + ".\nfunc Bad() int { return 3 }\n", checkTrackerIDs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			gitCommitAll(t, root, "clean base")

			writeGoFile(t, filepath.Join(root, "internal", "bad.go"), tc.body)
			gitAddAll(t, root)

			if err := tc.check(context.Background()); err == nil {
				t.Fatal("the sweep passed a staged change that introduces the pattern")
			}
		})
	}
}

// The whole-tree audit is how pre-existing drift still gets found, off the
// critical path of an unrelated commit.
func TestRegexSweepAllReadsPastTheStagedChange(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "FEEDBACK.md"), "a dash "+emDash+" here\n")
	gitCommitAll(t, root, "history the commit does not touch")

	writeGoFile(t, filepath.Join(root, "internal", "clean.go"),
		"package internal\n\nfunc Clean() int { return 2 }\n")
	gitAddAll(t, root)

	t.Setenv("SPARKWING_REGEX_SWEEP_ALL", "1")
	if err := checkEmDashes(ctx); err == nil {
		t.Fatal("the whole-tree audit missed an em dash outside the staged change")
	}
}

// gofumptDrift is formatted for gofmt and not for gofumpt: gofumpt's extra
// rules collapse the empty line after the opening brace. gofmt passing it is
// the point -- gofmt is the only formatting the gate enforced, and gofumpt
// with extra-rules is a strict superset of it, so a tree stays gofmt-clean
// and formatter-dirty indefinitely.
const gofumptDrift = "package internal\n\nfunc Drifted() int {\n\n\treturn 1\n}\n"

// formattersFixtureConfig is the formatters: block this repo ships. The
// fixture needs it because `golangci-lint fmt` with no config runs gofmt
// alone, which would pass every case below and prove nothing.
const formattersFixtureConfig = `version: "2"
formatters:
  enable:
    - gofumpt
    - goimports
  settings:
    gofumpt:
      extra-rules: true
    goimports:
      local-prefixes:
        - github.com/sparkwing-dev
`

// withFormattersConfig gives the fixture repo the formatters this repo
// configures.
func withFormattersConfig(t *testing.T, root string) {
	t.Helper()
	writeGoFile(t, filepath.Join(root, ".golangci.yml"), formattersFixtureConfig)
}

// The formatters step reads the staged change, so drift the commit did not
// touch never charges an unrelated author. Twenty files in the product tree
// are drifting today; a gate that reds on all of them is a gate somebody
// passes --no-verify within a day.
func TestFormattersIgnoreAFileTheCommitDoesNotTouch(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()
	requireGolangciLint(t)
	withFormattersConfig(t, root)

	writeGoFile(t, filepath.Join(root, "internal", "drifted.go"), gofumptDrift)
	gitCommitAll(t, root, "drift the commit does not touch")

	writeGoFile(t, filepath.Join(root, "internal", "clean.go"),
		"package internal\n\nfunc Clean() int { return 2 }\n")
	gitAddAll(t, root)

	if err := runFormatters(ctx); err != nil {
		t.Errorf("the formatters step charged the commit for untouched drift: %v", err)
	}
}

// The narrowing must not disarm the check: drift the commit stages is still
// refused, and gofmt passing the same file is what makes the step worth
// running at all.
func TestFormattersRefuseDriftTheStagedChangeIntroduces(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()
	requireGolangciLint(t)
	withFormattersConfig(t, root)
	gitCommitAll(t, root, "clean base")

	writeGoFile(t, filepath.Join(root, "internal", "drifted.go"), gofumptDrift)
	gitAddAll(t, root)

	if err := runGofmt(ctx); err != nil {
		t.Fatalf("gofmt must pass this file, or the step under test is redundant: %v", err)
	}
	err := runFormatters(ctx)
	if err == nil {
		t.Fatal("the formatters step passed staged gofumpt drift")
	}
	if !strings.Contains(err.Error(), "drifted.go") {
		t.Errorf("the failure does not name the file to fix: %v", err)
	}
	if !strings.Contains(err.Error(), "golangci-lint fmt") {
		t.Errorf("the failure does not name the command that fixes it: %v", err)
	}
}

// Third-party Go arriving through npm is not this repo's to format, and a
// dependency update must not be able to red a commit that did not cause it.
func TestStagedGoFilesSkipsNodeModules(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")

	writeGoFile(t, filepath.Join(root, "web", "node_modules", "flatted", "golang", "f.go"), gofumptDrift)
	writeGoFile(t, filepath.Join(root, "internal", "mine.go"),
		"package internal\n\nfunc Mine() int { return 2 }\n")
	gitAddAll(t, root)

	files, err := stagedGoFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(f, "node_modules/") {
			t.Errorf("the formatters step would format vendored npm Go: %s", f)
		}
	}
	if len(files) != 1 || files[0] != "internal/mine.go" {
		t.Errorf("the staged Go files are wrong: %v", files)
	}
}

// requireGolangciLint skips when the linter is not installed, which is the
// one honest thing to do: a step that cannot run must not report a pass.
func requireGolangciLint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint not available")
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
