package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type PreCommit struct{ sparkwing.Base }

func (PreCommit) ShortHelp() string {
	return "Broad local verification: Go gates, frontend checks, source-policy sweeps, and docs sync"
}

func (PreCommit) Help() string {
	return "Runs gofmt over the tree and go vet / go build / go test / golangci-lint in every committed Go module (today the repo root and .sparkwing/), runs the dashboard's TypeScript unit, full ESLint, production-build, and Playwright browser-smoke suites, plus the configured formatters (gofumpt + goimports), no em dashes, and no internal tracker IDs (IMP-/SDK-/LOCAL-/RUN-/ORG-/REG-/TOD-) over the staged files, or over the files changed since origin/main when nothing is staged, and on the staged change, no disallowed comments (only GoDoc on exported APIs and // hack:/safety:/bug:/perf: tags), and repo-wide, that the embedded pkg/docs/ copies match the docs/ and CHANGELOG.md sources (via `bin/sync-docs.sh --check`; run bin/sync-docs.sh without the flag if it drifted) and that no product file resolves the sparkwing home itself, by reading SPARKWING_HOME or by joining a home directory with .sparkwing, instead of through internal/paths.DefaultPaths. The formatters, em-dash, and tracker-ID steps name the mode they ran in, and the lint step names the modules it covered and the baseline it judged against. Set SPARKWING_REGEX_SWEEP_ALL=1 to sweep the whole tree for em dashes and tracker IDs."
}

func (PreCommit) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run broad local verification", Command: "sparkwing run pre-commit"},
	}
}

func (p *PreCommit) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(float64(preCommitCPUReservation(runtime.NumCPU()))))
	sparkwing.Job(plan, rc.Pipeline, p)
	return nil
}

func preCommitCPUReservation(cpuCount int) int {
	if cpuCount < 2 {
		return 1
	}
	return (cpuCount + 1) / 2
}

func boundedGoCommand(cpuCount int, verb, args string) string {
	parallelism := preCommitCPUReservation(cpuCount) - 1
	if parallelism < 1 {
		parallelism = 1
	}
	return fmt.Sprintf("GOMAXPROCS=%d go %s -p %d %s", parallelism, verb, parallelism, args)
}

func (p *PreCommit) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.ParallelFailures(sparkwing.FailFast)
	gofmtStep := sparkwing.Step(w, "gofmt", runGofmt)
	formattersStep := sparkwing.Step(w, "formatters", runFormatters).Needs(gofmtStep)
	vetStep := sparkwing.Step(w, "vet", runVet).Needs(formattersStep)
	buildStep := sparkwing.Step(w, "build", runBuild).Needs(vetStep)
	testStep := sparkwing.Step(w, "test", runTest).Needs(buildStep)
	sparkwing.Step(w, "lint", runGolangciLint).Needs(testStep)
	sparkwing.Step(w, "race-touched", runRaceTouched).Needs(testStep)
	sparkwing.Step(w, "em-dashes", checkEmDashes)
	sparkwing.Step(w, "tracker-ids", checkTrackerIDs)
	sparkwing.Step(w, "docs-mirror", checkDocsMirror)
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
	_, err := sparkwing.Bash(ctx, `go run ./internal/commentcheck -staged .`).Run()
	return err
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
			"pkg/storage/storeurl/spec.go": "public SDK surface, and the pkg/ tree imports nothing from internal/, so it carries a documented copy of the same rule including the redirect",
		},
		advice: "Call internal/paths.DefaultPaths() instead, which honors SPARKWING_HOME the same way and adds the test-sandbox redirect that keeps a test binary out of the developer's real ~/.sparkwing.",
	},
	{
		label:   "build the sparkwing home from a home directory",
		pattern: homeDirJoin,
		allowed: map[string]string{
			"internal/paths/paths.go":             "owns the resolution, and with it the test-sandbox redirect every other caller inherits",
			"pkg/storage/storeurl/spec.go":        "public SDK surface, and the pkg/ tree imports nothing from internal/, so it carries a documented copy of the same rule including the redirect",
			"internal/configguard/configguard.go": "watches the real user's home for writes a suite should not have made, so resolving anywhere else would measure the wrong directory; its package doc states this",
		},
		advice: "Call internal/paths.DefaultPaths() for the real home, or paths.PathsAt(root) when the root is already known.",
	},
}

func checkHomeResolution(ctx context.Context) error {
	root := regexCheckRoot()
	files, err := sparkwing.Bash(ctx, `git ls-files -- '*.go'`).Lines()
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
	quoted := make([]string, 0, len(files))
	for _, f := range files {
		quoted = append(quoted, fmt.Sprintf("%q", f))
	}

	_, runErr := sparkwing.Bash(ctx, "golangci-lint fmt --diff "+strings.Join(quoted, " ")).Capture()
	if runErr == nil {
		return nil
	}
	var execErr *sparkwing.ExecError
	if errors.As(runErr, &execErr) && strings.TrimSpace(execErr.Stdout) != "" {
		return fmt.Errorf("%s do not match the configured formatters; run `golangci-lint fmt %s`:\n%s",
			scope, strings.Join(files, " "), strings.TrimSpace(execErr.Stdout))
	}
	return fmt.Errorf("golangci-lint fmt: %w", runErr)
}

func changeScope(ctx context.Context, noun string, keep func([]string) []string) ([]string, string, error) {
	staged, err := listNames(ctx, `git diff --cached --name-only --diff-filter=ACMR`)
	if err != nil {
		return nil, "", fmt.Errorf("list the staged change: %w", err)
	}
	if files := keep(staged); len(files) > 0 {
		return files, fmt.Sprintf("%d staged %s", len(files), noun), nil
	}
	base, err := resolveGateBase(ctx)
	if err != nil {
		return nil, "", err
	}
	changed, err := listNames(ctx, "git diff --name-only --diff-filter=ACMR "+base)
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

func listNames(ctx context.Context, cmd string) ([]string, error) {
	return sparkwing.Bash(ctx, cmd).Lines()
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

func checkDocsMirror(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/sync-docs.sh --check").Run(); err != nil {
		return fmt.Errorf("the embedded docs mirror is out of sync; run `bash bin/sync-docs.sh && git add pkg/docs` (edit docs/ and CHANGELOG.md, never the mirror): %w", err)
	}
	return nil
}

var productTestUnset = []string{
	wingwire.LeaseTokenEnv,
	wingwire.ChildLeaseTokenEnv,
	"GIT_INDEX_FILE",
}

func withoutInherited(cmd string, names []string) string {
	if len(names) == 0 {
		return cmd
	}
	return "unset " + strings.Join(names, " ") + "; " + cmd
}

func runVet(ctx context.Context) error {
	return forEachGoModule(ctx, "go vet", boundedGoCommand(runtime.NumCPU(), "vet", "./..."), nil)
}

func runBuild(ctx context.Context) error {
	return forEachGoModule(ctx, "go build", boundedGoCommand(runtime.NumCPU(), "build", "./..."), nil)
}

func runTest(ctx context.Context) error {
	return withGoTestScratch(func(testRoot string) error {
		return forEachGoModuleEnv(
			ctx, "go test", boundedGoCommand(runtime.NumCPU(), "test", "./..."), productTestUnset,
			map[string]string{"TMPDIR": testRoot},
		)
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

func forEachGoModule(ctx context.Context, label, cmd string, unset []string) error {
	return forEachGoModuleEnv(ctx, label, cmd, unset, nil)
}

func forEachGoModuleEnv(ctx context.Context, label, cmd string, unset []string, env map[string]string) error {
	dirs, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	var failures []string
	for _, dir := range dirs {
		if empty, err := moduleHasNoPackages(ctx, dir); err == nil && empty {
			continue
		}
		script := withoutInherited(fmt.Sprintf("cd %q && %s", dir, cmd), unset)
		run := sparkwing.Bash(ctx, script)
		for name, value := range env {
			run.Env(name, value)
		}
		if _, err := run.Run(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", dir, err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s failed in %d module(s):\n  - %s",
		label, len(failures), strings.Join(failures, "\n  - "))
}

func moduleHasNoPackages(ctx context.Context, dir string) (bool, error) {
	out, err := sparkwing.Bash(ctx, fmt.Sprintf(`cd %q && go list ./... 2>&1 || true`, dir)).String()
	if err != nil {
		return false, err
	}
	out = strings.TrimSpace(out)
	return out == "" || strings.Contains(out, "matched no packages"), nil
}

var trackerIDPattern = regexp.MustCompile(`\b(IMP|SDK|LOCAL|RUN|ORG|REG|TOD)-[0-9]+\b`)

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
		all, err := listNames(ctx, "git ls-files")
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
	sparkwing.Register("pre-commit", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PreCommit{} })
}
