package jobs

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"

	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

var productDirs = []string{"cmd", "internal", "pkg", "sparkwing"}

const lintFixtureConfig = `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`

func ineffassignViolation(pkg string) string {
	return fmt.Sprintf("package %s\n\nfunc NegativeControl() int {\n\tx := 1\n\tx = 2\n\treturn x\n}\n", pkg)
}

func lintFixtureRepo(t *testing.T) string {
	t.Helper()
	requireGolangciLint(t)
	root := t.TempDir()
	gitInit(t, root)
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, ".golangci.yml"), lintFixtureConfig)
	for _, dir := range productDirs {
		writeGoFile(t, filepath.Join(root, dir, "clean.go"),
			fmt.Sprintf("package %s\n\nfunc Clean() int { return 1 }\n", dir))
	}
	writeGoFile(t, filepath.Join(root, ".sparkwing", "go.mod"), "module fixture-pipelines\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs.go"),
		"package pipelines\n\nfunc Jobs() int { return 1 }\n")
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/"+gateBaselineRef, "HEAD")

	t.Chdir(root)
	return root
}

func TestLintRefusesAFindingInEachProductDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 6.6s of real work; the fast class runs under -short")
	}
	for _, dir := range productDirs {
		t.Run(dir, func(t *testing.T) {
			root := lintFixtureRepo(t)
			ctx := newTestContext(t)

			if err := runGolangciLint(ctx); err != nil {
				t.Fatalf("clean fixture must pass lint, or the red below proves nothing: %v", err)
			}

			bad := filepath.Join(root, dir, "negative_control.go")
			writeGoFile(t, bad, ineffassignViolation(dir))
			gitAddAll(t, root)

			err := runGolangciLint(ctx)
			if err == nil {
				t.Fatalf("lint passed a finding in %s/, so the step never opened it", dir)
			}
			if !strings.Contains(err.Error(), "ineffectual assignment") {
				t.Errorf("lint failed in %s/ for some reason other than the planted finding: %v", dir, err)
			}
		})
	}
}

func TestLintStillCoversThePipelineModule(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.8s of real work; the fast class runs under -short")
	}
	root := lintFixtureRepo(t)
	ctx := newTestContext(t)

	writeGoFile(t, filepath.Join(root, ".sparkwing", "negative_control.go"),
		ineffassignViolation("pipelines"))
	gitAddAll(t, root)

	if err := runGolangciLint(ctx); err == nil {
		t.Fatal("lint passed a finding in .sparkwing/, the one module it used to cover")
	}
}

func TestLintCoversEveryCommittedModule(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.3s of real work; the fast class runs under -short")
	}
	root := lintFixtureRepo(t)
	ctx := newTestContext(t)

	writeGoFile(t, filepath.Join(root, "tools", "go.mod"), "module fixture/tools\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, "tools", "bad.go"), ineffassignViolation("tools"))
	gitAddAll(t, root)

	if err := runGolangciLint(ctx); err == nil {
		t.Fatal("lint skipped a committed module outside the root and .sparkwing/")
	}
}

func TestLintJudgesAllFilesWhenTheBaselineRefIsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: real lint integration runs in full scans")
	}
	root := lintFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, ".golangci.yml"), lintBaselineFixtureConfig)
	writeGoFile(t, filepath.Join(root, "internal", "bad.go"), ineffassignViolation("internal"))
	gitCommitAll(t, root, "uploaded snapshot with finding")
	runTestGit(t, root, "update-ref", "-d", "refs/remotes/"+gateBaselineRef)
	err := runGolangciLint(newTestContext(t))
	if err == nil || !strings.Contains(err.Error(), "ineffectual assignment") {
		t.Fatalf("lint did not judge snapshot finding: %v", err)
	}
}

func TestLintScopeLineNamesEveryModuleAndTheBaseline(t *testing.T) {
	got := describeLintScope([]string{".", ".sparkwing"}, "baseline origin/main at abc123def456")

	for _, want := range []string{"2 committed module(s)", ".sparkwing", "baseline origin/main", "abc123def456"} {
		if !strings.Contains(got, want) {
			t.Errorf("the scope line does not carry %q: %s", want, got)
		}
	}
}

func TestLintBaselineDescriptionCarriesTheResolvedCommit(t *testing.T) {
	root := lintFixtureRepo(t)

	_, got, err := resolveGateBase(newTestContext(t))
	if err != nil {
		t.Fatalf("the fixture's baseline must resolve: %v", err)
	}
	head := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	if !strings.Contains(got, head[:12]) {
		t.Errorf("the baseline description does not name the commit it resolved to: %s", got)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := testgit.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// TestTouchedLintJudgesOnlyThePackagesTheChangeHolds plants a finding in one
// changed package and requires the run to name only that package's module.
func TestTouchedLintJudgesOnlyThePackagesTheChangeHolds(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: real golangci-lint runs; the fast class runs under -short")
	}
	root := lintFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "negative_control.go"), ineffassignViolation("internal"))
	writeGoFile(t, filepath.Join(root, "testdata", "tool", "go.mod"), "module fixture/tool\n\ngo 1.25\n")
	gitAddAll(t, root)
	host := newTestHost(t, sparkwingtest.Options{})
	err := runGolangciLintTouched(host.Context(t.Context()))
	if err == nil || !strings.Contains(err.Error(), "ineffectual assignment") {
		t.Fatalf("touched lint missed the finding in the changed package: %v", err)
	}
	var log strings.Builder
	for _, record := range host.Records() {
		log.WriteString(record.Msg + "\n")
	}
	if !strings.Contains(log.String(), ".sparkwing holds no changed Go package") {
		t.Fatalf("touched lint judged a module the change does not reach:\n%s", log.String())
	}

	writeGoFile(t, filepath.Join(root, ".golangci.yml"), lintFixtureConfig+"# changed\n")
	gitAddAll(t, root)
	host = newTestHost(t, sparkwingtest.Options{})
	if err := runGolangciLintTouched(host.Context(t.Context())); err == nil {
		t.Fatal("a whole-module lint passed the planted finding")
	}
	log.Reset()
	for _, record := range host.Records() {
		log.WriteString(record.Msg + "\n")
	}
	if !strings.Contains(log.String(), "every package is linted") || strings.Contains(log.String(), "holds no changed Go package") {
		t.Fatalf("a lint configuration change did not lint every package:\n%s", log.String())
	}
}
