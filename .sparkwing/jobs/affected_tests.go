package jobs

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the marker sits on the line above a top-level test, so the critical
// set lives beside the code it guards and a renamed test keeps its tag.
const criticalTestMarker = "//sparkwing:critical"

var criticalTestFunc = regexp.MustCompile(`^func (Test\w*)\(\s*\w+\s+\*testing\.T\s*\)`)

// perf: a change to one of these files reaches tests the import graph cannot
// see, so its module runs every package.
var wholeModuleTriggers = map[string]string{
	"go.mod": "module requirements changed",
	"go.sum": "module checksums changed",
}

// perf: tests read these files through paths the import graph cannot see, so a
// change under a prefix runs the packages beside it. CHANGELOG.md is left out:
// nearly every change edits it, the docs-mirror and changelog steps judge it
// on every run, and its other readers run under --full.
var nonPackageReaders = []struct {
	prefix string
	dirs   []string
}{
	{".github/", []string{".sparkwing/jobs"}},
	{".github/workflows/release.yaml", []string{"cmd/sparkwing"}},
	{"bin/", []string{".sparkwing/jobs"}},
	{"docs/", []string{"internal/doccheck", "cmd/sparkwing"}},
	{"docs/migrations/", []string{"internal/store"}},
	{"docs/billing.md", []string{"internal/store"}},
	{"docs/backup-restore.md", []string{"internal/store"}},
	{"docs/github-actions-runners.md", []string{"internal/controller"}},
	{"docs/observability.md", []string{"internal/controller"}},
	{"docs/api-reference.md", []string{"internal/apiref"}},
	{"api/", []string{"internal/apispec", ".sparkwing/jobs"}},
	{"pkg/wingwire/", []string{".sparkwing/jobs"}},
	{"internal/controllerwire/", []string{".sparkwing/jobs"}},
	{"internal/apispec/", []string{".sparkwing/jobs"}},
	{"install/", []string{"cmd/sparkwing", "internal/releaseauth"}},
	{"examples/", []string{"internal/doccheck"}},
	{"design/rewrite/contracts/", []string{"tests/acceptance"}},
	{"AGENTS.md", []string{"cmd/sparkwing"}},
	{"DELIVERY.md", []string{".sparkwing/jobs", "internal/doccheck"}},
	{"README.md", []string{"internal/doccheck"}},
	{"VERSIONING.md", []string{"internal/doccheck"}},
}

func readerPackages(module string, modules []string, file string, byDir map[string]string) []string {
	var out []string
	for _, r := range nonPackageReaders {
		if file != r.prefix && !(strings.HasSuffix(r.prefix, "/") && strings.HasPrefix(file, r.prefix)) {
			continue
		}
		for _, dir := range r.dirs {
			if owningModule(dir, modules) != module {
				continue
			}
			rel := dir
			if module != "." {
				rel = strings.TrimPrefix(dir, module+"/")
			}
			if pkg, ok := byDir[rel]; ok {
				out = append(out, pkg)
			}
		}
	}
	return out
}

type modulePackage struct {
	importPath string
	dir        string
	imports    []string
}

type affectedTestPlan struct {
	module   string
	reason   string
	whole    bool
	packages []string
	critical map[string][]string
}

func runAffectedTest(ctx context.Context) error {
	files, scope, err := affectedChangeScope(ctx)
	if err != nil {
		return err
	}
	return runAffectedTestFiles(ctx, files, scope)
}

// perf: land passes the files a merge of origin/main changed, so a clean merge
// re-runs only the packages main touched instead of the whole gate.
func runAffectedTestFiles(ctx context.Context, files []string, scope string) error {
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	critical, err := criticalTests(ctx)
	if err != nil {
		return err
	}
	byDir := map[string]string{}
	var plans []affectedTestPlan
	for _, module := range modules {
		packages, err := listModulePackages(ctx, module)
		if err != nil {
			return fmt.Errorf("%s: %w", module, err)
		}
		addRepoPackages(byDir, module, packages)
		plans = append(plans, planAffectedTests(module, modules, files, packages, critical[module]))
	}
	sparkwing.Info(ctx, "affected tests: %s", scope)
	if goChange(files, byDir) == "" {
		sparkwing.Info(ctx, "affected tests: no Go source changed, so the critical set stays for hosted CI")
		for i := range plans {
			plans[i].critical = nil
		}
	}
	return withStableProductTestHome("gate-test", func(home string) error {
		var failures []string
		for _, p := range plans {
			for _, command := range p.commands(currentHost()) {
				script := productTestScript(fmt.Sprintf("cd %q && %s", p.module, command), home)
				if _, err := sparkwing.Bash(ctx, script).Run(); err != nil {
					failures = append(failures, describeModuleFailure(p.module, err))
				}
			}
			sparkwing.Info(ctx, "affected tests: %s", p.describe())
		}
		if len(failures) == 0 {
			return nil
		}
		return fmt.Errorf("go test failed in %d command(s):\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	})
}

func (p affectedTestPlan) commands(h hostShape) []string {
	var out []string
	timeout := "-timeout " + productGoTestTimeout + " "
	short := "-short " + timeout
	if p.whole {
		out = append(out, boundedGoCommand(h, "test", short+"./..."))
	} else if len(p.packages) > 0 {
		out = append(out, boundedGoCommand(h, "test", short+quoteAll(p.packages)))
	}
	if len(p.critical) > 0 {
		pkgs := mapKeys(p.critical)
		var names []string
		for _, pkg := range pkgs {
			names = append(names, p.critical[pkg]...)
		}
		sort.Strings(names)
		names = dedupe(names)
		run := shellQuote("^(" + strings.Join(names, "|") + ")$")
		out = append(out, boundedGoCommand(h, "test", timeout+"-run "+run+" "+quoteAll(pkgs)))
	}
	return out
}

func (p affectedTestPlan) describe() string {
	if p.whole {
		return fmt.Sprintf("%s: every package, because %s", p.module, p.reason)
	}
	count := 0
	for _, tests := range p.critical {
		count += len(tests)
	}
	return fmt.Sprintf("%s: %d affected package(s), %d critical test(s) in %d package(s)",
		p.module, len(p.packages), count, len(p.critical))
}

func planAffectedTests(module string, modules, files []string, packages []modulePackage, critical map[string][]string) affectedTestPlan {
	p := affectedTestPlan{module: module}
	byDir := make(map[string]string, len(packages))
	for _, pkg := range packages {
		byDir[pkg.dir] = pkg.importPath
	}
	changed := map[string]bool{}
	for _, f := range files {
		for _, pkg := range readerPackages(module, modules, f, byDir) {
			changed[pkg] = true
		}
		rel, ok := moduleRelative(module, modules, f)
		if !ok {
			continue
		}
		if why, ok := wholeModuleTriggers[rel]; ok {
			p.whole, p.reason = true, why
			continue
		}
		if pkg := owningPackage(rel, byDir); pkg != "" {
			changed[pkg] = true
		}
	}
	// safety: test imports count too, because a package can exercise another
	// package without importing it in production.
	affected := changed
	for added := true; added; {
		added = false
		for _, pkg := range packages {
			if affected[pkg.importPath] {
				continue
			}
			for _, dep := range pkg.imports {
				if affected[dep] {
					affected[pkg.importPath], added = true, true
					break
				}
			}
		}
	}
	p.packages = mapKeys(affected)
	p.critical = map[string][]string{}
	for dir, tests := range critical {
		pkg, ok := byDir[dir]
		if !ok {
			continue
		}
		p.critical[pkg] = tests
	}
	return p
}

// safety: modules nest, so a file belongs to the module with the longest
// matching directory, the rule owningModule applies for the race step.
func moduleRelative(module string, modules []string, file string) (string, bool) {
	dir := filepath.ToSlash(filepath.Dir(file))
	if owningModule(dir, modules) != module {
		return "", false
	}
	if module == "." {
		return file, true
	}
	rel, ok := strings.CutPrefix(file, module+"/")
	return rel, ok
}

// perf: a file outside every package, such as testdata or an embedded asset
// below a package, charges the nearest package above it.
func owningPackage(rel string, byDir map[string]string) string {
	dir := path.Dir(rel)
	for {
		if pkg, ok := byDir[dir]; ok {
			return pkg
		}
		if dir == "." || dir == "/" || dir == "" {
			return ""
		}
		dir = path.Dir(dir)
	}
}

func listModulePackages(ctx context.Context, module string) ([]modulePackage, error) {
	format := `{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`
	out, err := sparkwing.Bash(ctx, fmt.Sprintf("cd %q && go list -e -f %s ./...", module, shellQuote(format))).String()
	if err != nil {
		return nil, fmt.Errorf("list module packages: %w", err)
	}
	root, err := filepath.Abs(filepath.Join(regexCheckRoot(), module))
	if err != nil {
		return nil, err
	}
	var packages []modulePackage
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 3)
		if len(fields) != 3 || strings.Contains("/"+fields[0]+"/", "/node_modules/") {
			continue
		}
		rel, err := filepath.Rel(root, fields[1])
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		packages = append(packages, modulePackage{importPath: fields[0], dir: filepath.ToSlash(rel), imports: strings.Fields(fields[2])})
	}
	return packages, scanner.Err()
}

// safety: deletions and renames count, because removing a file changes what
// its package compiles; and an untracked file the gate will
// judge counts when nothing is staged.
func affectedChangeScope(ctx context.Context) ([]string, string, error) {
	staged, err := stagedNames(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list the staged change: %w", err)
	}
	if len(staged) > 0 {
		files, err := listNames(ctx, "diff --cached -z --name-only --no-renames --diff-filter=ACMRD")
		if err != nil {
			return nil, "", err
		}
		return files, fmt.Sprintf("%d staged file(s)", len(files)), nil
	}
	base, baseline, err := resolveGateBase(ctx)
	if err != nil {
		return nil, "", err
	}
	changed, err := listNames(ctx, "diff -z --name-only --no-renames --diff-filter=ACMRD "+base)
	if err != nil {
		return nil, "", fmt.Errorf("list the change since %s: %w", base, err)
	}
	untracked, err := listNames(ctx, "ls-files -z --others --exclude-standard")
	if err != nil {
		return nil, "", fmt.Errorf("list untracked files: %w", err)
	}
	files := append(changed, untracked...)
	return files, fmt.Sprintf("nothing staged, so %d file(s) in %s", len(files), baseline), nil
}

// safety: a marker that does not sit above a test fails, so the set cannot drift.
func criticalTests(ctx context.Context) (map[string]map[string][]string, error) {
	files, err := listNames(ctx, `ls-files -z -- '*_test.go'`)
	if err != nil {
		return nil, fmt.Errorf("list the tracked test files: %w", err)
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string][]string{}
	var bad []string
	for _, f := range files {
		if strings.Contains(f, "node_modules/") || isTestdataPath(f) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(regexCheckRoot(), f))
		if err != nil {
			continue
		}
		tests, misplaced := scanCriticalTests(string(data))
		for _, line := range misplaced {
			bad = append(bad, fmt.Sprintf("%s:%d", f, line))
		}
		if len(tests) == 0 {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(f))
		module := owningModule(dir, modules)
		if module == "" {
			continue
		}
		rel := dir
		if module != "." {
			rel = strings.TrimPrefix(strings.TrimPrefix(dir, module), "/")
			if rel == "" {
				rel = "."
			}
		}
		if out[module] == nil {
			out[module] = map[string][]string{}
		}
		out[module][rel] = append(out[module][rel], tests...)
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%s must sit on the line directly above a top-level func TestXxx(t *testing.T):\n  - %s",
			criticalTestMarker, strings.Join(bad, "\n  - "))
	}
	return out, nil
}

func scanCriticalTests(src string) (tests []string, misplaced []int) {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, " \t\r") != criticalTestMarker {
			continue
		}
		if i+1 < len(lines) {
			if m := criticalTestFunc.FindStringSubmatch(lines[i+1]); m != nil {
				tests = append(tests, m[1])
				continue
			}
		}
		misplaced = append(misplaced, i+1)
	}
	return tests, misplaced
}

func quoteAll(pkgs []string) string {
	quoted := make([]string, len(pkgs))
	for i, p := range pkgs {
		quoted[i] = shellQuote(p)
	}
	return strings.Join(quoted, " ")
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

const browserFixturePackage = "./internal/web/browserfixture"

// perf: the dashboard's unit and lint suites judge web/ alone. The production
// build feeds the browser smoke, which also serves the Go fixture, so both run
// when web/ or any package that fixture builds changed.
func skipFrontendUnlessTouched(browser bool) sparkwing.SkipPredicate {
	return func(ctx context.Context) bool {
		why, err := frontendChange(ctx, browser)
		switch {
		case err != nil:
			sparkwing.Info(ctx, "frontend: running, because its inputs could not be judged: %v", err)
			return false
		case why != "":
			sparkwing.Info(ctx, "frontend: running, because %s", why)
			return false
		}
		sparkwing.Info(ctx, "frontend: skipping, because the change touches nothing this suite judges")
		return true
	}
}

func frontendChange(ctx context.Context, browser bool) (string, error) {
	files, _, err := affectedChangeScope(ctx)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if strings.HasPrefix(f, "web/") {
			return f + " changed", nil
		}
	}
	if !browser {
		return "", nil
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return "", err
	}
	packages, err := listModulePackages(ctx, ".")
	if err != nil {
		return "", err
	}
	byDir := make(map[string]string, len(packages))
	for _, pkg := range packages {
		byDir[pkg.dir] = pkg.importPath
	}
	deps, err := sparkwing.Bash(ctx, "go list -deps "+browserFixturePackage).String()
	if err != nil {
		return "", fmt.Errorf("list the browser fixture's packages: %w", err)
	}
	built := map[string]bool{}
	for _, dep := range strings.Fields(deps) {
		built[dep] = true
	}
	return fixtureChange(files, modules, byDir, built), nil
}

// perf: Markdown and tests in a package the fixture builds leave what it serves
// unchanged, and nearly every change carries a changelog entry into pkg/docs.
func fixtureChange(files, modules []string, byDir map[string]string, built map[string]bool) string {
	for _, f := range files {
		rel, ok := moduleRelative(".", modules, f)
		if !ok {
			continue
		}
		if _, whole := wholeModuleTriggers[rel]; whole {
			return f + " changed"
		}
		if strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if pkg := owningPackage(rel, byDir); built[pkg] {
			return fmt.Sprintf("%s changed, which the browser fixture builds", pkg)
		}
	}
	return ""
}

func addRepoPackages(byDir map[string]string, module string, packages []modulePackage) {
	for _, pkg := range packages {
		dir := pkg.dir
		if module != "." {
			dir = path.Join(filepath.ToSlash(module), dir)
		}
		byDir[dir] = pkg.importPath
	}
}

// perf: decision 96 lands docs, process and UI changes on their own tests, so
// the module-wide Go steps answer only to a change that reaches compiled code,
// embedded assets, module requirements or the lint configuration. Markdown
// inside a package still runs that package's tests through the test step.
func goChange(files []string, byDir map[string]string) string {
	for _, f := range files {
		switch {
		case strings.HasSuffix(f, ".md"):
			continue
		case strings.HasSuffix(f, ".go"):
			return f + " changed"
		case path.Base(f) == "go.mod" || path.Base(f) == "go.sum" || path.Base(f) == ".golangci.yml" || f == "internal/store/schema.sql":
			return f + " changed"
		}
		if pkg := owningPackage(f, byDir); pkg != "" {
			return fmt.Sprintf("%s changed, which %s owns", f, pkg)
		}
	}
	return ""
}

func repoGoChange(ctx context.Context) (string, error) {
	files, _, err := affectedChangeScope(ctx)
	if err != nil {
		return "", err
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return "", err
	}
	byDir := map[string]string{}
	for _, module := range modules {
		packages, err := listModulePackages(ctx, module)
		if err != nil {
			return "", fmt.Errorf("%s: %w", module, err)
		}
		addRepoPackages(byDir, module, packages)
	}
	return goChange(files, byDir), nil
}

// safety: an unjudgeable change runs the step, so a broken scope can only widen it.
func skipUnlessGoTouched(step string) sparkwing.SkipPredicate {
	return func(ctx context.Context) bool {
		why, err := repoGoChange(ctx)
		switch {
		case err != nil:
			sparkwing.Info(ctx, "%s: running, because the change could not be judged: %v", step, err)
			return false
		case why != "":
			sparkwing.Info(ctx, "%s: running, because %s", step, why)
			return false
		}
		sparkwing.Info(ctx, "%s: skipping, because the change touches no Go source; hosted CI verifies main", step)
		return true
	}
}

// perf: the TypeScript SDK suite judges sdk/typescript and the Go protocol it
// conforms to, so a change touching neither leaves it to hosted CI.
func skipTypeScriptSDKUnlessTouched(ctx context.Context) bool {
	files, _, err := affectedChangeScope(ctx)
	if err == nil {
		for _, f := range files {
			if strings.HasPrefix(f, "sdk/typescript/") {
				sparkwing.Info(ctx, "sdk-typescript: running, because %s changed", f)
				return false
			}
		}
	}
	return skipUnlessGoTouched("sdk-typescript")(ctx)
}
