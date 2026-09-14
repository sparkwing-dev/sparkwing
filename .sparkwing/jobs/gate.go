package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Gate struct{ sparkwing.Base }

func (Gate) ShortHelp() string {
	return "Broad local verification at the push boundary: Go gates, frontend checks, source-policy sweeps, and docs sync"
}

func (Gate) Help() string {
	return "Runs gofmt over the tree and go vet / go build / go test / golangci-lint in every committed Go module (today the repo root and .sparkwing/), runs go test -race on the packages that hold the staged Go files (or the Go files changed since origin/main when nothing is staged), runs the pkg/store suite against an embedded Postgres when that change touches pkg/store, runs the dashboard's TypeScript unit, full ESLint, production-build, and Playwright browser-smoke suites, plus the configured formatters (gofumpt + goimports), no em dashes, and no internal tracker IDs (IMP-/SDK-/LOCAL-/RUN-/ORG-/REG-/TOD- uppercase, BW- in either case) over the staged files, or over the files changed since origin/main when nothing is staged, and no disallowed comments (only GoDoc on exported APIs and // hack:/safety:/bug:/perf: tags) in the staged change, or in the change since origin/main plus every untracked Go file when nothing is staged, and repo-wide, that the embedded pkg/docs/ copies match the docs/ and CHANGELOG.md sources (via `bin/sync-docs.sh --check`; run bin/sync-docs.sh without the flag if it drifted) and that no product file resolves the sparkwing home itself, by reading SPARKWING_HOME or by joining a home directory with .sparkwing, instead of through internal/paths.DefaultPaths. The formatters, comment, em-dash, and tracker-ID steps name the mode they ran in, and the lint step names the modules it covered and the baseline it judged against. Set SPARKWING_REGEX_SWEEP_ALL=1 to sweep the whole tree for em dashes and tracker IDs. The git pre-push hook runs this pipeline; the far cheaper source-policy subset runs at pre-commit."
}

func (Gate) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run broad local verification", Command: "sparkwing run gate"},
	}
}

func (p *Gate) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(gateCoreReservation(runtime.NumCPU())))
	sparkwing.Job(plan, rc.Pipeline, p)
	return nil
}

// perf: admission charges sustained CPU, measured on a 16-core Linux host over
// 20 uncontended runs at p95 3.9 cores, maximum 4.4. Half the machine was
// double that, so a second gate never fit. A pin also survives an edit to this
// file, which resets the plan hash and would otherwise re-charge a cold start.
func gateCoreReservation(cpuCount int) float64 {
	if cpuCount < 4 {
		return 1
	}
	return float64(cpuCount)/4 + 0.5
}

// perf: bounds a Go step's burst to under half the machine, so one gate cannot
// saturate a box another gate is sharing.
func goStepParallelism(cpuCount int) int {
	if parallelism := (cpuCount - 1) / 2; parallelism > 1 {
		return parallelism
	}
	return 1
}

func boundedGoCommand(cpuCount int, verb, args string) string {
	parallelism := goStepParallelism(cpuCount)
	return fmt.Sprintf("GOMAXPROCS=%d go %s -p %d %s", parallelism, verb, parallelism, args)
}

func (p *Gate) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.ParallelFailures(sparkwing.FailFast)
	gofmtStep := sparkwing.Step(w, "gofmt", runGofmt)
	formattersStep := sparkwing.Step(w, "formatters", runFormatters).Needs(gofmtStep)
	vetStep := sparkwing.Step(w, "vet", runVet).Needs(formattersStep)
	buildStep := sparkwing.Step(w, "build", runBuild).Needs(vetStep)
	testStep := sparkwing.Step(w, "test", runTest).Needs(buildStep)
	sparkwing.Step(w, "lint", runGolangciLint).Needs(testStep)
	sparkwing.Step(w, "race-touched", runRaceTouched).Needs(testStep)
	sparkwing.Step(w, "store-postgres", runStorePostgresIfTouched).Needs(testStep)
	sparkwing.Step(w, "em-dashes", checkEmDashes)
	sparkwing.Step(w, "tracker-ids", checkTrackerIDs)
	sparkwing.Step(w, "tracked-binaries", checkTrackedBinaries)
	sparkwing.Step(w, "docs-mirror", checkDocsMirror)
	sparkwing.Step(w, "changelog-links", checkChangelogLinks)
	sparkwing.Step(w, "comments", checkComments)
	sparkwing.Step(w, "home-resolution", checkHomeResolution)
	frontendUnit := sparkwing.Step(w, "frontend-unit", runFrontendUnit)
	frontendLint := sparkwing.Step(w, "frontend-lint", runFrontendLint)
	frontendBuild := sparkwing.Step(w, "frontend-build", runFrontendBuild).Needs(frontendUnit, frontendLint)
	sparkwing.Step(w, "frontend-browser", runFrontendBrowser).Needs(frontendBuild)
	return nil, nil
}

func runFrontendUnit(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "npm --prefix web test").Run(); err != nil {
		return fmt.Errorf("frontend unit suite: %w", err)
	}
	return nil
}

func runFrontendLint(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "npm --prefix web run lint").Run(); err != nil {
		return fmt.Errorf("frontend ESLint suite: %w", err)
	}
	return nil
}

func runFrontendBuild(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "npm --prefix web run build").Run(); err != nil {
		return fmt.Errorf("frontend production build: %w", err)
	}
	return nil
}

func browserArtifactDirs() []string {
	web := filepath.Join(sparkwing.WorkDir(), "web")
	return []string{filepath.Join(web, "playwright-report"), filepath.Join(web, "test-results")}
}

func removeBrowserArtifacts() error {
	var failures []error
	for _, dir := range browserArtifactDirs() {
		if err := os.RemoveAll(dir); err != nil {
			failures = append(failures, fmt.Errorf("remove browser artifact directory %s: %w", dir, err))
		}
	}
	return errors.Join(failures...)
}

func runFrontendBrowser(ctx context.Context) error {
	if err := removeBrowserArtifacts(); err != nil {
		return err
	}
	marker := filepath.Join(sparkwing.WorkDir(), "web", "test-results", ".sparkwing-browser-failed")
	if _, err := sparkwing.Bash(ctx, "npm --prefix web run test:browser:gate").Run(); err != nil {
		if markerErr := os.MkdirAll(filepath.Dir(marker), 0o755); markerErr != nil {
			return errors.Join(fmt.Errorf("frontend browser smoke suite: %w", err), fmt.Errorf("create browser failure artifact directory: %w", markerErr))
		}
		if markerErr := os.WriteFile(marker, []byte("failed\n"), 0o644); markerErr != nil {
			return errors.Join(fmt.Errorf("frontend browser smoke suite: %w", err), fmt.Errorf("write browser failure artifact marker: %w", markerErr))
		}
		// safety: a failed run keeps both directories because the hosted gate uploads them after this step.
		return fmt.Errorf("frontend browser smoke suite: %w", err)
	}
	return removeBrowserArtifacts()
}

func checkComments(ctx context.Context) error {
	command, scope, err := commentCheckCommand(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "comments: %s", scope)
	_, err = sparkwing.Bash(ctx, command).Run()
	return err
}

func commentCheckCommand(ctx context.Context) (command, scope string, err error) {
	// safety: -staged gates an empty diff on a branch whose work is already
	// committed, so the step passes locally and the hosted gate, which stages
	// the whole push, fails the same comments after the fast-forward.
	staged, err := stagedNames(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list the staged change: %w", err)
	}
	if len(staged) > 0 {
		return "go run ./internal/commentcheck -staged .",
			fmt.Sprintf("%d staged Go file(s)", len(existingGoFiles(staged))), nil
	}
	base, err := resolveGateBase(ctx)
	if err != nil {
		return "", "", err
	}
	return "go run ./internal/commentcheck -base " + base + " .",
		fmt.Sprintf("nothing staged, so the change since %s (%s) and every untracked Go file",
			gateBaselineRef, base), nil
}

var homeEnvRead = regexp.MustCompile(`(?:os\.)?(?:Getenv|LookupEnv)\(\s*"SPARKWING_HOME"\s*\)`)

var homeDirJoin = regexp.MustCompile(`filepath\.Join\([^,)]*[Hh]ome[^,)]*,\s*"\.sparkwing"`)

type homeRule struct {
	label   string
	pattern *regexp.Regexp
	allowed map[string]string
	advice  string
}

var homeRules = []homeRule{
	{
		label:   "read SPARKWING_HOME from the environment",
		pattern: homeEnvRead,
		allowed: map[string]string{
			"internal/paths/paths.go":      "owns the resolution, and with it the test-sandbox redirect every other caller inherits",
			"pkg/storage/storeurl/spec.go": "public SDK surface, and the pkg/ tree imports nothing from internal/, so it carries a documented copy of the sandbox redirect",
		},
		advice: "Call internal/paths.DefaultPaths() instead, which honors SPARKWING_HOME the same way and adds the test-sandbox redirect that keeps a test binary out of the developer's real ~/.sparkwing.",
	},
	{
		label:   "build the sparkwing home from a home directory",
		pattern: homeDirJoin,
		allowed: map[string]string{
			"internal/paths/paths.go":             "owns the resolution, and with it the test-sandbox redirect every other caller inherits",
			"pkg/storage/storeurl/spec.go":        "public SDK surface, and the pkg/ tree imports nothing from internal/, so it carries a documented copy of the sandbox redirect",
			"internal/configguard/configguard.go": "watches the real user's home for writes a suite should not have made, so resolving anywhere else would measure the wrong directory; its package doc states this",
		},
		advice: "Call internal/paths.DefaultPaths() for the real home, or paths.PathsAt(root) when the root is already known.",
	},
}

func checkHomeResolution(ctx context.Context) error {
	root := regexCheckRoot()
	files, err := listNames(ctx, `ls-files -z -- '*.go'`)
	if err != nil {
		return fmt.Errorf("list the tracked Go files: %w", err)
	}

	offenders := make([][]string, len(homeRules))
	for _, f := range files {
		if f == "" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		if strings.HasPrefix(f, ".sparkwing/") || strings.Contains(f, "node_modules/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		code := strippedGoComments(string(data))
		for i, rule := range homeRules {
			if _, ok := rule.allowed[f]; ok {
				continue
			}
			if rule.pattern.MatchString(code) {
				offenders[i] = append(offenders[i], f)
			}
		}
	}

	var failures []string
	for i, rule := range homeRules {
		if len(offenders[i]) == 0 {
			continue
		}
		for _, f := range offenders[i] {
			sparkwing.Info(ctx, "  %s: %s", rule.label, f)
		}
		allowed := make([]string, 0, len(rule.allowed))
		for f, why := range rule.allowed {
			allowed = append(allowed, fmt.Sprintf("%s (%s)", f, why))
		}
		sort.Strings(allowed)
		failures = append(failures, fmt.Sprintf("%d file(s) %s:\n  - %s\n%s Only these may do it directly:\n  - %s",
			len(offenders[i]), rule.label, strings.Join(offenders[i], "\n  - "),
			rule.advice, strings.Join(allowed, "\n  - ")))
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("the sparkwing home must be resolved through internal/paths:\n\n%s",
		strings.Join(failures, "\n\n"))
}

func strippedGoComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

func runGofmt(ctx context.Context) error {
	return sparkwing.Bash(ctx, `gofmt -l .`).MustBeEmpty("files need formatting")
}

func runFormatters(ctx context.Context) error {
	files, scope, err := changeScope(ctx, "Go file(s)", existingGoFiles)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "formatters: %s", scope)
	if len(files) == 0 {
		return nil
	}
	_, runErr := sparkwing.Bash(ctx, "golangci-lint fmt --diff -- "+shellQuoteAll(files)).Capture()
	if runErr == nil {
		return nil
	}
	var execErr *sparkwing.ExecError
	if errors.As(runErr, &execErr) && strings.TrimSpace(execErr.Stdout) != "" {
		return fmt.Errorf("%s do not match the configured formatters; run `golangci-lint fmt -- %s`:\n%s",
			scope, shellQuoteAll(files), strings.TrimSpace(execErr.Stdout))
	}
	return fmt.Errorf("golangci-lint fmt: %w", runErr)
}

func changeScope(ctx context.Context, noun string, keep func([]string) []string) ([]string, string, error) {
	staged, err := stagedNames(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list the staged change: %w", err)
	}
	// safety: branching on the filtered set would send a commit that stages
	// only documentation down the origin/main path, which widens the scope on a
	// long branch and hard-fails a checkout that cannot resolve the baseline.
	if len(staged) > 0 {
		files := keep(staged)
		return files, fmt.Sprintf("%d staged %s", len(files), noun), nil
	}
	base, err := resolveGateBase(ctx)
	if err != nil {
		return nil, "", err
	}
	changed, err := listNames(ctx, "diff -z --name-only --diff-filter=ACMR "+base)
	if err != nil {
		return nil, "", fmt.Errorf("list the change since %s: %w", base, err)
	}
	files := keep(changed)
	return files, fmt.Sprintf("nothing staged, so %d %s changed since %s (%s)",
		len(files), noun, gateBaselineRef, base), nil
}

func resolveGateBase(ctx context.Context) (string, error) {
	sha, err := sparkwing.Bash(ctx, "git merge-base "+gateBaselineRef+" HEAD").String()
	sha = strings.TrimSpace(sha)
	if err != nil || sha == "" {
		return "", fmt.Errorf("could not run -- nothing is staged, so the step reads the change "+
			"since %s, and this checkout cannot resolve it. Run `%s`",
			gateBaselineRef, fetchBaselineHint())
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return sha, nil
}

// safety: %q is Go quoting. It leaves $ and backticks untouched, and the
// result lands inside the double quotes bash expands, so a staged filename
// would run a command inside the gate that judges it.
func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func shellQuoteAll(paths []string) string {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, shellQuote(p))
	}
	return strings.Join(quoted, " ")
}

// safety: git quotes a path holding a non-ASCII byte unless core.quotePath is
// off, and a quoted name matches no suffix and stats to nothing, so the file
// drops out of every scoped step and the step passes without judging it. -z
// also carries a name holding a newline, which no line-split can, and Capture
// rather than String because String trims the blob, eating the leading byte of
// a name that begins with whitespace and sorts first.
func listNames(ctx context.Context, args string) ([]string, error) {
	res, err := sparkwing.Bash(ctx, "git -c core.quotePath=false "+args).Capture()
	if err != nil {
		return nil, err
	}
	return splitNULNames(res.Stdout), nil
}

func stagedNames(ctx context.Context) ([]string, error) {
	cmd := sparkwing.Bash(ctx, "git -c core.quotePath=false diff --cached -z --name-only --diff-filter=ACMR")
	// safety: the CLI unbinds GIT_INDEX_FILE at startup and records it here, so
	// the commit a hook is building lives in $SPARKWING_GATE_INDEX. Reading the
	// repository index instead reports `git commit -a` as staging nothing, and
	// every scoped step then widens to the branch range.
	if index := hookIndex(); index != "" {
		cmd = cmd.Env("GIT_INDEX_FILE", index)
	}
	res, err := cmd.Capture()
	if err != nil {
		return nil, err
	}
	return splitNULNames(res.Stdout), nil
}

func hookIndex() string {
	if os.Getenv("GIT_INDEX_FILE") != "" {
		return ""
	}
	return gitenv.GateIndex()
}

func splitNULNames(out string) []string {
	var names []string
	for _, name := range strings.Split(out, "\x00") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func existingGoFiles(all []string) []string {
	out := make([]string, 0, len(all))
	for _, f := range all {
		if !strings.HasSuffix(f, ".go") || strings.Contains(f, "node_modules/") {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(regexCheckRoot(), f)); statErr != nil {
			continue
		}
		out = append(out, f)
	}
	return out
}

func sweepableFiles(all []string) []string {
	out := make([]string, 0, len(all))
	for _, f := range all {
		if f == "" {
			continue
		}
		if strings.HasPrefix(f, "tickets/") || strings.HasPrefix(f, "archive/") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// safety: these rules also run in the lint pipeline, which no commit passes
// through. A dead documentation link reaches an adopter through the published
// changelog, so the check belongs where a commit is judged.
func checkChangelogLinks(ctx context.Context) error {
	if err := CheckChangelogLint(ctx, sparkwing.WorkDir()); err != nil {
		return fmt.Errorf("a released changelog entry may be edited to repair a link: restore the document, "+
			"or repoint at a github.com/sparkwing-dev/sparkwing/blob/<commit-or-tag>/ permalink that still carries it: %w", err)
	}
	return nil
}

func checkDocsMirror(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/sync-docs.sh --check").Run(); err != nil {
		return fmt.Errorf("the embedded docs mirror is out of sync; run `bash bin/sync-docs.sh && git add pkg/docs` (edit docs/ and CHANGELOG.md, never the mirror): %w", err)
	}
	return nil
}

// safety: a node hands each child the run's own bindings, so a suite that
// reads one reaches the machine's admission daemon, the operator's controller
// or cache, or a credential of the run's, and reds only inside a gate.
// internal/runners/local/env.go is the injector these names track.
var productTestUnset = []string{
	wingwire.LeaseTokenEnv,
	wingwire.ChildLeaseTokenEnv,
	wingwire.APISocketEnv,
	"GIT_INDEX_FILE",
	"SPARKWING_AGENT_TOKEN",
	"SPARKWING_CACHE_URL",
	"SPARKWING_CONTROLLER_URL",
	"SPARKWING_DRY_RUN",
	"SPARKWING_LOGS_URL",
	"SPARKWING_NODE_ID",
	"SPARKWING_PARENT_LIVENESS_FD",
	"SPARKWING_RUN_ID",
	"SPARKWING_TOKEN",
}

// safety: the injected names a suite may keep, each because it names no
// service and carries no credential. A name absent from both this map and the
// list above fails the contract test rather than reaching a suite unread.
var productTestKept = map[string]string{
	"SPARKWING_LOG_FORMAT":    "picks a renderer",
	"SPARKWING_RUNNER_NAME":   "reports which runner executed the node",
	"SPARKWING_RUNNER_TYPE":   "reports which runner executed the node",
	"SPARKWING_RUNNER_LABELS": "reports what the local runner advertises",
	"SPARKWING_START_AT":      "records the window the run already chose",
	"SPARKWING_STOP_AT":       "records the window the run already chose",
}

// safety: the pipeline module cannot import internal/orchestrator, so this
// spelling of its DevEnvDisableEnv is pinned by a contract test instead.
// Clearing the two URLs is not enough on its own, because both fall back to
// the dev.env under the home.
const devEnvDisableVar = "SPARKWING_DEV_ENV_DISABLE"

const productTestHomeVar = "SPARKWING_HOME"

func productTestPins(home string) []string {
	return []string{devEnvDisableVar + "=1", productTestHomeVar + "=" + shellQuote(home)}
}

func withoutInherited(cmd string, names []string) string {
	if len(names) == 0 {
		return cmd
	}
	return "unset " + strings.Join(names, " ") + "; " + cmd
}

func withPinned(cmd string, bindings []string) string {
	if len(bindings) == 0 {
		return cmd
	}
	return "export " + strings.Join(bindings, " ") + "; " + cmd
}

// safety: every step that starts a product suite composes its command here, so
// the scrub cannot be half-applied across the gate's several test steps. The
// unset runs first, which is what lets the pins survive it.
func productTestScript(cmd, home string) string {
	return withoutInherited(withPinned(cmd, productTestPins(home)), productTestUnset)
}

// safety: a helper binary a suite starts is not a *.test binary, so
// internal/paths resolves the operator's home for it rather than a per-pid
// sandbox, and it opens the real runs store. A home of the step's own is what
// the suite reaches instead.
func withProductTestHome(run func(home string) error) error {
	home, err := os.MkdirTemp("", "sparkwing-gate-home-")
	if err != nil {
		return fmt.Errorf("create the suite's sparkwing home: %w", err)
	}
	runErr := run(home)
	cleanupErr := os.RemoveAll(home)
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("remove the suite's sparkwing home: %w", cleanupErr)
	}
	return errors.Join(runErr, cleanupErr)
}

func runVet(ctx context.Context) error {
	return forEachGoModule(ctx, "go vet", boundedGoCommand(runtime.NumCPU(), "vet", "./..."), "")
}

func runBuild(ctx context.Context) error {
	return forEachGoModule(ctx, "go build", boundedGoCommand(runtime.NumCPU(), "build", "./..."), "")
}

func runTest(ctx context.Context) error {
	return withProductTestHome(func(home string) error {
		return forEachGoModule(ctx, "go test", boundedGoCommand(runtime.NumCPU(), "test", "./..."), home)
	})
}

func withGoTestScratch(run func(string) error) error {
	testRoot, err := os.MkdirTemp("", "sparkwing-go-test-")
	if err != nil {
		return fmt.Errorf("create go test temporary root: %w", err)
	}
	testErr := run(testRoot)
	cleanupErr := os.RemoveAll(testRoot)
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("remove go test temporary root: %w", cleanupErr)
	}
	return errors.Join(testErr, cleanupErr)
}

// safety: an empty home means the step starts no product suite, which is the
// only reason a step skips the scrub.
func forEachGoModule(ctx context.Context, label, cmd, home string) error {
	dirs, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	var failures []string
	for _, dir := range dirs {
		packages, err := modulePackageArgs(ctx, dir, label != "go build")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		if len(packages) == 0 {
			continue
		}
		command := strings.TrimSuffix(cmd, "./...") + strings.Join(packages, " ")
		script := fmt.Sprintf("cd %q && %s", dir, command)
		if home != "" {
			script = productTestScript(script, home)
		}
		if _, err := sparkwing.Bash(ctx, script).Run(); err != nil {
			failures = append(failures, describeModuleFailure(dir, err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s failed in %d module(s):\n  - %s",
		label, len(failures), strings.Join(failures, "\n  - "))
}

const maxNamedTestFailures = 25

// safety: the summary is bounded from its head, so the names go first or a
// hosted runner keeps nothing but the module that failed.
func describeModuleFailure(dir string, err error) string {
	named := failedTestNames(err)
	if len(named) == 0 {
		return fmt.Sprintf("%s: %v", dir, err)
	}
	return fmt.Sprintf("%s: %s\n%v", dir, strings.Join(named, "\n"), err)
}

func failedTestNames(err error) []string {
	var execErr *sparkwing.ExecError
	if !errors.As(err, &execErr) {
		return nil
	}
	var out []string
	truncated := false
	for _, line := range strings.Split(execErr.Stdout+"\n"+execErr.Stderr, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "--- FAIL: ") && !strings.HasPrefix(line, "FAIL\t") {
			continue
		}
		if len(out) == maxNamedTestFailures {
			truncated = true
			break
		}
		out = append(out, line)
	}
	if truncated {
		out = append(out, fmt.Sprintf("… more than %d failures; read the run log for the rest", maxNamedTestFailures))
	}
	return out
}

func modulePackageArgs(ctx context.Context, dir string, testOnly bool) ([]string, error) {
	// safety: -e keeps broken product packages in the list for vet/build/test to reject.
	out, err := sparkwing.Bash(ctx, fmt.Sprintf(`cd %q && go list -e -f '{{.ImportPath}} {{len .GoFiles}}' ./...`, dir)).String()
	if err != nil {
		return nil, fmt.Errorf("list module packages: %w", err)
	}
	var packages []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		path := fields[0]
		if strings.Contains("/"+path+"/", "/node_modules/") {
			continue
		}
		// safety: go build refuses a package that holds only tests, which ./... skipped silently.
		if !testOnly && fields[1] == "0" {
			continue
		}
		packages = append(packages, fmt.Sprintf("%q", path))
	}
	return packages, nil
}

func moduleHasNoPackages(ctx context.Context, dir string) (bool, error) {
	out, err := sparkwing.Bash(ctx, fmt.Sprintf(`cd %q && go list ./... 2>&1 || true`, dir)).String()
	if err != nil {
		return false, err
	}
	out = strings.TrimSpace(out)
	return out == "" || strings.Contains(out, "matched no packages"), nil
}

// safety: BW is the one prefix with a lowercase spelling in daily use, so it
// matches in either case; the rest appear only uppercase.
var trackerIDPattern = regexp.MustCompile(`\b(?:(?:IMP|SDK|LOCAL|RUN|ORG|REG|TOD)|(?i:BW))-[0-9]+\b`)

func checkEmDashes(ctx context.Context) error {
	files, scope, err := regexCheckFiles(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "em-dashes: %s", scope)
	root := regexCheckRoot()
	var bad []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil || len(data) == 0 {
			continue
		}
		// hack: null byte in first 8KB signals binary; skip to avoid false em-dash matches.
		head := data
		if len(head) > 8192 {
			head = head[:8192]
		}
		if bytes.IndexByte(head, 0) >= 0 {
			continue
		}
		if bytes.Contains(data, []byte("\u2014")) {
			bad = append(bad, f)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	for _, f := range bad {
		sparkwing.Info(ctx, "  em dash in: %s", f)
	}
	return fmt.Errorf("em dashes in %d file(s)", len(bad))
}

func checkTrackerIDs(ctx context.Context) error {
	files, scope, err := regexCheckFiles(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "tracker-ids: %s", scope)
	root := regexCheckRoot()
	var bad []string
	for _, f := range files {
		if f == "CHANGELOG.md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil || len(data) == 0 {
			continue
		}
		// hack: null byte in first 8KB signals binary; skip to avoid false tracker-ID matches.
		head := data
		if len(head) > 8192 {
			head = head[:8192]
		}
		if bytes.IndexByte(head, 0) >= 0 {
			continue
		}
		if trackerIDPattern.Match(data) {
			bad = append(bad, f)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	for _, f := range bad {
		sparkwing.Info(ctx, "  tracker ID in: %s", f)
	}
	return fmt.Errorf("tracker IDs in %d file(s)", len(bad))
}

func regexCheckFiles(ctx context.Context) ([]string, string, error) {
	if os.Getenv("SPARKWING_REGEX_SWEEP_ALL") != "" {
		all, err := listNames(ctx, "ls-files -z")
		if err != nil {
			return nil, "", fmt.Errorf("list the tracked files: %w", err)
		}
		files := sweepableFiles(all)
		return files, fmt.Sprintf("SPARKWING_REGEX_SWEEP_ALL is set, so %d tracked file(s)", len(files)), nil
	}
	return changeScope(ctx, "file(s)", sweepableFiles)
}

func regexCheckRoot() string {
	r := sparkwing.WorkDir()
	if r == "" {
		r = "."
	}
	return r
}

func init() {
	sparkwing.Register("gate", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Gate{} })
}

// safety: Go builds of one package leave an executable beside the sources,
// and a broad add sweeps it into a commit; this reads the container from the
// header bytes and returns "" for anything that is not one.
func executableFormat(head []byte) string {
	if len(head) < 4 {
		return ""
	}
	switch {
	case bytes.HasPrefix(head, []byte{0x7f, 'E', 'L', 'F'}):
		return "ELF"
	case bytes.HasPrefix(head, []byte{0xfe, 0xed, 0xfa, 0xce}),
		bytes.HasPrefix(head, []byte{0xfe, 0xed, 0xfa, 0xcf}),
		bytes.HasPrefix(head, []byte{0xce, 0xfa, 0xed, 0xfe}),
		bytes.HasPrefix(head, []byte{0xcf, 0xfa, 0xed, 0xfe}),
		bytes.HasPrefix(head, []byte{0xca, 0xfe, 0xba, 0xbe}):
		return "Mach-O"
	case head[0] == 'M' && head[1] == 'Z':
		return "PE"
	}
	return ""
}

func checkTrackedBinaries(ctx context.Context) error {
	files, err := listNames(ctx, "ls-files -z")
	if err != nil {
		return err
	}
	root := regexCheckRoot()
	var bad []string
	head := make([]byte, 4)
	for _, f := range files {
		fh, err := os.Open(filepath.Join(root, f))
		if err != nil {
			continue
		}
		n, readErr := fh.Read(head)
		_ = fh.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			continue
		}
		if format := executableFormat(head[:n]); format != "" {
			bad = append(bad, fmt.Sprintf("%s (%s)", f, format))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	for _, f := range bad {
		sparkwing.Info(ctx, "  tracked executable: %s", f)
	}
	return fmt.Errorf("%d tracked executable(s); remove them from the index and ignore their names", len(bad))
}
