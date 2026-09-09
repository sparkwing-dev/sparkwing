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

var productDirectories = []string{"cmd", "internal", "pkg", "sparkwing"}

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
	for _, directory := range productDirectories {
		writeGoFile(t, filepath.Join(root, directory, "clean.go"),
			fmt.Sprintf("package %s\n\nfunc Clean() int { return 1 }\n", directory))
	}
	writeGoFile(t, filepath.Join(root, ".sparkwing", "go.mod"), "module fixture-pipelines\n\ngo 1.25\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs.go"),
		"package pipelines\n\nfunc Jobs() int { return 1 }\n")
	gitCommitAll(t, root, "clean base")
	runTestGit(t, root, "update-ref", "refs/remotes/"+gateBaselineRef, "HEAD")

	previous := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(previous) })
	return root
}

func TestLintRefusesAFindingInEachProductDirectory(t *testing.T) {
	for _, directory := range productDirectories {
		t.Run(directory, func(t *testing.T) {
			root := lintFixtureRepo(t)
			ctx := context.Background()

			if err := runGolangciLint(ctx); err != nil {
				t.Fatalf("clean fixture must pass lint: %v", err)
			}

			bad := filepath.Join(root, directory, "negative_control.go")
			writeGoFile(t, bad, ineffassignViolation(directory))
			gitAddAll(t, root)

			err := runGolangciLint(ctx)
			if err == nil {
				t.Fatalf("lint passed a finding in %s/", directory)
			}
			if !strings.Contains(err.Error(), "ineffectual assignment") {
				t.Errorf("lint error in %s lacks the expected ineffectual assignment: %v", directory, err)
			}
		})
	}
}

func TestLintStillCoversThePipelineModule(t *testing.T) {
	root := lintFixtureRepo(t)
	ctx := context.Background()

	writeGoFile(t, filepath.Join(root, ".sparkwing", "negative_control.go"),
		ineffassignViolation("pipelines"))
	gitAddAll(t, root)

	if err := runGolangciLint(ctx); err == nil {
		t.Fatal("lint passed a finding in .sparkwing/")
	}
}

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

func TestLintRefusesToRunWhenTheBaselineRefIsMissing(t *testing.T) {
	root := lintFixtureRepo(t)
	ctx := context.Background()

	runTestGit(t, root, "update-ref", "-d", "refs/remotes/"+gateBaselineRef)

	err := runGolangciLint(ctx)
	if err == nil {
		t.Fatal("lint ran without a baseline it could resolve")
	}
	got := err.Error()
	if !strings.Contains(got, "cannot resolve baseline") {
		t.Errorf("a missing baseline did not report resolution failure: %s", got)
	}
	if !strings.Contains(got, gateBaselineRef) {
		t.Errorf("a missing baseline did not name the ref that went missing: %s", got)
	}
	if !strings.Contains(got, "git fetch") {
		t.Errorf("a missing baseline did not name the fix: %s", got)
	}
	if strings.Contains(got, "ineffectual assignment") {
		t.Errorf("baseline error includes an unrelated lint finding: %s", got)
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

	got, err := resolveLintBaseline(context.Background())
	if err != nil {
		t.Fatalf("the fixture's baseline must resolve: %v", err)
	}
	head := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	if !strings.Contains(got, head[:12]) {
		t.Errorf("the baseline description does not name the commit it resolved to: %s", got)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", directory}, arguments...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return string(output)
}
