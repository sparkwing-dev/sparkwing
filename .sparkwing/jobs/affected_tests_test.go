package jobs

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var affectedFixturePackages = []modulePackage{
	{importPath: "m/leaf", dir: "leaf"},
	{importPath: "m/mid", dir: "mid", imports: []string{"m/leaf"}},
	{importPath: "m/top", dir: "top", imports: []string{"m/mid"}},
	{importPath: "m/billing", dir: "billing"},
}

func TestAffectedTestsCoverTheReverseDependencyClosure(t *testing.T) {
	critical := map[string][]string{"billing": {"TestRefund"}, "leaf": {"TestLeaf"}}
	p := planAffectedTests(".", []string{"."}, []string{"leaf/leaf.go", "leaf/old.go", "docs/readme.md"}, affectedFixturePackages, critical)
	if want := []string{"m/leaf", "m/mid", "m/top"}; !slices.Equal(p.packages, want) {
		t.Fatalf("affected = %v, want the changed package and transitive importers %v", p.packages, want)
	}
	if len(p.critical) != 2 || !slices.Equal(p.critical["m/billing"], []string{"TestRefund"}) || !slices.Equal(p.critical["m/leaf"], []string{"TestLeaf"}) {
		t.Fatalf("critical = %v, want every critical test without short mode", p.critical)
	}
	commands := affectedTestGroups([]affectedTestPlan{p}, 3)
	if len(commands) != 2 || !strings.Contains(commands[0][0].command, "-short ") || strings.Contains(commands[1][0].command, "-short ") || !strings.Contains(commands[1][0].command, "TestRefund") {
		t.Fatalf("commands = %q, want affected fast tests and unshortened critical tests", commands)
	}
}

func TestAffectedTestsChargeTestdataToTheOwningPackage(t *testing.T) {
	p := planAffectedTests(".", []string{"."}, []string{"top/testdata/golden/out.txt"}, affectedFixturePackages, nil)
	if !slices.Equal(p.packages, []string{"m/top"}) {
		t.Fatalf("affected = %v, want the package that owns the testdata", p.packages)
	}
}

func TestAffectedTestsRunTheWholeModuleWhenTheGraphCannotSeeTheChange(t *testing.T) {
	for _, file := range []string{"go.mod", "go.sum"} {
		p := planAffectedTests(".", []string{"."}, []string{file}, affectedFixturePackages, nil)
		if !p.whole {
			t.Errorf("%s: plan = %+v, want every package", file, p)
		}
		if commands := affectedTestGroups([]affectedTestPlan{p}, 3); len(commands) != 1 || !strings.Contains(commands[0][0].command, "m/leaf") || !strings.Contains(commands[0][0].command, "m/top") {
			t.Errorf("%s: commands = %q, want every package in the module", file, commands)
		}
	}
}

func TestAffectedSchemaRunsStoreAndItsReadersWithoutWideningTheModule(t *testing.T) {
	packages := []modulePackage{
		{importPath: "m/store", dir: "internal/store"},
		{importPath: "m/controller", dir: "internal/controller", imports: []string{"m/store"}},
		{importPath: "m/other", dir: "internal/other"},
	}
	p := planAffectedTests(".", []string{"."}, []string{"internal/store/schema.sql"}, packages, nil)
	if p.whole || !slices.Equal(p.packages, []string{"m/controller", "m/store"}) {
		t.Fatalf("schema plan = %+v, want store and its readers", p)
	}
}

func TestAffectedModuleChangesKeepCriticalTestsUnshortened(t *testing.T) {
	p := planAffectedTests(".", []string{"."}, []string{"go.mod"}, affectedFixturePackages, map[string][]string{"leaf": {"TestGuard"}})
	commands := affectedTestGroups([]affectedTestPlan{p}, 1)
	if len(commands) != 2 || !strings.Contains(commands[0][0].command, "-short ") || !strings.Contains(commands[0][0].command, "m/top") || strings.Contains(commands[1][0].command, "-short ") || !strings.Contains(commands[1][0].command, "TestGuard") {
		t.Fatalf("commands = %q, want the whole fast class and full critical tests", commands)
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

func TestAffectedWireChangesRunTheReleaseWireCutCheck(t *testing.T) {
	modules := []string{".", ".sparkwing"}
	packages := []modulePackage{{importPath: "sparkwing-pipelines/jobs", dir: "jobs"}}
	files := []string{"pkg/wingwire/messages.go", "internal/controllerwire/run.go", "internal/apispec/spec.go"}
	for _, surface := range wireSurfaces {
		files = append(files, surface.path)
	}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			plan := planAffectedTests(".sparkwing", modules, []string{file}, packages, nil)
			commands := affectedTestGroups([]affectedTestPlan{plan}, 1)
			if len(commands) != 1 || !strings.HasSuffix(commands[0][0].command, " 'sparkwing-pipelines/jobs'") || strings.Contains(commands[0][0].command, "-run") {
				t.Fatalf("%s selected %q, want the jobs suite including TestRepositoryUnreleasedSectionDeclaresEveryWireCut", file, commands)
			}
		})
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
		"package other\n\nimport \"testing\"\n\n//sparkwing:critical\nfunc TestGuard(t *testing.T) { if testing.Short() { t.Skip(\"critical short guard\") }; t.Fatal(\"critical ran\") }\n")
	writeGoFile(t, filepath.Join(root, "importer", "importer.go"), "package importer\n")
	writeGoFile(t, filepath.Join(root, "importer", "importer_test.go"),
		"package importer_test\n\nimport (\"testing\"; _ \"fixture/changed\")\nfunc TestImporter(t *testing.T) { t.Fatal(\"test importer ran\") }\n")
	writeGoFile(t, filepath.Join(root, "quiet", "quiet.go"), "package quiet\n")
	writeGoFile(t, filepath.Join(root, "quiet", "quiet_test.go"),
		"package quiet\n\nimport \"testing\"\n\nfunc TestUnrelated(t *testing.T) { t.Fatal(\"unaffected package ran\") }\n")
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "changed", "changed.go"), "package changed\n")
	writeGoFile(t, filepath.Join(root, "changed", "changed_test.go"),
		"package changed\n\nimport \"testing\"\n\nfunc TestChanged(t *testing.T) { t.Fatal(\"changed package ran\") }\n")
	gitCommitAll(t, root, "change")
	ctx := newTestContext(t)
	err := runAffectedTest(ctx)
	if err == nil || !strings.Contains(err.Error(), "changed package ran") || !strings.Contains(err.Error(), "critical ran") || !strings.Contains(err.Error(), "test importer ran") || strings.Contains(err.Error(), "unaffected package ran") {
		t.Fatalf("err = %v, want the changed package and the critical test run and the unrelated package left alone", err)
	}
}

func TestThisRepoTagsASmallCriticalSet(t *testing.T) {
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path")
	}
	t.Chdir(root)
	critical, err := criticalTests(newTestContext(t))
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
	ctx := newTestContext(t)
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

func TestBrowserFixtureIgnoresMarkdownAndTestsInPackagesItBuilds(t *testing.T) {
	byDir := map[string]string{"pkg/docs": "m/pkg/docs", "internal/runners/launcher": "m/launcher", "internal/web": "m/web"}
	built := map[string]bool{"m/pkg/docs": true, "m/web": true}
	modules := []string{"."}
	quiet := []string{"CHANGELOG.md", "pkg/docs/changelog.md", "pkg/docs/mirror/schemas/node-protocol.schema.json", "internal/web/server_test.go", "internal/runners/launcher/launcher.go"}
	if why := fixtureChange(quiet, modules, byDir, built); why != "" {
		t.Fatalf("a changelog, a test and an unbuilt package ran the browser smoke: %q", why)
	}
	if why := fixtureChange(append(quiet, "internal/web/server.go"), modules, byDir, built); !strings.Contains(why, "m/web") {
		t.Fatalf("a source change in a package the fixture builds skipped the browser smoke: %q", why)
	}
	if why := fixtureChange([]string{"go.mod"}, modules, byDir, built); why == "" {
		t.Fatal("a module requirement change skipped the browser smoke")
	}
}

// TestAffectedTestsRunTheReadersOfFilesOutsideEveryPackage plants changes the
// import graph cannot see and requires the packages whose tests read them.
func TestAffectedTestsRunTheReadersOfFilesOutsideEveryPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: lists both modules' packages; the fast class runs under -short")
	}
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path")
	}
	t.Chdir(root)
	ctx := newTestContext(t)
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	packages := map[string][]modulePackage{}
	known := map[string]bool{}
	for _, m := range modules {
		pkgs, err := listModulePackages(ctx, m, "")
		if err != nil {
			t.Fatal(err)
		}
		packages[m] = pkgs
		for _, pkg := range pkgs {
			known[path.Join(m, pkg.dir)] = true
		}
	}
	for _, r := range nonPackageReaders {
		for _, dir := range r.dirs {
			if !known[dir] {
				t.Errorf("%s names %s, which is not a package in this repository", r.prefix, dir)
			}
		}
	}
	for file, want := range map[string]string{
		".github/workflows/canonical-gates.yaml": "sparkwing-pipelines/jobs",
		"docs/pipelines.md":                      "github.com/sparkwing-dev/sparkwing/internal/doccheck",
		"bin/check-changelog.sh":                 "sparkwing-pipelines/jobs",
	} {
		var selected []string
		for _, m := range modules {
			selected = append(selected, planAffectedTests(m, modules, []string{file}, packages[m], nil).packages...)
		}
		if !slices.Contains(selected, want) {
			t.Errorf("a change to %s selected %v, want %s among them", file, selected, want)
		}
	}
}

func TestGoStepsAnswerOnlyToChangesThatReachGo(t *testing.T) {
	byDir := map[string]string{"pkg/docs": "m/pkg/docs", "internal/web": "m/web", ".sparkwing/jobs": "jobs"}
	docsOnly := []string{"README.md", "docs/machine-config.md", "pkg/docs/mirror/machine-config.md", "CHANGELOG.md", "web/src/page.tsx", "design/notes.txt"}
	if why := goChange(docsOnly, byDir); why != "" {
		t.Fatalf("a docs and UI change ran the Go steps: %q", why)
	}
	for _, file := range []string{"internal/web/server.go", "internal/web/assets/logo.svg", ".sparkwing/go.sum", ".golangci.yml", "internal/store/schema.sql", "tools/new.go"} {
		if why := goChange(append(docsOnly, file), byDir); why == "" {
			t.Errorf("%s skipped the Go steps", file)
		}
	}
}

func TestDocsOnlyChangeSkipsTheGoStepsAndTheSDKSuite(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	ctx := newTestContext(t)
	writeGoFile(t, filepath.Join(root, "docs", "guide.md"), "# Guide\n")
	gitCommitAll(t, root, "docs")
	if !skipUnlessGoTouched("vet")(ctx) || !skipTypeScriptSDKUnlessTouched(ctx) {
		t.Fatal("a docs-only change ran vet or the TypeScript SDK suite")
	}
	writeGoFile(t, filepath.Join(root, "sdk", "typescript", "src", "index.ts"), "export {}\n")
	gitCommitAll(t, root, "sdk")
	if skipTypeScriptSDKUnlessTouched(ctx) {
		t.Fatal("a TypeScript SDK change skipped its suite")
	}
	writeGoFile(t, filepath.Join(root, "internal", "sound.go"), "package internal\n\nfunc Sound() int { return 2 }\n")
	gitCommitAll(t, root, "go")
	if skipUnlessGoTouched("vet")(ctx) {
		t.Fatal("a Go change skipped vet")
	}
}

func TestAffectedCompilerChecksSkipOtherModulesAndRejectBrokenTouchedCode(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "quiet", "quiet.go"), "package quiet\nfunc Broken( {\n")
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	file := filepath.Join(root, ".sparkwing", "jobs.go")
	writeGoFile(t, file, "package pipelines\nfunc Jobs() int { return 2 }\n")
	gitAddAll(t, root)
	for _, check := range []func(context.Context) error{runAffectedVet, runAffectedBuild} {
		if err := check(newTestContext(t)); err != nil {
			t.Fatalf("unaffected broken module ran: %v", err)
		}
	}
	writeGoFile(t, file, "package pipelines\nfunc Jobs() int { return missing }\n")
	for _, check := range []func(context.Context) error{runAffectedVet, runAffectedBuild} {
		if err := check(newTestContext(t)); err == nil {
			t.Fatal("broken affected package passed")
		}
	}
}

func TestAffectedBuildSkipsPackagesContainingOnlyTests(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "onlytests", "only_test.go"), "package onlytests\nimport \"testing\"\nfunc TestOnly(t *testing.T) {}\n")
	gitAddAll(t, root)
	if err := runAffectedBuild(newTestContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAffectedPackageDeletionKeepsItsImportersChecked(t *testing.T) {
	packages := []modulePackage{{importPath: "m", dir: ".", hasGo: true}, {importPath: "m/reader", dir: "reader", imports: []string{"m/deleted"}, hasGo: true}}
	p := planAffectedTests(".", []string{"."}, []string{"deleted/source.go"}, packages, nil)
	if !p.whole || !slices.Equal(p.packages, []string{"m", "m/reader"}) || !slices.Equal(p.buildPackages, p.packages) {
		t.Fatalf("deleted package plan = %+v, want its remaining module checked", p)
	}
}

func TestAffectedBuildLeavesNoCommandBinary(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "cmd", "tool", "main.go"), "package main\nfunc main() {}\n")
	gitAddAll(t, root)
	if err := runAffectedBuild(newTestContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "tool")); !os.IsNotExist(err) {
		t.Fatalf("build left a command binary: %v", err)
	}
}

func TestAffectedFileReadersDoNotChargeTheirImporters(t *testing.T) {
	packages := []modulePackage{
		{importPath: "m/check", dir: "internal/doccheck"},
		{importPath: "m/consumer", dir: "consumer", imports: []string{"m/check"}},
	}
	p := planAffectedTests(".", []string{"."}, []string{"DELIVERY.md"}, packages, nil)
	if !slices.Equal(p.packages, []string{"m/check"}) {
		t.Fatalf("reader selection=%v, want only the file reader", p.packages)
	}
	p = planAffectedTests(".", []string{"."}, []string{"internal/doccheck/check.go"}, packages, nil)
	if !slices.Equal(p.packages, []string{"m/check", "m/consumer"}) {
		t.Fatalf("code selection=%v, want its importer too", p.packages)
	}
}

func TestAffectedGeneratedDocsStayWithTheDriftCheck(t *testing.T) {
	packages := []modulePackage{
		{importPath: "m/docs", dir: "pkg/docs"},
		{importPath: "m/consumer", dir: "consumer", imports: []string{"m/docs"}},
	}
	p := planAffectedTests(".", []string{"."}, []string{"CHANGELOG.md", "pkg/docs/changelog.md", "pkg/docs/mirror/cli.md", "pkg/docs/mirror/schemas/node-protocol.schema.json"}, packages, nil)
	if len(p.packages) != 0 {
		t.Fatalf("generated docs selected %v; source readers and drift checks cover them", p.packages)
	}
}

func TestAffectedClosureCrossesLocalModuleReplacements(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, ".sparkwing", "go.mod"), "module fixture/pipelines\n\ngo 1.25\nrequire fixture v0.0.0\nreplace fixture => ..\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs.go"), "package pipelines\nimport f \"fixture/internal\"\nfunc Jobs() int { return f.Sound() }\n")
	gitCommitAll(t, root, "modules")
	plans, _, err := planAffectedModules(newTestContext(t), []string{"internal/sound.go"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.module == ".sparkwing" {
			if !slices.Equal(p.packages, []string{"fixture/pipelines"}) || !slices.Equal(p.buildPackages, p.packages) {
				t.Fatalf("cross-module importer omitted: %+v", p)
			}
			return
		}
	}
	t.Fatal("pipeline module omitted")
}

func TestAffectedProtocolSchemasRunTheirReaderWithoutTheCLI(t *testing.T) {
	packages := []modulePackage{
		{importPath: "m/doccheck", dir: "internal/doccheck"},
		{importPath: "m/cli", dir: "cmd/sparkwing"},
	}
	p := planAffectedTests(".", []string{"."}, []string{"docs/schemas/node-protocol.schema.json"}, packages, nil)
	if !slices.Equal(p.packages, []string{"m/doccheck"}) {
		t.Fatalf("schema readers = %v, want doccheck", p.packages)
	}
}
