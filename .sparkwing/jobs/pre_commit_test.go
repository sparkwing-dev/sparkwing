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

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	runTestGit(t, dir, "init")
}

func gitAddAll(t *testing.T, dir string) {
	t.Helper()
	runTestGit(t, dir, "add", "-A")
}

func writeGoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

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

func TestGoStepsStillCoverThePipelineModule(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, ".sparkwing", "negative_control.go"),
		"package pipelines\n\nfunc Broken( {\n")

	if err := runBuild(ctx); err == nil {
		t.Fatal("build accepted an unparseable file in the pipeline module")
	}
}

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
	for _, id := range []string{"frontend-lint", "frontend-build", "frontend-browser"} {
		if w.StepByID(id) == nil {
			t.Fatalf("pre-commit does not run %s", id)
		}
	}
	if deps := w.StepByID("frontend-lint").DepIDs(); len(deps) != 0 {
		t.Fatalf("frontend-lint dependencies = %v, want an independent fast check", deps)
	}
	if !stepWaitsOn(w, "frontend-build", "frontend-unit") {
		t.Fatal("frontend-build does not wait on frontend-unit")
	}
	if !stepWaitsOn(w, "frontend-build", "frontend-lint") {
		t.Fatal("frontend-build does not wait on frontend-lint")
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

func TestFrontendLintPropagatesVerdictWithoutInstallingDependencies(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	packageJSON := filepath.Join(web, "package.json")
	previous := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(previous) })

	writeGoFile(t, packageJSON, `{"scripts":{"lint":"node -e \"process.exit(1)\""}}`)
	err := runFrontendLint(context.Background())
	if err == nil {
		t.Fatal("frontend-lint accepted a failing npm lint script")
	}
	if !strings.Contains(err.Error(), "frontend ESLint suite") {
		t.Fatalf("frontend-lint failure = %q, want named suite", err)
	}

	writeGoFile(t, packageJSON, `{"scripts":{"lint":"node -e \"process.exit(0)\""}}`)
	if err := runFrontendLint(context.Background()); err != nil {
		t.Fatalf("frontend-lint rejected a passing npm lint script: %v", err)
	}
	for _, path := range []string{filepath.Join(web, "package-lock.json"), filepath.Join(web, "node_modules")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("frontend-lint created dependency state at %s: %v", path, err)
		}
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

const (
	emDash    = "\u2014"
	trackerID = "TOD" + "-42"
)

func gitCommitAll(t *testing.T, dir, message string) {
	t.Helper()
	gitAddAll(t, dir)
	runTestGit(t, dir, "-c", "user.name=gate", "-c", "user.email=gate@example.com",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
}

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

func TestRegexSweepsReadTheRangeWhenNothingIsStaged(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	writeGoFile(t, filepath.Join(root, "FEEDBACK.md"), "a dash "+emDash+" and a "+trackerID+" id\n")
	gitCommitAll(t, root, "committed without a gate run")

	if err := checkEmDashes(ctx); err == nil {
		t.Error("the em-dash sweep passed committed drift the hosted gate would reject")
	}
	if err := checkTrackerIDs(ctx); err == nil {
		t.Error("the tracker-id sweep passed committed drift the hosted gate would reject")
	}
}

func TestRegexSweepsIgnoreHistoryBeforeTheBaseline(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "FEEDBACK.md"), "a dash "+emDash+" and a "+trackerID+" id\n")
	gitCommitAll(t, root, "history before the baseline")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	writeGoFile(t, filepath.Join(root, "internal", "clean.go"),
		"package internal\n\nfunc Clean() int { return 2 }\n")
	gitCommitAll(t, root, "clean change")

	if err := checkEmDashes(ctx); err != nil {
		t.Errorf("the em-dash sweep charged the range for untouched history: %v", err)
	}
	if err := checkTrackerIDs(ctx); err != nil {
		t.Errorf("the tracker-id sweep charged the range for untouched history: %v", err)
	}
}

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

const gofumptDrift = "package internal\n\nfunc Drifted() int {\n\n\treturn 1\n}\n"

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

func withFormattersConfig(t *testing.T, root string) {
	t.Helper()
	writeGoFile(t, filepath.Join(root, ".golangci.yml"), formattersFixtureConfig)
}

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

func TestFormattersCheckTheRangeWhenNothingIsStaged(t *testing.T) {
	root := gateFixtureRepo(t)
	ctx := context.Background()
	requireGolangciLint(t)
	withFormattersConfig(t, root)
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")

	writeGoFile(t, filepath.Join(root, "internal", "drifted.go"), gofumptDrift)
	gitCommitAll(t, root, "drift committed without a gate run")

	err := runFormatters(ctx)
	if err == nil {
		t.Fatal("the formatters step passed committed drift that the hosted gate would reject")
	}
	if !strings.Contains(err.Error(), "drifted.go") {
		t.Errorf("the failure does not name the file to fix: %v", err)
	}
	if !strings.Contains(err.Error(), "nothing staged") {
		t.Errorf("the failure does not name the mode the step ran in: %v", err)
	}
}

func TestFormattersRefuseToJudgeWithoutTheBaseline(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")

	err := runFormatters(context.Background())
	if err == nil {
		t.Fatal("the formatters step reported success without resolving a base")
	}
	if !strings.Contains(err.Error(), "origin/main") || !strings.Contains(err.Error(), "git fetch") {
		t.Errorf("the failure does not name the ref to fetch: %v", err)
	}
}

func TestStagedScopeSkipsNodeModules(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "clean base")

	writeGoFile(t, filepath.Join(root, "web", "node_modules", "flatted", "golang", "f.go"), gofumptDrift)
	writeGoFile(t, filepath.Join(root, "internal", "mine.go"),
		"package internal\n\nfunc Mine() int { return 2 }\n")
	gitAddAll(t, root)

	files, scope, err := changeScope(context.Background(), "Go file(s)", existingGoFiles)
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
	if !strings.Contains(scope, "staged") {
		t.Errorf("a staged change resolved to %q", scope)
	}
}

func requireGolangciLint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint not available")
	}
}

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

const (
	homeEnvViolation = "package internal\n\nimport \"os\"\n\n" +
		"func Home() string { return os.Getenv(\"SPARKWING_" + "HOME\") }\n"

	homeJoinViolation = "package internal\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n)\n\n" +
		"func Home() string {\n\thome, _ := os.UserHomeDir()\n\treturn filepath.Join(home, \"" + ".sparkwing\")\n}\n"

	projectDirJoin = "package internal\n\nimport \"path/filepath\"\n\n" +
		"func Dir(cwd string) string { return filepath.Join(cwd, \"" + ".sparkwing\") }\n"
)

var homeRuleFixtures = map[string]string{
	"read SPARKWING_HOME from the environment":       homeEnvViolation,
	"build the sparkwing home from a home directory": homeJoinViolation,
}

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

func TestHomeResolutionAllowsTheProjectPipelineDirectory(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "projectdir.go"), projectDirJoin)
	gitAddAll(t, root)

	if err := checkHomeResolution(context.Background()); err != nil {
		t.Fatalf("the gate refused a checkout-relative pipeline directory: %v", err)
	}
}

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
