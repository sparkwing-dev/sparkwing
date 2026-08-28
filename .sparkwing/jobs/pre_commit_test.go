package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestPreCommitReservesAndBoundsItsCPU(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (&PreCommit{}).Plan(context.Background(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-commit"}); err != nil {
		t.Fatal(err)
	}

	hints := plan.ResourceHints()
	wantCores := float64(preCommitCPUReservation(runtime.NumCPU()))
	if hints == nil || hints.Cores != wantCores {
		t.Fatalf("reserved cores = %#v, want %.0f", hints, wantCores)
	}
	if got := boundedGoCommand(14, "test", "./..."); got != "GOMAXPROCS=6 go test -p 6 ./..." {
		t.Fatalf("bounded command = %q", got)
	}
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

func TestTheTestStepRemovesItsSuitesTemporaryRoot(t *testing.T) {
	root := gateFixtureRepo(t)
	marker := filepath.Join(t.TempDir(), "tmpdir")
	t.Setenv("SPARKWING_TEST_TMPDIR_MARKER", marker)
	writeGoFile(t, filepath.Join(root, "internal", "tmpdir_probe_test.go"), `package internal

import (
	"os"
	"testing"
)

func TestRecordTMPDIR(t *testing.T) {
	if err := os.WriteFile(os.Getenv("SPARKWING_TEST_TMPDIR_MARKER"), []byte(os.Getenv("TMPDIR")), 0o600); err != nil {
		t.Fatal(err)
	}
}
`)
	gitAddAll(t, root)

	if err := runTest(context.Background()); err != nil {
		t.Fatalf("test step: %v", err)
	}
	assertTestScratchRemoved(t, marker)
}

func TestTheTestStepRemovesItsSuitesTemporaryRootAfterFailure(t *testing.T) {
	root := gateFixtureRepo(t)
	marker := filepath.Join(t.TempDir(), "tmpdir")
	t.Setenv("SPARKWING_TEST_TMPDIR_MARKER", marker)
	writeGoFile(t, filepath.Join(root, "internal", "tmpdir_probe_test.go"), `package internal

import (
	"os"
	"testing"
)

func TestRecordTMPDIRThenFail(t *testing.T) {
	if err := os.WriteFile(os.Getenv("SPARKWING_TEST_TMPDIR_MARKER"), []byte(os.Getenv("TMPDIR")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Fatal("deliberate")
}
`)
	gitAddAll(t, root)

	if err := runTest(context.Background()); err == nil {
		t.Fatal("test step accepted a failing suite")
	}
	assertTestScratchRemoved(t, marker)
}

func assertTestScratchRemoved(t *testing.T, marker string) {
	t.Helper()
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	testRoot := string(raw)
	if !strings.HasPrefix(filepath.Base(testRoot), "sparkwing-go-test-") {
		t.Fatalf("suite TMPDIR = %q, want gate-owned root", testRoot)
	}
	if _, err := os.Stat(testRoot); !os.IsNotExist(err) {
		t.Fatalf("suite temporary root survived test step: %v", err)
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

func TestPreCommitRunsFrontendUnitSuiteAsAnIndependentStep(t *testing.T) {
	w := sparkwing.NewWork()
	if _, err := (&PreCommit{}).Work(w); err != nil {
		t.Fatal(err)
	}
	step := w.StepByID("frontend-unit")
	if step == nil {
		t.Fatal("pre-commit does not run frontend-unit")
	}
	if deps := step.DepIDs(); len(deps) != 0 {
		t.Fatalf("frontend-unit dependencies = %v, want an independent fast check", deps)
	}
}

func TestPreCommitRunsFrontendChecksBeforeBrowserSmoke(t *testing.T) {
	w := sparkwing.NewWork()
	if _, err := (&PreCommit{}).Work(w); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"frontend-browser-lint", "frontend-build", "frontend-browser"} {
		if w.StepByID(id) == nil {
			t.Fatalf("pre-commit does not run %s", id)
		}
	}
	if !stepWaitsOn(w, "frontend-build", "frontend-unit") {
		t.Fatal("frontend-build does not wait on frontend-unit")
	}
	if !stepWaitsOn(w, "frontend-build", "frontend-browser-lint") {
		t.Fatal("frontend-build does not wait on frontend-browser-lint")
	}
	if !stepWaitsOn(w, "frontend-browser", "frontend-build") {
		t.Fatal("frontend-browser does not wait on frontend-build")
	}
}

func TestFrontendUnitSuitePropagatesTheNPMVerdict(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	packageJSON := filepath.Join(web, "package.json")
	previous := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(previous) })

	writeGoFile(t, packageJSON, `{"scripts":{"test":"node -e \"process.exit(1)\""}}`)
	err := runFrontendUnit(context.Background())
	if err == nil {
		t.Fatal("frontend-unit accepted a failing npm test script")
	}
	if !strings.Contains(err.Error(), "frontend unit suite") {
		t.Fatalf("frontend-unit failure = %q, want named suite", err)
	}

	writeGoFile(t, packageJSON, `{"scripts":{"test":"node -e \"process.exit(0)\""}}`)
	if err := runFrontendUnit(context.Background()); err != nil {
		t.Fatalf("frontend-unit rejected a passing npm test script: %v", err)
	}
}

func TestFrontendUnitRunnerRejectsZeroDiscovery(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	packageJSON := readHostedCIFile(t, "web/package.json")
	requireWorkflowText(t, packageJSON, `"test": "node scripts/run-tests.mjs"`)

	runner := readHostedCIFile(t, "web/scripts/run-tests.mjs")
	requireWorkflowText(t, runner,
		`/\.test\.tsx?$/`,
		`tests.length === 0`,
		`["--import", "tsx", "--test", "--test-reporter=tap", ...tests]`,
		`/^1\.\.0\r?$/m`,
	)

	fixture := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fixture, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	runnerPath := filepath.Join(fixture, "scripts", "run-tests.mjs")
	writeGoFile(t, runnerPath, runner)
	cmd := exec.Command("node", runnerPath)
	cmd.Dir = filepath.Join(root, "web")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("frontend unit runner accepted an empty source tree")
	}
	if !strings.Contains(string(output), "no .test.ts or .test.tsx files found") {
		t.Fatalf("empty-suite failure = %q, want zero-discovery diagnostic", output)
	}
}

func TestFrontendChecksPropagateNamedNPMVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		run     func(context.Context) error
		failure string
	}{
		{name: "browser lint", script: "lint:browser", run: runFrontendBrowserLint, failure: "frontend browser-test lint"},
		{name: "build", script: "build", run: runFrontendBuild, failure: "frontend production build"},
		{name: "browser", script: "test:browser:gate", run: runFrontendBrowser, failure: "frontend browser smoke suite"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			web := filepath.Join(root, "web")
			if err := os.MkdirAll(web, 0o755); err != nil {
				t.Fatal(err)
			}
			previous := sparkwing.WorkDir()
			sparkwing.SetWorkDir(root)
			t.Cleanup(func() { sparkwing.SetWorkDir(previous) })

			writeGoFile(t, filepath.Join(web, "package.json"), fmt.Sprintf(`{"scripts":{"%s":"node -e \"process.exit(1)\""}}`, tc.script))
			err := tc.run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("%s failure = %v, want named verdict", tc.name, err)
			}
		})
	}
}

func TestFrontendBrowserMarksOnlyFailedRunsForHostedArtifacts(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(previous) })
	marker := filepath.Join(web, "test-results", ".sparkwing-browser-failed")

	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, marker, "stale\n")
	writeGoFile(t, filepath.Join(web, "package.json"), `{"scripts":{"test:browser:gate":"node -e \"process.exit(0)\""}}`)
	if err := runFrontendBrowser(context.Background()); err != nil {
		t.Fatalf("frontend-browser rejected a passing npm script: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passing browser run left failure marker: %v", err)
	}

	writeGoFile(t, filepath.Join(web, "package.json"), `{"scripts":{"test:browser:gate":"node -e \"process.exit(1)\""}}`)
	if err := runFrontendBrowser(context.Background()); err == nil {
		t.Fatal("frontend-browser accepted a failing npm script")
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "failed\n" {
		t.Fatalf("browser failure marker = %q, %v", body, err)
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

// The fixtures below spell the forbidden patterns with a join rather
// than literally, so this file does not itself carry what the gate
// refuses. Written out they are the real thing, which is what makes them
// fixtures rather than approximations.
const (
	// homeEnvViolation reads the variable directly.
	homeEnvViolation = "package internal\n\nimport \"os\"\n\n" +
		"func Home() string { return os.Getenv(\"SPARKWING_" + "HOME\") }\n"

	// homeJoinViolation skips the variable entirely and builds the path
	// from the user's home directory, which is the half of the original
	// bug an environment-only rule sails past.
	homeJoinViolation = "package internal\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n)\n\n" +
		"func Home() string {\n\thome, _ := os.UserHomeDir()\n\treturn filepath.Join(home, \"" + ".sparkwing\")\n}\n"

	// projectDirJoin builds the per-repo pipeline module directory from a
	// checkout path. Same literal, different thing, and there are roughly
	// two dozen of these in the tree.
	projectDirJoin = "package internal\n\nimport \"path/filepath\"\n\n" +
		"func Dir(cwd string) string { return filepath.Join(cwd, \"" + ".sparkwing\") }\n"
)

// homeRuleFixtures pairs each rule with source that violates it, so the
// cases below stay in step with the rule table rather than restating it.
var homeRuleFixtures = map[string]string{
	"read SPARKWING_HOME from the environment":       homeEnvViolation,
	"build the sparkwing home from a home directory": homeJoinViolation,
}

// The gate has to refuse a fresh bypass of either shape, or it is
// decoration. These are the two shapes internal/bincache carried: it
// read the variable, and when that was unset it built the path from the
// user's home directory.
func TestHomeResolutionRefusesEveryBypassShape(t *testing.T) {
	for _, rule := range homeRules {
		body, ok := homeRuleFixtures[rule.label]
		if !ok {
			t.Fatalf("rule %q has no fixture; add one so the rule cannot ship untested", rule.label)
		}
		t.Run(rule.label, func(t *testing.T) {
			root := gateFixtureRepo(t)
			ctx := context.Background()

			if err := checkHomeResolution(ctx); err != nil {
				t.Fatalf("the clean fixture must pass, or the failure below proves nothing: %v", err)
			}

			writeGoFile(t, filepath.Join(root, "internal", "bypass.go"), body)
			gitAddAll(t, root)

			err := checkHomeResolution(ctx)
			if err == nil {
				t.Fatal("the gate passed a bypass it is supposed to refuse")
			}
			if !strings.Contains(err.Error(), "internal/bypass.go") {
				t.Errorf("the failure does not name the offending file: %v", err)
			}
			if !strings.Contains(err.Error(), "paths.DefaultPaths") {
				t.Errorf("the failure does not name the call that fixes it: %v", err)
			}
			for allowed := range rule.allowed {
				if !strings.Contains(err.Error(), allowed) {
					t.Errorf("the failure does not list the allowlisted file %s: %v", allowed, err)
				}
			}
		})
	}
}

// The allowlist has to actually exempt its entries, and it has to key on
// the path rather than on anything about the content, so an entry keeps
// working when the file around it is edited.
func TestHomeResolutionExemptsTheAllowlist(t *testing.T) {
	for _, rule := range homeRules {
		t.Run(rule.label, func(t *testing.T) {
			root := gateFixtureRepo(t)
			for allowed := range rule.allowed {
				writeGoFile(t, filepath.Join(root, allowed), homeRuleFixtures[rule.label])
			}
			gitAddAll(t, root)

			if err := checkHomeResolution(context.Background()); err != nil {
				t.Fatalf("the gate refused an allowlisted file: %v", err)
			}
		})
	}
}

// The join rule keys on the argument, not the literal, because the same
// literal names the per-repo pipeline module directory in roughly two
// dozen correct call sites. Firing on those would make the gate noise
// somebody silences rather than a rule anybody keeps.
func TestHomeResolutionAllowsTheProjectPipelineDirectory(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "projectdir.go"), projectDirJoin)
	gitAddAll(t, root)

	if err := checkHomeResolution(context.Background()); err != nil {
		t.Fatalf("the gate refused a checkout-relative pipeline directory: %v", err)
	}
}

// Prose has to be able to quote the call it forbids. A rule nobody can
// write about is one nobody can document, and the doc comments on the
// fixed packages quote exactly these calls.
func TestHomeResolutionIgnoresCommentsQuotingTheForbiddenCalls(t *testing.T) {
	root := gateFixtureRepo(t)
	quoted := "package internal\n\n"
	for _, rule := range homeRules {
		for _, line := range strings.Split(homeRuleFixtures[rule.label], "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			quoted += "// " + line + "\n"
		}
	}
	quoted += "func Documented() int { return 1 }\n"
	writeGoFile(t, filepath.Join(root, "internal", "documented.go"), quoted)
	gitAddAll(t, root)

	if err := checkHomeResolution(context.Background()); err != nil {
		t.Fatalf("the gate refused a comment that only quotes the forbidden calls: %v", err)
	}
}

// Three exemptions the gate has to hold, each for its own reason: a test
// reading the variable is asserting something about it, .sparkwing/ is a
// separate module that cannot import internal/paths at all, and setting
// the variable is the isolation this rule exists to make reliable.
func TestHomeResolutionExemptions(t *testing.T) {
	setter := "package internal\n\nimport \"testing\"\n\n" +
		"func Isolate(t *testing.T) { t.Setenv(\"SPARKWING_" + "HOME\", t.TempDir()) }\n"

	cases := []struct {
		name string
		path string
		body string
	}{
		{"a test reading the variable", filepath.Join("internal", "probe_test.go"), homeEnvViolation},
		{"a test joining the home directory", filepath.Join("internal", "join_test.go"), homeJoinViolation},
		{"the pipeline module", filepath.Join(".sparkwing", "job.go"), homeEnvViolation},
		{"setting the variable in product code", filepath.Join("internal", "isolate.go"), setter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			writeGoFile(t, filepath.Join(root, tc.path), tc.body)
			gitAddAll(t, root)

			if err := checkHomeResolution(context.Background()); err != nil {
				t.Errorf("the gate refused %s: %v", tc.name, err)
			}
		})
	}
}
