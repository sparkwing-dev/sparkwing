package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	markdownlintCommand = "npx --yes markdownlint-cli2@0.23.2"
	actionlintCommand   = "go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12"
	// safety: no --target-seconds, so a loaded builder records a slow
	// measurement instead of reddening the release lane.
	installToGreenCommand = "bash bin/install-to-green.sh --build --output json"
	// safety: the harness reserves this status for a module proxy it could
	// not reach, which measured nothing and is not a release defect.
	installToGreenUnavailable = 75
)

func measureInstallToGreen(jobContext context.Context) (string, bool, error) {
	result, err := sparkwing.Bash(jobContext, installToGreenCommand).Run()
	return installToGreenOutcome(result.Stdout, err)
}

func installToGreenOutcome(stdout string, err error) (string, bool, error) {
	if err == nil {
		return strings.TrimSpace(stdout), true, nil
	}
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) && execErr.ExitCode == installToGreenUnavailable {
		record := strings.TrimSpace(execErr.Stdout)
		if record == "" {
			record = strings.TrimSpace(execErr.Stderr)
		}
		return record, false, nil
	}
	return "", false, err
}

func runMarkdownlint(jobContext context.Context) error {
	_, err := sparkwing.Bash(jobContext, markdownlintCommand).Run()
	return err
}

func runActionlint(jobContext context.Context) error {
	_, err := sparkwing.Bash(jobContext, actionlintCommand).Run()
	return err
}

func runReleaseBinaryVulnerabilityScan(jobContext context.Context) (resultErr error) {
	scanDirectory, err := os.MkdirTemp("", "sparkwing-release-vulnerability-*")
	if err != nil {
		return fmt.Errorf("create release vulnerability scan directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(scanDirectory); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove release vulnerability scan directory: %w", err))
		}
	}()

	for _, binary := range publicBinaries {
		artifact := filepath.Join(scanDirectory, binary)
		if _, err := sparkwing.Exec(jobContext, "go", "build", "-trimpath", "-o", artifact, "./cmd/"+binary).
			Env("GOWORK", "off").Run(); err != nil {
			return fmt.Errorf("build release vulnerability artifact %s: %w", binary, err)
		}
		if _, err := sparkwing.Exec(jobContext, "bash", "bin/check-release-binary-vulnerabilities.sh", artifact).Run(); err != nil {
			return fmt.Errorf("scan release vulnerability artifact %s: %w", binary, err)
		}
	}
	return nil
}

type PreRelease struct {
	sparkwing.Base
}

func (PreRelease) ShortHelp() string {
	return "Release-boundary checks: race, chaos, vulnerabilities, dependencies, public interfaces, and infrastructure"
}

func (PreRelease) Help() string {
	return "Run lint, race tests, Postgres tests, admission fault tests, vulnerability scans, " +
		"dependency checks, public interface checks, Terraform checks, and workflow checks. " +
		"Committed Go modules must use released dependencies; the pipeline module may replace " +
		"the Sparkwing module with its parent checkout. Keep Go workspace files untracked. " +
		"On an attached branch, the gate updates a stale Sparkwing dependency pin, regenerates interface " +
		"snapshots, and commits those changes before the push. On a detached checkout, it leaves the pin " +
		"artifacts unchanged and reports stale versions. This is the release-boundary tier: the release " +
		"pipeline runs it, hosted CI runs it on every pull request and push to main, and nothing fires " +
		"it from a git hook. The broad " +
		"per-landing check is `gate`, and the source-policy check a commit passes is `pre-commit`."
}

func (PreRelease) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run release checks", Command: "sparkwing run pre-release"},
	}
}

func (preRelease *PreRelease) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, runContext.Pipeline, preRelease)
	return nil
}

type preReleaseCheck struct {
	id  string
	run func(context.Context) error
}

func (preRelease *PreRelease) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	addPreReleaseChecks(work, preReleaseChecks())
	return nil, nil
}

func addPreReleaseChecks(work *sparkwing.Work, checks []preReleaseCheck) {
	var previous *sparkwing.WorkStep
	for _, check := range checks {
		step := sparkwing.Step(work, check.id, check.run).ContinueOnError()
		if previous != nil {
			step.Needs(previous)
		}
		previous = step
	}
}

func preReleaseChecks() []preReleaseCheck {
	return []preReleaseCheck{
		{id: "no-replace", run: checkNoReplaceDirectivesInCommittedGoMods},
		{id: "no-go-work", run: checkNoCommittedGoWorkFiles},
		{id: "module-tidy", run: checkPreReleaseModuleTidy},
		{id: "sparkwing-pin", run: checkPreReleaseSparkwingPin},
		{id: "version-freshness", run: func(ctx context.Context) error {
			return CheckVersionsFreshness(ctx, sparkwing.WorkDir())
		}},
		{id: "pre-v1-policy", run: func(ctx context.Context) error {
			return CheckPreV1Policy(ctx, sparkwing.WorkDir())
		}},
		{id: "gofmt", run: checkPreReleaseGofmt},
		{id: "lint", run: runGolangciLint},
		{id: "race", run: preReleaseShell("go -C .sparkwing test -race ./...")},
		{id: "store-postgres", run: checkPreReleaseStorePostgres},
		{id: "chaos", run: preReleaseShell("go test -count=1 -run TestChaos_CI ./internal/chaos")},
		{id: "release-vulnerability", run: runReleaseBinaryVulnerabilityScan},
		{id: "shell-portability", run: preReleaseShell("bash bin/check-shell-test.sh")},
		{id: "hosted-mutation-guard", run: preReleaseShell("bash bin/check-hosted-gate-clean-test.sh")},
		{id: "vulnerability-script", run: preReleaseShell("bash bin/check-release-binary-vulnerabilities-test.sh")},
		{id: "schema-parity-script", run: preReleaseShell("bash bin/check-release-schema-parity-test.sh")},
		{id: "changelog-script", run: preReleaseShell("bash bin/check-changelog-test.sh")},
		{id: "installer-report", run: preReleaseShell("bash bin/install-test.sh")},
		{id: "service-installer", run: preReleaseShell("bash bin/service-install-test.sh")},
		{id: "release-installer", run: preReleaseShell("bash bin/release-install-test.sh")},
		{id: "install-to-green", run: checkPreReleaseInstallToGreen},
		{id: "shellcheck", run: preReleaseShell("bash bin/check-shell.sh")},
		{id: "terraform", run: preReleaseShell("bash bin/check-terraform-test.sh && bash bin/check-terraform.sh")},
		{id: "markdownlint", run: runMarkdownlint},
		{id: "actionlint", run: runActionlint},
		{id: "doc-examples", run: checkPreReleaseDocExamples},
		{id: "cli-reference", run: checkPreReleaseCLIReference},
		{id: "config-reference", run: checkPreReleaseConfigReference},
		{id: "sdk-reference", run: checkPreReleaseSDKReference},
		{id: "api-reference", run: checkPreReleaseAPIReference},
		{id: "openapi", run: checkPreReleaseOpenAPI},
		{id: "api-snapshot", run: checkPreReleaseAPISnapshot},
	}
}

func checkPreReleaseModuleTidy(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx,
		`go -C .sparkwing mod tidy 2>/dev/null || true; git diff --quiet -- .sparkwing/go.mod .sparkwing/go.sum`,
	).Run(); err != nil {
		return errors.New("go mod tidy drift: run `go -C .sparkwing mod tidy` and commit the result")
	}
	return nil
}

func checkPreReleaseSparkwingPin(ctx context.Context) error {
	bumpedTo, err := autoBumpSparkwingPinIfStale(ctx, sparkwing.WorkDir())
	if err != nil {
		return fmt.Errorf("auto-bump sparkwing pin: %w", err)
	}
	if bumpedTo != "" {
		sparkwing.Info(ctx, "sparkwing pin: auto-bumped to %s (commit added to push)", bumpedTo)
	}
	return nil
}

func checkPreReleaseGofmt(ctx context.Context) error {
	if err := sparkwing.Bash(ctx, `gofmt -l $(go list -f '{{.Dir}}' ./...)`).
		MustBeEmpty("gofmt reported unformatted files"); err != nil {
		return fmt.Errorf("gofmt: %w", err)
	}
	return nil
}

func checkPreReleaseStorePostgres(ctx context.Context) error {
	storeContext, cancelStore := context.WithTimeout(ctx, storePostgresPrePushTimeout)
	defer cancelStore()
	if err := (&StorePostgres{}).run(storeContext); err != nil {
		return fmt.Errorf("store postgres suite: %w", err)
	}
	return nil
}

func checkPreReleaseInstallToGreen(ctx context.Context) error {
	record, measured, err := measureInstallToGreen(ctx)
	if err != nil {
		return fmt.Errorf("install-to-green harness: %w", err)
	}
	if measured {
		sparkwing.Info(ctx, "install-to-green: %s", record)
	} else {
		sparkwing.Warn(ctx, "install-to-green: no measurement taken: %s", record)
	}
	return nil
}

func checkPreReleaseDocExamples(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" && go run ./internal/doccheck "$ROOT/docs" "$ROOT"`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		return fmt.Errorf("doc-examples: %w", err)
	}
	return nil
}

func checkPreReleaseCLIReference(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" &&
		TMP="$(mktemp -d)" &&
		trap 'rm -rf "$TMP"' EXIT &&
		go run ./cmd/sparkwing commands --format markdown --output plain --split-dir "$TMP" >/dev/null &&
		fail=0 &&
		for f in "$TMP"/*.md; do
			diff -u "docs/$(basename "$f")" "$f" || fail=1
		done;
		for f in docs/cli-*.md; do
			if [ ! -e "$TMP/$(basename "$f")" ] && head -n1 "$f" | grep -q "GENERATED from the CLI command registry"; then
				echo "stale generated page: $f"
				fail=1
			fi
		done;
		exit "$fail"`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		return errors.New("cli-reference: stale -- run `bash bin/gen-cli-docs.sh`")
	}
	return nil
}

func checkPreReleaseConfigReference(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" && go run ./internal/configref "$ROOT" | diff -u docs/config-reference.md -`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		return errors.New("config-reference: stale -- run `bash bin/gen-config-docs.sh`")
	}
	return nil
}

func checkPreReleaseSDKReference(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" &&
		TMP="$(mktemp -d)" &&
		trap 'rm -rf "$TMP"' EXIT &&
		go run ./internal/sdkref "$ROOT" "$TMP" >/dev/null &&
		fail=0 &&
		for f in "$TMP"/*.md; do
			diff -u "docs/$(basename "$f")" "$f" || fail=1
		done;
		for f in docs/sdk-*.md; do
			if [ ! -e "$TMP/$(basename "$f")" ] && head -n1 "$f" | grep -q "GENERATED from the .sparkwing. package via go/doc"; then
				echo "stale generated page: $f"
				fail=1
			fi
		done;
		exit "$fail"`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		return errors.New("sdk-reference: stale -- run `bash bin/gen-sdk-docs.sh`")
	}
	return nil
}

func checkPreReleaseAPIReference(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" && go run ./internal/apiref "$ROOT" | diff -u docs/api-reference.md -`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		return errors.New("api-reference: stale -- run `bash bin/gen-api-docs.sh`")
	}
	return nil
}

func checkPreReleaseOpenAPI(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-spec.sh").Run(); err != nil {
		return errors.New("openapi: stale -- run `bash bin/gen-api-docs.sh`")
	}
	return nil
}

func checkPreReleaseAPISnapshot(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-snapshot.sh").Run(); err != nil {
		return errors.New("api-snapshot: drift -- run `bash bin/regen-api-snapshot.sh` and commit .apidiff/")
	}
	return nil
}

func preReleaseShell(command string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := sparkwing.Bash(ctx, command).Run()
		return err
	}
}

func committedGoMods(jobContext context.Context) ([]string, error) {
	// safety: git -C anchors paths to repo root regardless of process cwd.
	output, err := sparkwing.Bash(jobContext,
		`git -C "$SPARKWING_WORKDIR" ls-files '*go.mod'`,
	).Env("SPARKWING_WORKDIR", sparkwing.Path()).String()
	if err != nil {
		return nil, fmt.Errorf("list go.mod files: %w", err)
	}
	var moduleFiles []string
	for _, relativePath := range strings.Split(strings.TrimSpace(output), "\n") {
		if relativePath != "" && !isTestdataPath(relativePath) {
			moduleFiles = append(moduleFiles, relativePath)
		}
	}
	return moduleFiles, nil
}

func isTestdataPath(relativePath string) bool {
	return strings.HasPrefix(relativePath, "testdata/") || strings.Contains(relativePath, "/testdata/")
}

func committedModuleDirs(jobContext context.Context) ([]string, error) {
	moduleFiles, err := committedGoMods(jobContext)
	if err != nil {
		return nil, err
	}
	moduleDirectories := make([]string, 0, len(moduleFiles))
	for _, moduleFile := range moduleFiles {
		moduleDirectories = append(moduleDirectories, filepath.Dir(moduleFile))
	}
	return moduleDirectories, nil
}

func checkNoReplaceDirectivesInCommittedGoMods(jobContext context.Context) error {
	moduleFiles, err := committedGoMods(jobContext)
	if err != nil {
		return err
	}
	var offenders []string
	for _, relativePath := range moduleFiles {
		absolutePath := sparkwing.Path(relativePath)
		contents, readError := os.ReadFile(absolutePath)
		if readError != nil {
			return fmt.Errorf("read %s: %w", relativePath, readError)
		}
		parsedModule, parseError := modfile.Parse(relativePath, contents, nil)
		if parseError != nil {
			return fmt.Errorf("parse %s: %w", relativePath, parseError)
		}
		for _, replacement := range parsedModule.Replace {
			if isSparkwingDogfoodReplace(relativePath, replacement) {
				continue
			}
			offenders = append(offenders,
				fmt.Sprintf("%s: %s => %s", relativePath, replacement.Old.Path, replacement.New.Path))
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	return fmt.Errorf(
		"refusing to push: %d disallowed replace line(s) (remove and pin a released tag):\n    %s",
		len(offenders), strings.Join(offenders, "\n    "),
	)
}

func isSparkwingDogfoodReplace(path string, replacement *modfile.Replace) bool {
	return path == ".sparkwing/go.mod" &&
		replacement.Old.Path == "github.com/sparkwing-dev/sparkwing" &&
		replacement.Old.Version == "" &&
		replacement.New.Path == ".." &&
		replacement.New.Version == ""
}

func checkNoCommittedGoWorkFiles(jobContext context.Context) error {
	output, err := sparkwing.Bash(jobContext,
		`git ls-files | grep -E '(^|/)go\.work(\.sum)?$' || true`,
	).String()
	if err != nil {
		return fmt.Errorf("scan go.work files: %w", err)
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	files := strings.Split(output, "\n")
	return fmt.Errorf(
		"refusing to push: %d committed go.work file(s) (remove and add to .gitignore):\n    %s",
		len(files), strings.Join(files, "\n    "),
	)
}

func init() {
	sparkwing.Register("pre-release", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PreRelease{} })
}
