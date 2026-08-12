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

// PreCommit provides broad local verification with deterministic checks. gofmt,
// vet, build, test and lint cover every committed Go module -- the repo
// root, where the SDK, CLI and server live, as well as .sparkwing/ -- so a
// a selected run catches product code that does not compile, does not
// pass its tests, or trips the blessed linter set; the formatters step
// holds the staged Go files to gofumpt and goimports, which `golangci-lint
// run` does not check; the two regex sweeps cover the staged change for
// em dashes and internal tracker IDs (IMP-, SDK-, LOCAL-, RUN-, ORG-,
// REG-, TOD-); the docs-mirror check runs `bin/sync-docs.sh --check` and
// fails when docs/ or CHANGELOG.md (the sources) and pkg/docs/ (the
// embedded copies) have drifted, so an edit to either source can't be
// committed without re-running bin/sync-docs.sh; the comment
// check fails when the staged diff adds a comment the policy disallows;
// the home-resolution check fails when product code resolves the
// sparkwing home itself -- reading SPARKWING_HOME, or building the path
// from a home directory -- instead of through
// internal/paths.DefaultPaths.
//
// The repository keeps this pipeline manual so the lead can select it when the
// changed boundary warrants broad verification.
type PreCommit struct{ sparkwing.Base }

func (PreCommit) ShortHelp() string {
	return "Broad local verification: format, vet, build, test, lint, em-dash + tracker-ID sweeps, docs-mirror sync, comment policy, home resolution"
}

func (PreCommit) Help() string {
	return "Runs gofmt over the tree and go vet / go build / go test / golangci-lint in every committed Go module (today the repo root and .sparkwing/), plus checks on the staged change: the configured formatters (gofumpt + goimports), no em dashes, no internal tracker IDs (IMP-/SDK-/LOCAL-/RUN-/ORG-/REG-/TOD-), no disallowed comments (only godoc on declarations and // hack:/safety:/bug:/perf: tags), and repo-wide, that the embedded pkg/docs/ copies match the docs/ and CHANGELOG.md sources (via `bin/sync-docs.sh --check`; run bin/sync-docs.sh without the flag if it drifted) and that no product file resolves the sparkwing home itself, by reading SPARKWING_HOME or by joining a home directory with .sparkwing, instead of through internal/paths.DefaultPaths. The lint step names the modules it covered and the baseline it judged against. Set SPARKWING_REGEX_SWEEP_ALL=1 to sweep the whole tree for em dashes and tracker IDs."
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

// preCommitCPUReservation gives the gate half the host so admission can run
// another gate beside it without oversubscribing the machine.
func preCommitCPUReservation(cpuCount int) int {
	if cpuCount < 2 {
		return 1
	}
	return (cpuCount + 1) / 2
}

// boundedGoCommand leaves one reserved core for the pipeline runner and its
// lightweight parallel checks while bounding the Go tool's worker fanout.
func boundedGoCommand(cpuCount int, verb, args string) string {
	parallelism := preCommitCPUReservation(cpuCount) - 1
	if parallelism < 1 {
		parallelism = 1
	}
	return fmt.Sprintf("GOMAXPROCS=%d go %s -p %d %s", parallelism, verb, parallelism, args)
}

// Work orders the chain cheapest-first, each step waiting on the one
// before it, so the first failure stops the run and the author waits on
// the cheapest verdict a broken tree can produce rather than on the
// whole suite. Measured on this repo, warm: gofmt 0.2s, formatters 0.2s,
// vet 1.7s, build 24s, test 88s, lint 2.6s.
//
// lint sits last against its warm cost because the cost that decides the
// order is the cold one: a golangci-lint cache the box has not filled
// yet takes 81s on the root module and 26s on .sparkwing/, which is the
// same tier as test. It also needs a tree that compiles, which is what
// build and test establish.
//
// The five sweeps stay parallel. Nothing downstream waits on them and
// each finishes in well under a second (docs-mirror 0.02s,
// home-resolution 0.15s, comments 0.5s), so ordering them would only
// delay their verdict without saving any work.
func (p *PreCommit) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.ParallelFailures(sparkwing.FailFast)
	gofmtStep := sparkwing.Step(w, "gofmt", runGofmt)
	formattersStep := sparkwing.Step(w, "formatters", runFormatters).Needs(gofmtStep)
	vetStep := sparkwing.Step(w, "vet", runVet).Needs(formattersStep)
	buildStep := sparkwing.Step(w, "build", runBuild).Needs(vetStep)
	testStep := sparkwing.Step(w, "test", runTest).Needs(buildStep)
	sparkwing.Step(w, "lint", runGolangciLint).Needs(testStep)
	sparkwing.Step(w, "em-dashes", checkEmDashes)
	sparkwing.Step(w, "tracker-ids", checkTrackerIDs)
	sparkwing.Step(w, "docs-mirror", checkDocsMirror)
	sparkwing.Step(w, "comments", checkComments)
	sparkwing.Step(w, "home-resolution", checkHomeResolution)
	return nil, nil
}

// checkComments fails when the staged diff introduces a comment the repo
// policy disallows -- anything that isn't godoc on a top-level declaration or
// a // hack:/safety:/bug:/perf: tag. Scoped to the staged diff so the
// pre-existing comment corpus is never charged to a new commit; run
// `go run ./internal/commentcheck .` for a whole-tree audit.
func checkComments(ctx context.Context) error {
	_, err := sparkwing.Bash(ctx, `go run ./internal/commentcheck -staged .`).Run()
	return err
}

// homeEnvRead matches a direct read of the SPARKWING_HOME variable,
// with or without the os. qualifier. Setting it is not a read:
// t.Setenv("SPARKWING_HOME", ...) is the isolation this rule exists to
// make reliable, so the pattern deliberately does not match it.
var homeEnvRead = regexp.MustCompile(`(?:os\.)?(?:Getenv|LookupEnv)\(\s*"SPARKWING_HOME"\s*\)`)

// homeDirJoin matches the other half of the same mistake: building the
// home path out of a home directory instead of asking for it. Reading
// the environment is only one way in, and internal/bincache used both --
// SPARKWING_HOME first, then filepath.Join(home, ".sparkwing") when it
// was unset.
//
// The first Join argument has to look like a home for this to fire,
// which is what separates it from the ~26 legitimate joins of the same
// literal. Those build the per-repo .sparkwing/ pipeline module
// directory from a checkout path -- cwd, root, repoDir, absPath,
// workTree -- and are a completely different thing that happens to share
// a name. No accident-catching regex can tell the two apart by the
// literal alone, so this one keys on the argument, and the naming
// convention it depends on is the one every home resolution in the tree
// already follows.
//
// The limit that buys: a home held in a variable named something else
// slips through. That is the honest trade for a gate that does not cry
// wolf on two dozen correct call sites, and the env rule above covers
// the path such code would most likely take to obtain the home anyway.
var homeDirJoin = regexp.MustCompile(`filepath\.Join\([^,)]*[Hh]ome[^,)]*,\s*"\.sparkwing"`)

// homeRule is one forbidden way to arrive at the sparkwing home, the
// files allowed to use it anyway, and the advice an offender gets.
type homeRule struct {
	label   string
	pattern *regexp.Regexp
	allowed map[string]string
	advice  string
}

// homeRules are the two shapes a bypass takes. Each carries its own
// allowlist because the exemptions are not the same: configguard has to
// build a home path from a directory it was handed, and must never read
// the environment to do it.
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

// checkHomeResolution fails when product code arrives at the sparkwing
// home on its own instead of asking internal/paths.
//
// The rule has teeth because the bypass is invisible when it happens: a
// package that resolves the home itself behaves identically to the
// canonical resolution for every real run and differs only inside a test
// binary, where it silently returns the developer's own ~/.sparkwing.
// internal/bincache did exactly this and its suites compiled pipeline
// binaries into the real cache; once the LRU prune landed on the same
// resolution, they could evict from it too.
//
// Swept whole-tree rather than over the staged change, which the em-dash
// and tracker-ID sweeps cannot afford: the tree satisfies these rules
// today, with the allowlisted files and nothing else, so a whole-tree
// sweep has no pre-existing corpus to charge an unrelated commit for. It
// is also the only scope that makes this an invariant instead of a diff
// filter, since a violation moved between files by a refactor would slip
// past a staged-only check.
//
// Comment lines are skipped, so prose may quote a forbidden call without
// the gate refusing the file. A comment cannot resolve a home, and a
// rule that cannot be written about is one nobody can document.
//
// _test.go files are exempt. A test that reads the variable is asserting
// something about it rather than resolving a home from it, and pointing
// such a test at internal/paths would defeat what it is checking.
//
// .sparkwing/ is exempt because it is a separate module
// (sparkwing-pipelines) and Go's internal rule bars it from importing
// internal/paths at all, so the fix this gate names is not available
// there.
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

// strippedGoComments blanks out // comment lines so a rule cannot be
// tripped by prose that quotes the call it forbids. Line count is
// preserved but content is not read; the patterns match within a line,
// so nothing else needs to survive.
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

// runFormatters fails when a staged Go file does not match the formatters
// .golangci.yml configures -- gofumpt with extra rules and goimports with
// this repo's local prefix. `golangci-lint run` never reads that block: in
// v2 the formatters apply only under the `fmt` subcommand, so the config
// was decorative from the day it landed and gofmt, a strict subset of
// gofumpt, was the only formatting the gate enforced.
//
// Scoped to the staged Go files, for cost. `golangci-lint fmt` has no
// cache, so it re-reads whatever it is pointed at every time it runs:
// measured on a clean tree, 37.5s and 38.3s over `./...` against 0.28s
// over the five files a commit typically stages. That is a third of this
// chain's runtime for a check that answers in a quarter of a second.
//
// The scope also forgives exactly the code the change did not touch and
// stops forgiving it the moment somebody touches it, which is the bargain
// issues.new-from-merge-base strikes for the linters, at file rather than
// line granularity. That property is why re-drift cannot accumulate into
// something that blocks an unrelated commit.
//
// The tree is fully formatted as of the 20-file cleanup, so a whole-tree
// check would pass today and the cost is the only live argument for
// staged scope. It was not always the only one, and it will not be if the
// tree drifts again, which is worth knowing before anyone widens this on
// the grounds that it is currently green. `golangci-lint fmt ./...` is the
// whole-tree audit, off the critical path of an unrelated commit.
func runFormatters(ctx context.Context) error {
	files, err := stagedGoFiles(ctx)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(files))
	for _, f := range files {
		quoted = append(quoted, fmt.Sprintf("%q", f))
	}
	sparkwing.Info(ctx, "formatters: checking %d staged Go file(s)", len(files))

	_, runErr := sparkwing.Bash(ctx, "golangci-lint fmt --diff "+strings.Join(quoted, " ")).Capture()
	if runErr == nil {
		return nil
	}
	var execErr *sparkwing.ExecError
	if errors.As(runErr, &execErr) && strings.TrimSpace(execErr.Stdout) != "" {
		return fmt.Errorf("staged files do not match the configured formatters; run `golangci-lint fmt %s`:\n%s",
			strings.Join(files, " "), strings.TrimSpace(execErr.Stdout))
	}
	return fmt.Errorf("golangci-lint fmt: %w", runErr)
}

// stagedGoFiles returns the Go files the commit adds or changes, as paths
// relative to the repo root. Deletions are excluded because there is no
// content left to format, and web/node_modules is excluded because
// third-party Go arriving through npm is not this repo's to format.
func stagedGoFiles(ctx context.Context) ([]string, error) {
	all, err := sparkwing.Bash(ctx, `git diff --cached --name-only --diff-filter=ACMR`).Lines()
	if err != nil {
		return nil, fmt.Errorf("list the staged change: %w", err)
	}
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
	return out, nil
}

// checkDocsMirror fails when the embedded pkg/docs/mirror/ or
// pkg/docs/changelog.md has drifted from its canonical source. Read-only
// (a recursive diff, no mutation) so it's safe to run alongside the other
// parallel steps. The fix is `bash bin/sync-docs.sh && git add pkg/docs`.
//
// It asks the sync script itself rather than re-implementing the
// comparison, so the gate and the script can never disagree about what
// "synced" means -- and so the script's check mode is exercised on every
// gate run instead of only when someone remembers it exists.
func checkDocsMirror(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/sync-docs.sh --check").Run(); err != nil {
		return fmt.Errorf("the embedded docs mirror is out of sync; run `bash bin/sync-docs.sh && git add pkg/docs` (edit docs/ and CHANGELOG.md, never the mirror): %w", err)
	}
	return nil
}

// productTestUnset names the bindings this gate's own run exports that the
// suites it runs must not inherit. The gate is a sparkwing run, so a suite
// that launches one attaches to the gate's admission lease instead of
// admitting on its own and fails with "attach to parent lease"; the gate is
// also a git run, and `sparkwing run --sw-index` binds GIT_INDEX_FILE, so
// every git a suite spawns reads and writes the gate's index rather than its
// own fixture's. Dropping them makes a suite behave exactly as it does when
// run by hand outside a pipeline.
var productTestUnset = []string{
	wingwire.LeaseTokenEnv,
	wingwire.ChildLeaseTokenEnv,
	"GIT_INDEX_FILE",
}

// withoutInherited prefixes cmd with a shell unset of names, which removes
// them from the environment cmd's children see. Emptying them instead is not
// the same thing and is worse for GIT_INDEX_FILE, which git acts on: an empty
// value makes `git add` fail with "unable to write new index file" and
// `git status` report every tracked file as deleted.
func withoutInherited(cmd string, names []string) string {
	if len(names) == 0 {
		return cmd
	}
	return "unset " + strings.Join(names, " ") + "; " + cmd
}

// Checking only .sparkwing/ would prove nothing: sparkwing compiles the
// pipeline module before it can run this gate at all, so a pipeline-scoped
// step re-reports a prerequisite of the run as a result of it.
func runVet(ctx context.Context) error {
	return forEachGoModule(ctx, "go vet", boundedGoCommand(runtime.NumCPU(), "vet", "./..."), nil)
}

func runBuild(ctx context.Context) error {
	return forEachGoModule(ctx, "go build", boundedGoCommand(runtime.NumCPU(), "build", "./..."), nil)
}

func runTest(ctx context.Context) error {
	return forEachGoModule(ctx, "go test", boundedGoCommand(runtime.NumCPU(), "test", "./..."), productTestUnset)
}

// forEachGoModule runs cmd in every committed module directory that holds
// buildable packages, with the variables in unset dropped, and reports all
// failures, so one broken module does not hide another's verdict.
func forEachGoModule(ctx context.Context, label, cmd string, unset []string) error {
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
		if _, err := sparkwing.Bash(ctx, script).Run(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", dir, err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s failed in %d module(s):\n  - %s",
		label, len(failures), strings.Join(failures, "\n  - "))
}

// moduleHasNoPackages reports whether dir's module holds nothing to build --
// a committed go.mod whose packages are all behind build tags, or none yet.
// `go vet` and `go test` exit 1 on such a module ("no packages to vet") while
// `go build` exits 0, so walking into it would red two steps for a reason no
// change to the author's tree can clear.
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
	files, err := regexCheckFiles(ctx)
	if err != nil {
		return err
	}
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
	files, err := regexCheckFiles(ctx)
	if err != nil {
		return err
	}
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

// regexCheckFiles returns the file list the regex sweeps judge: the staged
// change, so a commit is never charged for content it did not touch. Swept
// whole-tree, these checks refuse a clean commit over history it never went
// near, and the only way past that is the bypass the gate exists to prevent.
// Deletions are excluded because there is no content left to read. tickets/
// and archive/ are exempt because historical content is allowed to carry
// whatever style it was written with.
//
// Set SPARKWING_REGEX_SWEEP_ALL=1 for the whole-tree audit, which is how
// pre-existing drift gets found without blocking an unrelated commit.
func regexCheckFiles(ctx context.Context) ([]string, error) {
	list := `git diff --cached --name-only --diff-filter=ACMR`
	if os.Getenv("SPARKWING_REGEX_SWEEP_ALL") != "" {
		list = "git ls-files"
	}
	all, err := sparkwing.Bash(ctx, list).Lines()
	if err != nil {
		return nil, err
	}
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
	return out, nil
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
