package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Gate struct {
	sparkwing.Base
	sparkwing.Produces[bool]
	Proven sparkwing.Ref[gatePass]
	mode   gateMode
}

// safety: a struct job, because only a reference held in a job field is
// declared in the plan, and the skip predicate reads one.
type raceTouchedJob struct {
	sparkwing.Base
	sparkwing.Produces[bool]
	Proven sparkwing.Ref[gatePass]
	mode   gateMode
}

func (j *raceTouchedJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (bool, error) { return true, runGateRaceTouched(ctx, j.mode) }), nil
}

type GateArgs struct {
	Full bool   `flag:"full" desc:"Run every Go test and PostgreSQL suite instead of affected fast tests and the critical set."`
	Base string `flag:"base" desc:"Fetch this public repository commit as origin/main before each job, for an uploaded snapshot."`
}

// safety: the hosted full-test chain can take 36 minutes before the 30-minute
// Postgres tail. The hosted job keeps ten more minutes for setup and diagnostics.
const gateRunTimeout = 70 * time.Minute

func (Gate) ShortHelp() string {
	return "Broad verification on demand and in hosted CI: Go gates, frontend checks, source-policy sweeps, contract gates, and docs sync"
}

func (Gate) Help() string {
	return "Runs a policy job first, and no heavy job starts until it passes, so a source-policy finding fails the run within seconds: gofmt over the tree, the configured formatters, em dashes, tracker IDs, comments, test sleeps, tracked binaries, home resolution, the docs mirror, changelog lint and links, the required CHANGELOG.md entry, the OpenAPI and API-snapshot checks, the generated CLI reference (bin/gen-cli-docs.sh --check), Markdown lint, config-reference drift and wire-cut coverage. " +
		"Together the jobs run gofmt over the tree and go vet / go build over the affected dependency closure and golangci-lint over touched Go packages in committed modules (every package with --full), runs go test -short on the reverse dependency closure of the packages the change touches (including test imports, staged or changed since origin/main) and runs every test tagged //sparkwing:critical without -short, or on every package with -- --full, which nightly hosted CI passes, runs go test -race -short on the packages that hold the staged Go files (or the Go files changed since origin/main when nothing is staged, with all tracked files judged if that baseline is unavailable, or every package without -short with -- --full) apart from internal/store, which cannot finish inside the step's budget and races in pre-release instead, runs the internal/store suite against an embedded Postgres only with -- --full, leaving long integration bodies and PostgreSQL to full, nightly and release checks, runs the dashboard's TypeScript unit, full ESLint, production-build, and Playwright browser-smoke suites, plus the configured formatters (gofumpt + goimports), no em dashes, and no internal tracker IDs (IMP-/SDK-/LOCAL-/RUN-/ORG-/REG-/TOD- uppercase, BW- in either case) over the staged files, or over the files changed since origin/main when nothing is staged, and no disallowed comments (only GoDoc on exported APIs and // hack:/safety:/bug:/perf: tags) in the staged change, or in the change since origin/main plus every untracked Go file when nothing is staged, and no test that sleeps or waits on the wall clock over that same scope, and repo-wide, that the embedded pkg/docs/ copies match the docs/ and CHANGELOG.md sources (via `bin/sync-docs.sh --check`; run bin/sync-docs.sh without the flag if it drifted) and that no product file resolves the sparkwing home itself, by reading SPARKWING_HOME or by joining a home directory with .sparkwing, instead of through internal/paths.DefaultPaths. The formatters, comment, em-dash, and tracker-ID steps name the mode they ran in, and the lint step names the modules it covered and the baseline it judged against. It also requires a CHANGELOG.md entry for every covered surface the change touches (bin/check-changelog.sh), api/openapi.yaml agreeing with the controller's route table (bin/check-api-spec.sh), and the public API surface matching the .apidiff/ snapshot (bin/check-api-snapshot.sh). Set SPARKWING_REGEX_SWEEP_ALL=1 to sweep the whole tree for em dashes and tracker IDs. No git hook runs this pipeline: `sparkwing run gate` runs it on demand and hosted CI runs it on every pull request and every push to main. The two hook tiers are the far cheaper subsets pre-commit and pre-push. A clean run whose gate and race-touched jobs both ran and passed outputs HEAD's tree, the gate definition fingerprint, the engine version and the mode from its record-tree job; a run whose jobs skipped records nothing. A later run at the same commit skips every job when the controller finds such a pass from a full-mode run proven for this repository (from GitHub or fetched by the controller) that selected every job without dry run, sealed no environment variables and ran every job on a trusted host (a fresh host, or a runner scoped to repositories with --repos), and says which runner proved it. A plain local run proves no repository, so it never lets another run skip. Without --full, vet, build, lint, the critical tests and the TypeScript SDK suite run only when the change reaches Go (a .go file, a non-Markdown file inside a Go package, go.mod, go.sum, .golangci.yml or the store schema) or, for the SDK suite, sdk/typescript/, and the frontend dependency install runs only when a frontend suite will or web/node_modules is missing, so a docs, UI or process change lands on its own tests plus the review (decision 96)."
}

func (Gate) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run broad local verification", Command: "sparkwing run gate"},
		{Comment: "Run every Go test, as hosted CI does", Command: "sparkwing run gate -- --full"},
	}
}

func (p *Gate) Plan(_ context.Context, plan *sparkwing.Plan, in GateArgs, rc sparkwing.RunContext) error {
	jobs := gateJobs(plan, p, rc.Pipeline, in.Full)
	if in.Base != "" {
		if err := validateGateBase(in.Base); err != nil {
			return err
		}
		for _, job := range jobs {
			if job.ID() == rc.Pipeline || job.ID() == "race-touched" {
				job.Cores(overlappedGoSuiteCPUs)
			}
			job.BeforeRun(func(ctx context.Context) error {
				return fetchGateBase(ctx, "https://github.com/sparkwing-dev/sparkwing.git", in.Base)
			})
		}
	}
	return nil
}

// safety: land composes the gate through this one function and orders its
// push after every node returned, so a job added to the gate here is one the
// push waits for.
func gateJobs(plan *sparkwing.Plan, p *Gate, id string, full bool) []*sparkwing.JobNode {
	history, tags := true, true
	plan.Checkout(sparkwing.Checkout{FullHistory: &history, Tags: &tags})
	p.mode = gateAffected
	if full {
		p.mode = gateFull
	}
	ref := provenGatePass(id)
	p.Proven = ref
	proven := skipWhenTreePassed(ref)
	policy := sparkwing.Job(plan, policyJobID, &policyJob{Proven: ref}).Timeout(policyJobTimeout).SkipIf(proven)
	broad := sparkwing.Job(plan, id, p).Timeout(gateRunTimeout).SkipIf(proven).Needs(policy)
	race := sparkwing.Job(plan, "race-touched", &raceTouchedJob{Proven: ref, mode: p.mode}).Timeout(raceTouchedStepTimeout).SkipIf(proven).Needs(policy)
	record := &recordTree{Proven: ref, Gate: sparkwing.RefTo[bool](broad), Race: sparkwing.RefTo[bool](race), mode: p.mode}
	tree := sparkwing.Job(plan, recordTreeJob, record).Needs(broad, race).SkipIf(proven).Cores(lightJobCores)
	return []*sparkwing.JobNode{policy, broad, race, tree}
}

// perf: jobs that mostly wait on git, the network or a reviewer ask local
// admission for a quarter core, so they start beside running gates.
const lightJobCores = 0.25

// perf: four-core runners need explicit per-process limits for heavy suites.
const overlappedGoSuiteCPUs = 4

func runGateRaceTouched(ctx context.Context, mode gateMode) error {
	var suitesDone chan struct{}
	if currentHost().singleTenant {
		// perf: the hosted race job owns its runner, so all four workers can start immediately.
		suitesDone = make(chan struct{})
		close(suitesDone)
	}
	return runRaceTouched(ctx, mode, suitesDone)
}

// safety: tenancy travels with the CPU count so a step's parallelism has no
// input the caller cannot see, and a test says which host it describes.
type hostShape struct {
	cpus         int
	singleTenant bool
}

// safety: CI names the hosted runner, which carries one job and is thrown away
// after it, so no neighbor loses cores to a gate that takes the whole box.
func currentHost() hostShape {
	return hostShape{cpus: min(runtime.NumCPU(), runtime.GOMAXPROCS(0)), singleTenant: os.Getenv("CI") != ""}
}

// perf: half the machine, so one gate cannot saturate a box another is
// sharing. The shared form subtracts a CPU first and so collapses to one
// thread at four, pinning a gate that reserved 2.5 cores to one of them. A
// single-tenant host holds nothing back for a neighbor it does not have.
func goStepParallelism(h hostShape) int {
	parallelism := (h.cpus - 1) / 2
	if h.singleTenant {
		parallelism = h.cpus / 2
	}
	if parallelism > 1 {
		return parallelism
	}
	return 1
}

func boundedGoCommand(h hostShape, verb, args string) string {
	return goCommandAt(goStepParallelism(h), verb, args)
}

func goCommandAt(parallelism int, verb, args string) string {
	return goCommandWithLimits(parallelism, parallelism, verb, args)
}

func goCommandWithLimits(gomaxprocs, packageParallelism int, verb, args string) string {
	return fmt.Sprintf("GOMAXPROCS=%d go %s -p %d %s", gomaxprocs, verb, packageParallelism, args)
}

// safety: CollectAll lets one heavy failure finish beside the others instead
// of cancelling them, so one run reports every failure. The cheap source
// policy runs before this job, in the policy job. Needs still require success,
// so a step whose prerequisite failed is still never run.
func (p *Gate) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.FailurePolicy(sparkwing.CollectAll)
	vet, build := runVet, runBuild
	if p.mode == gateAffected {
		vet, build = runAffectedVet, runAffectedBuild
	}
	vetStep := sparkwing.Step(w, "vet", vet)
	buildStep := sparkwing.Step(w, "build", build).Needs(vetStep)
	sparkwing.Step(w, "vet-qatrust", runQATrustVetTouched).Needs(vetStep)
	// safety: a Gate that Plan never configured runs every test, so a missing
	// mode can only widen the gate.
	test := runTest
	if p.mode == gateAffected {
		test = runAffectedTest
	}
	// safety: tests/acceptance drives the dashboard client through web/node_modules.
	frontendDeps := sparkwing.Step(w, "frontend-deps", runFrontendDeps)
	testStep := sparkwing.Step(w, "test", test).Needs(buildStep, frontendDeps)
	lint := runGolangciLint
	if p.mode == gateAffected {
		lint = runGolangciLintTouched
	}
	lintStep := sparkwing.Step(w, "lint", lint)
	sparkwing.Step(w, "store-postgres", func(ctx context.Context) error { return runStorePostgresIfTouched(ctx, p.mode) }).Needs(testStep)
	frontendUnit := sparkwing.Step(w, "frontend-unit", runFrontendUnit).Needs(frontendDeps)
	frontendLint := sparkwing.Step(w, "frontend-lint", runFrontendLint).Needs(frontendDeps)
	frontendBuild := sparkwing.Step(w, "frontend-build", runFrontendBuild).Needs(frontendUnit, frontendLint)
	frontendBrowser := sparkwing.Step(w, "frontend-browser", runFrontendBrowser).Needs(frontendBuild)
	if p.mode == gateAffected {
		frontendUnit.SkipIf(skipFrontendUnlessTouched(false))
		frontendLint.SkipIf(skipFrontendUnlessTouched(false))
		frontendBuild.SkipIf(skipFrontendUnlessTouched(true))
		frontendBrowser.SkipIf(skipFrontendUnlessTouched(true))
	}
	sdkStep := sparkwing.Step(w, "sdk-typescript", runTypeScriptSDK)
	if p.mode == gateAffected {
		sdkStep.SkipIf(skipTypeScriptSDKUnlessTouched)
		vetStep.SkipIf(skipUnlessGoTouched("vet"))
		buildStep.SkipIf(skipUnlessGoTouched("build"))
		lintStep.SkipIf(skipUnlessGoTouched("lint"))
		frontendDeps.SkipIf(skipFrontendDepsUnlessTouched)
	}

	// safety: the job succeeds only when every step does, so this output says the
	// gate ran; a job its proven lookup skipped writes none.
	return sparkwing.Step(w, "ran", func(context.Context) (bool, error) { return true, nil }), nil
}

// safety: npm ci installs exactly the lockfile, so the typecheck judges the
// package with the compiler it pins; node --test alone strips types unchecked.
func runTypeScriptSDK(ctx context.Context) error {
	const sdk = "npm --prefix sdk/typescript"
	if _, err := sparkwing.Bash(ctx, sdk+" ci --ignore-scripts --no-audit --no-fund && "+sdk+" run typecheck && "+sdk+" test").Run(); err != nil {
		return fmt.Errorf("TypeScript SDK suite: %w", err)
	}
	return nil
}

const frontendInstall = "pnpm --dir web install --frozen-lockfile --prefer-offline --ignore-scripts"

// safety: a checkout without web/node_modules failed the frontend steps with
// exit 127 and no remedy. The frozen install matches hosted CI's and is a fast
// no-op when the dependencies already match the lockfile.
func runFrontendDeps(ctx context.Context) error {
	if _, err := exec.LookPath("pnpm"); err != nil {
		if !frontendDepsMissing() {
			sparkwing.Warn(ctx, "pnpm is not on PATH, so web/node_modules was not checked against the lockfile")
			return nil
		}
		return fmt.Errorf("frontend dependencies: web/node_modules is missing and pnpm is not on PATH; install pnpm (corepack enable pnpm), then run: %s", frontendInstall)
	}
	if _, err := sparkwing.Bash(ctx, frontendInstall).Run(); err != nil {
		return fmt.Errorf("frontend dependencies: %w\nrun: %s", err, frontendInstall)
	}
	return nil
}

func frontendDepsMissing() bool {
	_, err := os.Stat(filepath.Join(sparkwing.WorkDir(), "web", "node_modules"))
	return err != nil
}

func ensureFrontendDeps(ctx context.Context) error {
	if !frontendDepsMissing() {
		return nil
	}
	return runFrontendDeps(ctx)
}

func skipFrontendDepsUnlessTouched(ctx context.Context) bool {
	if frontendDepsMissing() {
		sparkwing.Info(ctx, "frontend dependencies: installing, because web/node_modules is missing")
		return false
	}
	return skipFrontendUnlessTouched(true)(ctx)
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
	return runScopedChecker(ctx, "comments", commentCheckCommand)
}

func checkTestSleeps(ctx context.Context) error {
	return runScopedChecker(ctx, "test-sleeps", sleepCheckCommand)
}

func runScopedChecker(ctx context.Context, step string, plan func(context.Context) (string, string, error)) error {
	command, scope, err := plan(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "%s: %s", step, scope)
	_, err = sparkwing.Bash(ctx, command).Env("GOWORK", "off").Run()
	return err
}

func commentCheckCommand(ctx context.Context) (command, scope string, err error) {
	return scopedCheckerCommand(ctx, "commentcheck", "Go file", existingGoFiles)
}

func sleepCheckCommand(ctx context.Context) (command, scope string, err error) {
	return scopedCheckerCommand(ctx, "sleepcheck", "test file", existingGoTestFiles)
}

func scopedCheckerCommand(ctx context.Context, tool, noun string, keep func([]string) []string) (command, scope string, err error) {
	// safety: -staged gates an empty diff on a branch whose work is already
	// committed, so the step passes locally and the hosted gate, which stages
	// the whole push, fails the same findings after the fast-forward.
	staged, err := stagedNames(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list the staged change: %w", err)
	}
	if len(staged) > 0 {
		return fmt.Sprintf("go run ./internal/%s -staged .", tool),
			fmt.Sprintf("%d staged %s(s)", len(keep(staged)), noun), nil
	}
	base, baseline, err := resolveGateBase(ctx)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("go run ./internal/%s -base %s .", tool, base),
		fmt.Sprintf("nothing staged, so %s and every untracked %s", baseline, noun), nil
}

type scopeFunc func(ctx context.Context, noun string, keep func([]string) []string) ([]string, string, error)

// safety: git hands a pre-push hook no index of its own, so a file left
// staged would narrow every step to that file and push the commits unjudged.
// The push tier reads the range; changeScope, which reads the index, is what
// a commit is judged by.
func pushRangeScope(ctx context.Context, noun string, keep func([]string) []string) ([]string, string, error) {
	base, baseline, err := resolveGateBase(ctx)
	if err != nil {
		return nil, "", err
	}
	changed, err := listNames(ctx, "diff -z --name-only --diff-filter=ACMR "+base+" HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("list the push range %s..HEAD: %w", base, err)
	}
	files := keep(changed)
	return files, fmt.Sprintf("%d %s in %s", len(files), noun, baseline), nil
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
			"internal/paths/paths.go":           "owns the resolution, and with it the test-sandbox redirect every other caller inherits",
			"internal/storage/storeurl/spec.go": "storage URL configuration carries its own test-sandbox redirect",
		},
		advice: "Call internal/paths.DefaultPaths() instead, which honors SPARKWING_HOME the same way and adds the test-sandbox redirect that keeps a test binary out of the developer's real ~/.sparkwing.",
	},
	{
		label:   "build the sparkwing home from a home directory",
		pattern: homeDirJoin,
		allowed: map[string]string{
			"internal/paths/paths.go":             "owns the resolution, and with it the test-sandbox redirect every other caller inherits",
			"internal/storage/storeurl/spec.go":   "storage URL configuration carries its own test-sandbox redirect",
			"internal/configguard/configguard.go": "watches the real user's home for writes a suite should not have made, so resolving anywhere else would measure the wrong directory; its package doc states this",
			"internal/testleak/testleak.go":       "lays out the sandbox home a test binary runs in, before internal/paths has a home to resolve",
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
	return gofmtOverScope(ctx, treeGoFiles)
}

// bug: sdk-typescript runs npm ci beside this step, and a walk into the
// node_modules it rewrites failed on entries vanishing mid-readdir. Vendored
// JavaScript holds no Go of ours, so the walk never enters it.
func treeGoFiles(_ context.Context, noun string, keep func([]string) []string) ([]string, string, error) {
	root := regexCheckRoot()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("list the tree's Go files: %w", err)
	}
	files = keep(files)
	return files, fmt.Sprintf("%d %s in the tree, node_modules excluded", len(files), noun), nil
}

func runFormatters(ctx context.Context) error {
	return formattersOverScope(ctx, changeScope)
}

func formattersOverScope(ctx context.Context, scopeOf scopeFunc) error {
	files, scope, err := scopeOf(ctx, "Go file(s)", existingGoFiles)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "formatters: %s", scope)
	if len(files) == 0 {
		return nil
	}
	var diffs []string
	for _, batch := range argBatches(files) {
		_, runErr := sparkwing.Exec(ctx, "golangci-lint", append([]string{"fmt", "--diff", "--"}, batch...)...).Capture()
		if runErr == nil {
			continue
		}
		var execErr *sparkwing.ExecError
		if !errors.As(runErr, &execErr) || strings.TrimSpace(execErr.Stdout) == "" {
			return fmt.Errorf("golangci-lint fmt: %w", runErr)
		}
		diffs = append(diffs, strings.TrimSpace(execErr.Stdout))
	}
	if len(diffs) > 0 {
		return fmt.Errorf("%s do not match the configured formatters; run `golangci-lint fmt -- <file>...` on the files this diff names:\n%s",
			scope, strings.Join(diffs, "\n"))
	}
	return nil
}

// perf: a change of any size is split into command lines of at most this
// many bytes, under Linux's 128 KiB limit on one argument and the 32 KiB
// Windows command line.
const argBatchBytes = 24 << 10

func argBatches(files []string) [][]string {
	var batches [][]string
	var batch []string
	size := 0
	for _, f := range files {
		if len(batch) > 0 && size+len(f)+1 > argBatchBytes {
			batches = append(batches, batch)
			batch, size = nil, 0
		}
		batch = append(batch, f)
		size += len(f) + 1
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	return batches
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
	base, baseline, err := resolveGateBase(ctx)
	if err != nil {
		return nil, "", err
	}
	changed, err := listNames(ctx, "diff -z --name-only --diff-filter=ACMR "+base)
	if err != nil {
		return nil, "", fmt.Errorf("list the change since %s: %w", base, err)
	}
	files := keep(changed)
	return files, fmt.Sprintf("nothing staged, so %d %s in %s", len(files), noun, baseline), nil
}

// safety: a full pass lets other runs skip, so its scope must not depend on the
// producer's baseline. Like a Gate that Plan never configured, a missing mode widens.
func gateScope(ctx context.Context, mode gateMode, noun string, keep func([]string) []string) ([]string, string, error) {
	if mode == gateAffected {
		return changeScope(ctx, noun, keep)
	}
	tracked, err := listNames(ctx, "ls-files -z")
	if err != nil {
		return nil, "", fmt.Errorf("list the tracked files: %w", err)
	}
	files := keep(tracked)
	return files, fmt.Sprintf("full mode, so all %d tracked %s", len(files), noun), nil
}

func resolveGateBase(ctx context.Context) (base, scope string, err error) {
	sha, err := sparkwing.Bash(ctx, "git merge-base "+gateBaselineRef+" HEAD").String()
	sha = strings.TrimSpace(sha)
	if err == nil && sha != "" {
		return sha, fmt.Sprintf("change since %s (%s)", gateBaselineRef, sha[:12]), nil
	}
	// safety: an uploaded snapshot has no ancestry, but an explicitly fetched baseline still bounds its diff.
	sha, err = sparkwing.Exec(ctx, "git", "rev-parse", "--verify", gateBaselineRef+"^{commit}").String()
	if err == nil && sha != "" {
		return sha, fmt.Sprintf("snapshot change against %s (%s)", gateBaselineRef, sha[:12]), nil
	}
	// safety: uploaded snapshots may have no remote refs or parent history.
	// An empty tree charges every tracked file to the gate without fetching.
	sha, err = sparkwing.Bash(ctx, "git hash-object -w -t tree --stdin </dev/null").String()
	sha = strings.TrimSpace(sha)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve %s or create an empty-tree baseline: %w", gateBaselineRef, err)
	}
	if sha == "" {
		return "", "", fmt.Errorf("empty-tree baseline returned no object for unavailable %s", gateBaselineRef)
	}
	return sha, fmt.Sprintf("all tracked files (%s unavailable; empty tree %s)", gateBaselineRef, sha), nil
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

func existingGoTestFiles(all []string) []string {
	out := make([]string, 0, len(all))
	for _, f := range existingGoFiles(all) {
		if strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
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
// internal/procenv's composeRuntime is the injector these names track; the
// service and token names come from an operator's shell rather than from it.
var productTestUnset = []string{
	"GIT_INDEX_FILE",
	"SPARKWING_AGENT_TOKEN",
	"SPARKWING_CACHE_URL",
	"SPARKWING_CONTROLLER_URL",
	"SPARKWING_DRY_RUN",
	"SPARKWING_GATE_INDEX",
	"SPARKWING_JOB_ID",
	"SPARKWING_NODE_ID",
	"SPARKWING_NODE_TOKEN",
	"SPARKWING_NODE_URL",
	"SPARKWING_RUN_ID",
}

// safety: the injected names a suite may keep, each because it names no
// service and carries no credential. A name absent from both this map and the
// list above fails the contract test rather than reaching a suite unread.
var productTestKept = map[string]string{
	"SPARKWING_RUNNER_NAME":    "reports which runner executed the node",
	"SPARKWING_RUNNER_TYPE":    "reports which runner executed the node",
	"SPARKWING_RUNNER_LABELS":  "reports what the local runner advertises",
	"SPARKWING_BRANCH":         "describes the run's source and no product code reads it",
	"SPARKWING_COMMIT":         "describes the run's source and no product code reads it",
	"SPARKWING_REPO":           "describes the run's source; the installer reads SPARKWING_RELEASE_REPO instead",
	"SPARKWING_TRIGGER_SOURCE": "describes the run's source and no product code reads it",
	"SPARKWING_ENGINE_VERSION": "names the engine that runs the node and no product code reads it",
	"CI":                       "hosted runners set it for every suite anyway",
}

// safety: a fresh home also hides the dev.env the local dashboard writes, which
// a CLI verb would otherwise read for the dashboard's address.
const productTestHomeVar = "SPARKWING_HOME"

// safety: go test never hashes a file outside the module it tests, and the
// pipeline module's tests read .github/, bin/ and the root guides above it, so
// a cached pass would survive a change to them. The checkout's state travels
// as a variable those tests read, so only a rerun of the same tree replays.
const productTestRepoStateVar = "SPARKWING_TEST_REPO_STATE"

// safety: a checkout without commits has no HEAD, so git's complaints are
// dropped rather than mixed into the step's failure output. Untracked files
// count by name and content, because an edit to one is a change too.
const productTestRepoState = `"$(git rev-parse -q --verify 'HEAD^{tree}')-$(git diff HEAD 2>/dev/null | git hash-object --stdin)-$({ git ls-files -o --exclude-standard; git ls-files -o --exclude-standard -z | xargs -0 -r git hash-object --; } 2>/dev/null | git hash-object --stdin)"`

func productTestPins(home string) []string {
	return []string{productTestHomeVar + "=" + shellQuote(home), productTestRepoStateVar + "=" + productTestRepoState}
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

// perf: go test replays a pass only while the environment its tests read
// repeats, so a fresh home per run reran every package reading SPARKWING_HOME.
// A lock beside the home covers creation, execution and cleanup, so overlapping
// runs of the same step cannot remove each other's state.
func withStableProductTestHome(step string, run func(home string) error) error {
	sum := sha256.Sum256([]byte(regexCheckRoot()))
	home := filepath.Join(os.TempDir(), fmt.Sprintf("sparkwing-gate-home-%s-%x", step, sum[:6]))
	lock, err := os.OpenFile(home+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open the suite's home lock: %w", err)
	}
	defer lock.Close()
	if err := lockExclusiveFile(lock); err != nil {
		return fmt.Errorf("lock the suite's sparkwing home: %w", err)
	}
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("empty the suite's sparkwing home: %w", err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
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
	return forEachGoModule(ctx, "go vet", boundedGoCommand(currentHost(), "vet", "./..."), "")
}

func runBuild(ctx context.Context) error {
	return forEachGoModule(ctx, "go build", boundedGoCommand(currentHost(), "build", "./..."), "")
}

func runTest(ctx context.Context) error {
	return withStableProductTestHome("gate-test", func(home string) error {
		return forEachGoModule(ctx, "go test", testGoCommand(currentHost()), home)
	})
}

// perf: short mode defers expensive test bodies while retaining real-store
// fixture setup, whose aggregate cost still takes minutes.
func runShortTest(ctx context.Context) error {
	return withStableProductTestHome("short-test", func(home string) error {
		return forEachGoModule(ctx, "go test", boundedGoCommand(currentHost(), "test", "-short ./..."), home)
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
	var timings *goTestTimings
	if label == "go test" {
		timings = &goTestTimings{}
		defer timings.report(ctx, time.Now())
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
		if err := runModuleCommand(ctx, script, timings); err != nil {
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

// safety: the summary is bounded from its head, so the test failures go first
// or a hosted runner keeps nothing but the module that failed.
func describeModuleFailure(dir string, err error) string {
	named := failedTestSummaries(err)
	if len(named) == 0 {
		return fmt.Sprintf("%s: %v", dir, err)
	}
	return fmt.Sprintf("%s: %s\n%v", dir, strings.Join(named, "\n"), err)
}

func failedTestSummaries(err error) []string {
	var execErr *sparkwing.ExecError
	if !errors.As(err, &execErr) {
		return nil
	}
	var out []string
	named := 0
	truncated := false
	wantReason := false
	for _, line := range strings.Split(execErr.Stdout+"\n"+execErr.Stderr, "\n") {
		line = strings.TrimRight(line, "\r")
		isTest := strings.HasPrefix(line, "--- FAIL: ")
		isPackage := strings.HasPrefix(line, "FAIL\t")
		if !isTest && !isPackage {
			if wantReason && strings.HasPrefix(line, "    ") && !strings.Contains(line, "--- FAIL: ") {
				out = append(out, line)
				wantReason = false
			}
			continue
		}
		if named == maxNamedTestFailures {
			truncated = true
			break
		}
		out = append(out, line)
		named++
		wantReason = isTest
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
	sparkwing.Register("Gate", func() sparkwing.Pipeline[GateArgs] { return &Gate{} })
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
