package jobs

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var affectedFixturePackages = []modulePackage{
	{importPath: "m/leaf", dir: "leaf"},
	{importPath: "m/mid", dir: "mid"},
	{importPath: "m/top", dir: "top"},
	{importPath: "m/billing", dir: "billing"},
}

func TestAffectedTestsCoverOnlyTheChangedPackages(t *testing.T) {
	critical := map[string][]string{"billing": {"TestRefund"}, "leaf": {"TestLeaf"}}
	p := planAffectedTests(".", []string{"."}, []string{"leaf/leaf.go", "leaf/old.go", "docs/readme.md"}, affectedFixturePackages, critical)
	if want := []string{"m/leaf"}; !slices.Equal(p.packages, want) {
		t.Fatalf("affected = %v, want only the changed package %v; importers run after deploy", p.packages, want)
	}
	if want := map[string][]string{"m/billing": {"TestRefund"}}; len(p.critical) != 1 || !slices.Equal(p.critical["m/billing"], want["m/billing"]) {
		t.Fatalf("critical = %v, want only the critical tests outside the affected set", p.critical)
	}
	commands := p.commands(hostShape{cpus: 8})
	if len(commands) != 2 || !strings.Contains(commands[1], `-run '^(TestRefund)$' 'm/billing'`) {
		t.Fatalf("commands = %q, want an affected run and a critical run", commands)
	}
}

func TestAffectedTestsChargeTestdataToTheOwningPackage(t *testing.T) {
	p := planAffectedTests(".", []string{"."}, []string{"top/testdata/golden/out.txt"}, affectedFixturePackages, nil)
	if !slices.Equal(p.packages, []string{"m/top"}) {
		t.Fatalf("affected = %v, want the package that owns the testdata", p.packages)
	}
}

func TestAffectedTestsRunTheWholeModuleWhenTheGraphCannotSeeTheChange(t *testing.T) {
	for _, file := range []string{"go.mod", "go.sum", "internal/store/schema.sql"} {
		p := planAffectedTests(".", []string{"."}, []string{file}, affectedFixturePackages, nil)
		if !p.whole {
			t.Errorf("%s: plan = %+v, want every package", file, p)
		}
		if commands := p.commands(hostShape{cpus: 8}); len(commands) != 1 || !strings.HasSuffix(commands[0], " ./...") {
			t.Errorf("%s: commands = %q, want one ./... run", file, commands)
		}
	}
}

func TestAffectedTestsLeaveANestedModulesFilesToThatModule(t *testing.T) {
	modules := []string{".", ".sparkwing"}
	root := planAffectedTests(".", modules, []string{".sparkwing/go.mod", ".sparkwing/jobs/gate.go"}, affectedFixturePackages, nil)
	if root.whole || len(root.packages) != 0 {
		t.Fatalf("root plan = %+v, want nothing for a change inside .sparkwing", root)
	}
	nested := planAffectedTests(".sparkwing", modules, []string{".sparkwing/go.mod"}, nil, nil)
	if !nested.whole {
		t.Fatalf("nested plan = %+v, want its own go.mod to run it whole", nested)
	}
}

func TestCriticalMarkersMustSitAboveATest(t *testing.T) {
	tests, misplaced := scanCriticalTests("package x\n\n//sparkwing:critical\nfunc TestGuard(t *testing.T) {}\n\n//sparkwing:critical\nfunc helper() {}\n")
	if !slices.Equal(tests, []string{"TestGuard"}) || !slices.Equal(misplaced, []int{6}) {
		t.Fatalf("tests = %v, misplaced = %v; want TestGuard found and line 6 refused", tests, misplaced)
	}
}

func TestAffectedTestStepRunsTheChangeAndTheCriticalSetOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: real go test runs; the fast class runs under -short")
	}
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "other", "other.go"), "package other\n")
	writeGoFile(t, filepath.Join(root, "other", "other_test.go"),
		"package other\n\nimport \"testing\"\n\n//sparkwing:critical\nfunc TestGuard(t *testing.T) { t.Fatal(\"critical ran\") }\n")
	writeGoFile(t, filepath.Join(root, "quiet", "quiet.go"), "package quiet\n")
	writeGoFile(t, filepath.Join(root, "quiet", "quiet_test.go"),
		"package quiet\n\nimport \"testing\"\n\nfunc TestUnrelated(t *testing.T) { t.Fatal(\"unaffected package ran\") }\n")
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "changed", "changed.go"), "package changed\n")
	writeGoFile(t, filepath.Join(root, "changed", "changed_test.go"),
		"package changed\n\nimport \"testing\"\n\nfunc TestChanged(t *testing.T) { t.Fatal(\"changed package ran\") }\n")
	gitCommitAll(t, root, "change")
	ctx := context.Background()
	err := runAffectedTest(ctx)
	if err == nil || !strings.Contains(err.Error(), "changed package ran") || !strings.Contains(err.Error(), "critical ran") || strings.Contains(err.Error(), "unaffected package ran") {
		t.Fatalf("err = %v, want the changed package and the critical test run and the unrelated package left alone", err)
	}
}

func TestThisRepoTagsASmallCriticalSet(t *testing.T) {
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path")
	}
	t.Chdir(root)
	critical, err := criticalTests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tests := range critical["."] {
		count += len(tests)
	}
	for _, dir := range []string{"internal/controller", "internal/capability", "internal/store", "internal/runspec", "internal/secrets"} {
		if len(critical["."][dir]) == 0 {
			t.Errorf("%s holds no critical test; the set covers isolation, billing, trust, masking and the store baseline", dir)
		}
	}
	// perf: the critical set runs on every gate, so it stays a few dozen tests.
	if count == 0 || count > 40 {
		t.Errorf("critical set holds %d tests, want between 1 and 40", count)
	}
}

func TestFrontendSuitesRunOnlyWhenTheirInputsChange(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	ctx := context.Background()
	writeGoFile(t, filepath.Join(root, "internal", "sound.go"), "package internal\n\nfunc Sound() int { return 2 }\n")
	gitCommitAll(t, root, "go only")
	if why, err := frontendChange(ctx, false); err != nil || why != "" {
		t.Fatalf("a Go-only change ran the dashboard suites: %q, %v", why, err)
	}
	if !skipFrontendUnlessTouched(false)(ctx) {
		t.Fatal("the dashboard unit suite ran for a Go-only change")
	}
	if skipFrontendUnlessTouched(true)(ctx) {
		t.Fatal("the browser smoke skipped although its fixture could not be judged")
	}
	writeGoFile(t, filepath.Join(root, "web", "src", "page.tsx"), "export {}\n")
	gitCommitAll(t, root, "web")
	if why, err := frontendChange(ctx, false); err != nil || !strings.Contains(why, "web/src/page.tsx") {
		t.Fatalf("a web change did not run the dashboard suites: %q, %v", why, err)
	}
}
