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
)

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
	AllowReleaseLineSelfReplace bool
}

func (PreRelease) ShortHelp() string {
	return "Release-boundary checks: race, chaos, vulnerabilities, dependencies, public interfaces, and infrastructure"
}

func (PreRelease) Help() string {
	return "Run lint, race tests, Postgres tests, admission fault tests, vulnerability scans, " +
		"dependency checks, public interface checks, Terraform checks, and workflow checks. " +
		"Committed Go modules must use released dependencies; the pipeline module may replace " +
		"the Sparkwing module with its parent checkout. Keep Go workspace files untracked. " +
		"The gate updates a stale Sparkwing dependency pin, regenerates interface snapshots, " +
		"and commits those changes before the push. This is the release-boundary tier: the release " +
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
	sparkwing.Job(plan, runContext.Pipeline, preRelease.run)
	return nil
}

func (preRelease *PreRelease) run(jobContext context.Context) error {
	var failures []string

	if err := checkNoReplaceDirectivesInCommittedGoMods(jobContext); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(jobContext, "no-replace check: passed")
	}

	if err := checkNoCommittedGoWorkFiles(jobContext); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(jobContext, "no-go.work check: passed")
	}

	if _, err := sparkwing.Bash(jobContext,
		`go -C .sparkwing mod tidy 2>/dev/null || true; git diff --quiet -- .sparkwing/go.mod .sparkwing/go.sum`,
	).Run(); err != nil {
		failures = append(failures, "go mod tidy drift: run `go -C .sparkwing mod tidy` and commit the result")
	} else {
		sparkwing.Info(jobContext, "go mod tidy: no drift")
	}

	if bumpedTo, err := autoBumpSparkwingPinIfStale(jobContext, sparkwing.WorkDir()); err != nil {
		failures = append(failures, fmt.Sprintf("auto-bump sparkwing pin: %v", err))
	} else if bumpedTo != "" {
		sparkwing.Info(jobContext, "sparkwing pin: auto-bumped to %s (commit added to push)", bumpedTo)
	}

	versionOptions := VersionFreshnessOptions{
		AllowReleaseLineSelfReplace: preRelease.AllowReleaseLineSelfReplace,
	}
	if err := CheckVersionsFreshnessWithOptions(jobContext, sparkwing.WorkDir(), versionOptions); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(jobContext, "version freshness: passed")
	}

	if err := CheckPreV1Policy(jobContext, sparkwing.WorkDir()); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(jobContext, "pre-v1 policy: passed")
	}

	if err := sparkwing.Bash(jobContext, `gofmt -l $(go list -f '{{.Dir}}' ./...)`).
		MustBeEmpty("gofmt reported unformatted files"); err != nil {
		failures = append(failures, fmt.Sprintf("gofmt: %v", err))
	} else {
		sparkwing.Info(jobContext, "gofmt: passed")
	}

	if err := runGolangciLint(jobContext); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(jobContext, "golangci-lint: passed")
	}

	if _, err := sparkwing.Bash(jobContext, "go -C .sparkwing test -race ./...").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("go test -race: %v", err))
	} else {
		sparkwing.Info(jobContext, "go test -race: passed")
	}

	storeContext, cancelStore := context.WithTimeout(jobContext, storePostgresPrePushTimeout)
	err := (&StorePostgres{}).run(storeContext)
	cancelStore()
	if err != nil {
		failures = append(failures, fmt.Sprintf("store postgres suite: %v", err))
	} else {
		sparkwing.Info(jobContext, "store postgres suite: passed against postgres")
	}

	if _, err := sparkwing.Bash(jobContext, "go test -count=1 -run TestChaos_CI ./internal/chaos").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("chaos gate: %v", err))
	} else {
		sparkwing.Info(jobContext, "chaos gate: admission invariants held under fault injection")
	}

	if err := runReleaseBinaryVulnerabilityScan(jobContext); err != nil {
		failures = append(failures, fmt.Sprintf("release binary vulnerability scan: %v", err))
	} else {
		sparkwing.Info(jobContext, "release binary vulnerability scan: passed")
	}

	if _, err := sparkwing.Bash(jobContext, "bash bin/check-shell-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("shellcheck script portability: %v", err))
	} else {
		sparkwing.Info(jobContext, "shellcheck script portability: passed")
	}
	if _, err := sparkwing.Bash(jobContext, "bash bin/check-hosted-gate-clean-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("hosted gate mutation guard: %v", err))
	} else {
		sparkwing.Info(jobContext, "hosted gate mutation guard: passed")
	}
	if _, err := sparkwing.Bash(jobContext, "bash bin/check-release-binary-vulnerabilities-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("release binary vulnerability scanner: %v", err))
	} else {
		sparkwing.Info(jobContext, "release binary vulnerability scanner: passed")
	}
	if _, err := sparkwing.Bash(jobContext, "bash bin/check-changelog-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("changelog script portability: %v", err))
	} else {
		sparkwing.Info(jobContext, "changelog script portability: passed")
	}
	if _, err := sparkwing.Bash(jobContext, "bash bin/install-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("installer report: %v", err))
	} else {
		sparkwing.Info(jobContext, "installer report: passed")
	}
	if _, err := sparkwing.Bash(jobContext, "bash bin/service-install-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("service installer config guard: %v", err))
	} else {
		sparkwing.Info(jobContext, "service installer config guard: passed")
	}
	if _, err := sparkwing.Bash(jobContext, "bash bin/release-install-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("public installer release verification: %v", err))
	} else {
		sparkwing.Info(jobContext, "public installer release verification: passed")
	}

	if _, err := sparkwing.Bash(jobContext, "bash bin/check-shell.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("shellcheck: %v", err))
	} else {
		sparkwing.Info(jobContext, "shellcheck: passed")
	}

	if _, err := sparkwing.Bash(jobContext, "bash bin/check-terraform-test.sh && bash bin/check-terraform.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("terraform: %v", err))
	} else {
		sparkwing.Info(jobContext, "terraform: validation and both engine plans passed")
	}

	if err := runMarkdownlint(jobContext); err != nil {
		failures = append(failures, fmt.Sprintf("markdownlint: %v", err))
	} else {
		sparkwing.Info(jobContext, "markdownlint: passed")
	}

	if err := runActionlint(jobContext); err != nil {
		failures = append(failures, fmt.Sprintf("actionlint: %v", err))
	} else {
		sparkwing.Info(jobContext, "actionlint: passed")
	}

	if _, err := sparkwing.Bash(jobContext,
		`cd "$ROOT" && go run ./internal/doccheck "$ROOT/docs" "$ROOT"`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		failures = append(failures, fmt.Sprintf("doc-examples: %v", err))
	} else {
		sparkwing.Info(jobContext, "doc-examples: no SDK-API drift")
	}

	if _, err := sparkwing.Bash(jobContext,
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
		failures = append(failures, "cli-reference: stale -- run `bash bin/gen-cli-docs.sh`")
	} else {
		sparkwing.Info(jobContext, "cli-reference: matches source")
	}

	if _, err := sparkwing.Bash(jobContext,
		`cd "$ROOT" && go run ./internal/configref "$ROOT" | diff -u docs/config-reference.md -`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		failures = append(failures, "config-reference: stale -- run `bash bin/gen-config-docs.sh`")
	} else {
		sparkwing.Info(jobContext, "config-reference: matches source")
	}

	if _, err := sparkwing.Bash(jobContext,
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
		failures = append(failures, "sdk-reference: stale -- run `bash bin/gen-sdk-docs.sh`")
	} else {
		sparkwing.Info(jobContext, "sdk-reference: matches source")
	}

	if _, err := sparkwing.Bash(jobContext,
		`cd "$ROOT" && go run ./internal/apiref "$ROOT" | diff -u docs/api-reference.md -`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		failures = append(failures, "api-reference: stale -- run `bash bin/gen-api-docs.sh`")
	} else {
		sparkwing.Info(jobContext, "api-reference: matches source")
	}

	if _, err := sparkwing.Bash(jobContext, "bash bin/check-api-spec.sh").Run(); err != nil {
		failures = append(failures, "openapi: stale -- run `bash bin/gen-api-docs.sh`")
	} else {
		sparkwing.Info(jobContext, "openapi: matches source")
	}

	if _, err := sparkwing.Bash(jobContext, "bash bin/check-api-snapshot.sh").Run(); err != nil {
		failures = append(failures, "api-snapshot: drift -- run `bash bin/regen-api-snapshot.sh` and commit .apidiff/")
	} else {
		sparkwing.Info(jobContext, "api-snapshot: no drift")
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d pre-push check(s) failed:\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	}
	return nil
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
