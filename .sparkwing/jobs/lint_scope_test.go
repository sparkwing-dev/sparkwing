package jobs

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// productDirs are the four directories the lint step never opened while it
// ran inside .sparkwing/ alone. One case each, because the defect was
// precisely that some of the tree was out of scope and a single case cannot
// tell "covered" from "covered somewhere else".
var productDirs = []string{"cmd", "internal", "pkg", "sparkwing"}

// lintFixtureConfig enables one cheap linter and no baseline. The baseline
// is deliberately absent: this fixture answers which code the step opens,
// and a baseline would let a missed directory and a forgiven finding
// produce the same silence.
const lintFixtureConfig = `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`

// ineffassignViolation is a finding ineffassign reports and the compiler
// accepts, so a directory that reds here reds for the lint step's reason
// rather than the build step's.
func ineffassignViolation(pkg string) string {
	return fmt.Sprintf("package %s\n\nfunc NegativeControl() int {\n\tx := 1\n\tx = 2\n\treturn x\n}\n", pkg)
}

// lintFixtureRepo builds a repo shaped like this one -- a product module at
// the root carrying cmd/, internal/, pkg/ and sparkwing/, and the pipeline
// module in .sparkwing/ -- and points the gate at it.
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
	runTestGit(t, root, "update-ref", "refs/remotes/"+lintBaselineRef, "HEAD")

	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
	return root
}

// The whole ticket in one test: a finding in each product directory must
// red the step. Narrowing the step back to .sparkwing/ passes every case
// below, which is what it did on every commit from 2026-05-20.
func TestLintRefusesAFindingInEachProductDirectory(t *testing.T) {
	for _, dir := range productDirs {
		t.Run(dir, func(t *testing.T) {
			root := lintFixtureRepo(t)
			ctx := context.Background()

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

// The pipeline module stays covered by the widening: the step that used to
// read only .sparkwing/ must not now read only the root.
func TestLintStillCoversThePipelineModule(t *testing.T) {
	root := lintFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, ".sparkwing", "negative_control.go"),
		ineffassignViolation("pipelines"))
	gitAddAll(t, root)

	if err := runGolangciLint(ctx); err == nil {
		t.Fatal("lint passed a finding in .sparkwing/, the one module it used to cover")
	}
}

// A module added later is covered the day its go.mod lands, because the
// step walks the committed modules rather than naming them.
func TestLintCoversEveryCommittedModule(t *testing.T) {
	root := lintFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, "tools", "go.mod"), "module fixture/tools\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, "tools", "bad.go"), ineffassignViolation("tools"))
	gitAddAll(t, root)

	if err := runGolangciLint(ctx); err == nil {
		t.Fatal("lint skipped a committed module outside the root and .sparkwing/")
	}
}

// golangci-lint handed a baseline ref it cannot resolve drops the baseline
// and reports the repo's whole standing debt against the change in front of
// it. The step has to refuse instead, and say which ref went missing, or a
// machine that has not fetched main turns every commit red for reasons the
// author did not write and cannot read.
func TestLintRefusesToRunWhenTheBaselineRefIsMissing(t *testing.T) {
	root := lintFixtureRepo(t)
	ctx := context.Background()

	runTestGit(t, root, "update-ref", "-d", "refs/remotes/"+lintBaselineRef)

	err := runGolangciLint(ctx)
	if err == nil {
		t.Fatal("lint ran without a baseline it could resolve")
	}
	got := err.Error()
	if !strings.Contains(got, "could not run") {
		t.Errorf("a missing baseline did not report could-not-run: %s", got)
	}
	if !strings.Contains(got, lintBaselineRef) {
		t.Errorf("a missing baseline did not name the ref that went missing: %s", got)
	}
	if !strings.Contains(got, "git fetch") {
		t.Errorf("a missing baseline did not name the fix: %s", got)
	}
	if strings.Contains(got, "ineffectual assignment") {
		t.Errorf("the step linted anyway and charged the author for the result: %s", got)
	}
}

// The scope line is the negative control the exit code cannot be. A step
// that silently narrows itself is how this survived two months, so the
// modules and the baseline have to be readable in the output rather than
// inferred from a zero exit.
func TestLintScopeLineNamesEveryModuleAndTheBaseline(t *testing.T) {
	got := describeLintScope([]string{".", ".sparkwing"}, "baseline origin/main at abc123def456")

	for _, want := range []string{"2 committed module(s)", ".sparkwing", "baseline origin/main", "abc123def456"} {
		if !strings.Contains(got, want) {
			t.Errorf("the scope line does not carry %q: %s", want, got)
		}
	}
}

// The baseline description has to name the commit, not merely the ref. A
// ref reads the same whether it is current or a month stale, and the
// question a reader asks of a grandfathering gate is what it forgave.
func TestLintBaselineDescriptionCarriesTheResolvedCommit(t *testing.T) {
	root := lintFixtureRepo(t)

	got, err := resolveLintBaseline(context.Background())
	if err != nil {
		t.Fatalf("the fixture's baseline must resolve: %v", err)
	}
	head := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	if !strings.Contains(got, head[:12]) {
		t.Errorf("the baseline description does not name the commit it resolved to: %s", got)
	}
}

// gitOutput runs git in dir and returns its stdout.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
