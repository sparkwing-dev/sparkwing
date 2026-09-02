package jobs

import (
	"context"
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

func runMarkdownlint(ctx context.Context) error {
	_, err := sparkwing.Bash(ctx, markdownlintCommand).Run()
	return err
}

func runActionlint(ctx context.Context) error {
	_, err := sparkwing.Bash(ctx, actionlintCommand).Run()
	return err
}

func runReleaseBinaryVulnerabilityScan(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "sparkwing-release-vulnerability-*")
	if err != nil {
		return fmt.Errorf("create release vulnerability scan directory: %w", err)
	}
	defer os.RemoveAll(dir)

	for _, binary := range publicBinaries {
		artifact := filepath.Join(dir, binary)
		if _, err := sparkwing.Exec(ctx, "go", "build", "-trimpath", "-o", artifact, "./cmd/"+binary).
			Env("GOWORK", "off").Run(); err != nil {
			return fmt.Errorf("build release vulnerability artifact %s: %w", binary, err)
		}
		if _, err := sparkwing.Exec(ctx, "bash", "bin/check-release-binary-vulnerabilities.sh", artifact).Run(); err != nil {
			return fmt.Errorf("scan release vulnerability artifact %s: %w", binary, err)
		}
	}
	return nil
}

type PrePush struct {
	sparkwing.Base
	AllowReleaseLineSelfReplace bool
}

func (PrePush) ShortHelp() string {
	return "Release-boundary verification: lint, test -race, chaos, vuln, freshness, api-snapshot, no replace + no go.work"
}

func (PrePush) Help() string {
	return "Explicit release-boundary verification. Runs the full golangci-lint set, " +
		"`go test -race ./...` in the .sparkwing pipeline module, " +
		"binary-mode govulncheck against every shipped Go executable, the " +
		"sparkwing-ecosystem version-freshness check (deps must be at " +
		"the latest released tag, or replaced with a not-behind local " +
		"path), the chaos gate (the adversarial admission suite in " +
		"internal/chaos, which fault-injects a real daemon and asserts " +
		"the concurrency invariants), the public API-surface drift gate " +
		"(the `pkg/` snapshot " +
		"under .apidiff/ must match HEAD), refuses to push if any " +
		"committed go.mod contains a `replace` line other than " +
		"`.sparkwing/go.mod`'s dogfood self-replace to `..`, and refuses to push " +
		"if `go.work` / `go.work.sum` have been committed (workspaces are " +
		"local-iteration scaffolding and can't be resolved by the Go " +
		"module proxy), validates + offline-plans the Mode 3 Postgres " +
		"Terraform module for both engine knobs (bin/check-terraform.sh), " +
		"and validates every GitHub Actions workflow with pinned actionlint. " +
		"Not read-only: when the .sparkwing sparkwing pin is behind the " +
		"latest released tag, pre-push bumps the pin and pkg/scaffold's " +
		"fallback version, tidies .sparkwing/go.mod, regenerates the public " +
		"API snapshots, and commits the result " +
		"so the bump rides along with the push."
}

func (PrePush) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Manually invoke the pre-push gate", Command: "sparkwing run pre-push"},
	}
}

func (p *PrePush) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p.run)
	return nil
}

func (p *PrePush) run(ctx context.Context) error {
	var failures []string

	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "no-replace check: clean")
	}

	if err := checkNoCommittedGoWorkFiles(ctx); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "no-go.work check: clean")
	}

	if _, err := sparkwing.Bash(ctx,
		`go -C .sparkwing mod tidy 2>/dev/null || true; git diff --quiet -- .sparkwing/go.mod .sparkwing/go.sum`,
	).Run(); err != nil {
		failures = append(failures, "go mod tidy drift: run `go -C .sparkwing mod tidy` and commit the result")
	} else {
		sparkwing.Info(ctx, "go mod tidy: no drift")
	}

	if bumpedTo, err := autoBumpSparkwingPinIfStale(ctx, sparkwing.WorkDir()); err != nil {
		failures = append(failures, fmt.Sprintf("auto-bump sparkwing pin: %v", err))
	} else if bumpedTo != "" {
		sparkwing.Info(ctx, "sparkwing pin: auto-bumped to %s (commit added to push)", bumpedTo)
	}

	versionOptions := VersionFreshnessOptions{
		AllowReleaseLineSelfReplace: p.AllowReleaseLineSelfReplace,
	}
	if err := CheckVersionsFreshnessWithOptions(ctx, sparkwing.WorkDir(), versionOptions); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "version freshness: current")
	}

	if err := CheckPreV1Policy(ctx, sparkwing.WorkDir()); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "pre-v1 policy: clean")
	}

	if err := sparkwing.Bash(ctx, `gofmt -l $(go list -f '{{.Dir}}' ./...)`).
		MustBeEmpty("gofmt reported unformatted files"); err != nil {
		failures = append(failures, fmt.Sprintf("gofmt: %v", err))
	} else {
		sparkwing.Info(ctx, "gofmt: clean")
	}

	if err := runGolangciLint(ctx); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "golangci-lint: clean")
	}

	if _, err := sparkwing.Bash(ctx, "go -C .sparkwing test -race ./...").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("go test -race: %v", err))
	} else {
		sparkwing.Info(ctx, "go test -race: passed")
	}

	if _, err := sparkwing.Bash(ctx, "go test -count=1 -run TestChaos_CI ./internal/chaos").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("chaos gate: %v", err))
	} else {
		sparkwing.Info(ctx, "chaos gate: admission invariants held under fault injection")
	}

	if err := runReleaseBinaryVulnerabilityScan(ctx); err != nil {
		failures = append(failures, fmt.Sprintf("release binary vulnerability scan: %v", err))
	} else {
		sparkwing.Info(ctx, "release binary vulnerability scan: clean")
	}

	if _, err := sparkwing.Bash(ctx, "bash bin/check-shell-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("shellcheck script portability: %v", err))
	} else {
		sparkwing.Info(ctx, "shellcheck script portability: clean")
	}
	if _, err := sparkwing.Bash(ctx, "bash bin/check-hosted-gate-clean-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("hosted gate mutation guard: %v", err))
	} else {
		sparkwing.Info(ctx, "hosted gate mutation guard: clean")
	}
	if _, err := sparkwing.Bash(ctx, "bash bin/check-release-binary-vulnerabilities-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("release binary vulnerability scanner: %v", err))
	} else {
		sparkwing.Info(ctx, "release binary vulnerability scanner: clean")
	}
	if _, err := sparkwing.Bash(ctx, "bash bin/check-changelog-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("changelog script portability: %v", err))
	} else {
		sparkwing.Info(ctx, "changelog script portability: clean")
	}
	if _, err := sparkwing.Bash(ctx, "bash bin/install-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("installer report: %v", err))
	} else {
		sparkwing.Info(ctx, "installer report: clean")
	}
	if _, err := sparkwing.Bash(ctx, "bash bin/service-install-test.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("service installer config guard: %v", err))
	} else {
		sparkwing.Info(ctx, "service installer config guard: clean")
	}

	if _, err := sparkwing.Bash(ctx, "bash bin/check-shell.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("shellcheck: %v", err))
	} else {
		sparkwing.Info(ctx, "shellcheck: clean")
	}

	if _, err := sparkwing.Bash(ctx, "bash bin/check-terraform.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("terraform: %v", err))
	} else {
		sparkwing.Info(ctx, "terraform: module valid + plans clean (both engines)")
	}

	if err := runMarkdownlint(ctx); err != nil {
		failures = append(failures, fmt.Sprintf("markdownlint: %v", err))
	} else {
		sparkwing.Info(ctx, "markdownlint: clean")
	}

	if err := runActionlint(ctx); err != nil {
		failures = append(failures, fmt.Sprintf("actionlint: %v", err))
	} else {
		sparkwing.Info(ctx, "actionlint: clean")
	}

	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" && go run ./internal/doccheck "$ROOT/docs" "$ROOT"`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		failures = append(failures, fmt.Sprintf("doc-examples: %v", err))
	} else {
		sparkwing.Info(ctx, "doc-examples: no SDK-API drift")
	}

	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" &&
		TMP="$(mktemp -d)" &&
		trap 'rm -rf "$TMP"' EXIT &&
		go run ./cmd/sparkwing commands -o markdown --split-dir "$TMP" >/dev/null &&
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
		sparkwing.Info(ctx, "cli-reference: current")
	}

	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" && go run ./internal/configref "$ROOT" | diff -u docs/config-reference.md -`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		failures = append(failures, "config-reference: stale -- run `bash bin/gen-config-docs.sh`")
	} else {
		sparkwing.Info(ctx, "config-reference: current")
	}

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
		failures = append(failures, "sdk-reference: stale -- run `bash bin/gen-sdk-docs.sh`")
	} else {
		sparkwing.Info(ctx, "sdk-reference: current")
	}

	if _, err := sparkwing.Bash(ctx,
		`cd "$ROOT" && go run ./internal/apiref "$ROOT" | diff -u docs/api-reference.md -`,
	).Env("ROOT", sparkwing.Path()).Run(); err != nil {
		failures = append(failures, "api-reference: stale -- run `bash bin/gen-api-docs.sh`")
	} else {
		sparkwing.Info(ctx, "api-reference: current")
	}

	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-spec.sh").Run(); err != nil {
		failures = append(failures, "openapi: stale -- run `bash bin/gen-api-docs.sh`")
	} else {
		sparkwing.Info(ctx, "openapi: current")
	}

	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-snapshot.sh").Run(); err != nil {
		failures = append(failures, "api-snapshot: drift -- run `bash bin/regen-api-snapshot.sh` and commit .apidiff/")
	} else {
		sparkwing.Info(ctx, "api-snapshot: no drift")
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d pre-push check(s) failed:\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	}
	return nil
}

func committedGoMods(ctx context.Context) ([]string, error) {
	// safety: git -C anchors paths to repo root regardless of process cwd.
	out, err := sparkwing.Bash(ctx,
		`git -C "$SPARKWING_WORKDIR" ls-files '*go.mod'`,
	).Env("SPARKWING_WORKDIR", sparkwing.Path()).String()
	if err != nil {
		return nil, fmt.Errorf("list go.mod files: %w", err)
	}
	var mods []string
	for _, rel := range strings.Split(strings.TrimSpace(out), "\n") {
		if rel != "" && !isTestdataPath(rel) {
			mods = append(mods, rel)
		}
	}
	return mods, nil
}

func isTestdataPath(rel string) bool {
	return strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/")
}

func committedModuleDirs(ctx context.Context) ([]string, error) {
	mods, err := committedGoMods(ctx)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(mods))
	for _, m := range mods {
		dirs = append(dirs, filepath.Dir(m))
	}
	return dirs, nil
}

func checkNoReplaceDirectivesInCommittedGoMods(ctx context.Context) error {
	mods, err := committedGoMods(ctx)
	if err != nil {
		return err
	}
	var offenders []string
	for _, rel := range mods {
		abs := sparkwing.Path(rel)
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", rel, rerr)
		}
		mf, perr := modfile.Parse(rel, data, nil)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		for _, r := range mf.Replace {
			if isSparkwingDogfoodReplace(rel, r) {
				continue
			}
			offenders = append(offenders,
				fmt.Sprintf("%s: %s => %s", rel, r.Old.Path, r.New.Path))
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

func isSparkwingDogfoodReplace(path string, r *modfile.Replace) bool {
	return path == ".sparkwing/go.mod" &&
		r.Old.Path == "github.com/sparkwing-dev/sparkwing" &&
		r.Old.Version == "" &&
		r.New.Path == ".." &&
		r.New.Version == ""
}

func checkNoCommittedGoWorkFiles(ctx context.Context) error {
	out, err := sparkwing.Bash(ctx,
		`git ls-files | grep -E '(^|/)go\.work(\.sum)?$' || true`,
	).String()
	if err != nil {
		return fmt.Errorf("scan go.work files: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	files := strings.Split(out, "\n")
	return fmt.Errorf(
		"refusing to push: %d committed go.work file(s) (remove + add to .gitignore):\n    %s",
		len(files), strings.Join(files, "\n    "),
	)
}

func init() {
	sparkwing.Register("pre-push", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PrePush{} })
	sparkwing.Register("push-checks", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PrePush{} })
}
