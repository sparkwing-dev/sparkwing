package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PrePush gates pushes to main with the slower checks that don't
// belong in pre-commit: full golangci-lint, `go test -race`, the
// version-freshness check against the sparkwing ecosystem, and a
// hard ban on any `replace` directive in a committed `go.mod`.
//
// Push-to-main means this pipeline is the last gate before code is
// shared, so it's stricter than a typical PR-time check.
//
// Wire it to git: declare `pre_push:` in pipelines.yaml and run
// `sparkwing pipeline hooks install`. Tooling assumed on PATH:
// golangci-lint, staticcheck (called by golangci-lint), govulncheck.
type PrePush struct{ sparkwing.Base }

func (PrePush) ShortHelp() string {
	return "Pre-push gate: lint, test -race, vuln, freshness, no replace + no go.work"
}

func (PrePush) Help() string {
	return "Final gate before main. Runs the full golangci-lint set, " +
		"`go test -race ./...`, `govulncheck ./...`, the " +
		"sparkwing-ecosystem version-freshness check (deps must be at " +
		"the latest released tag, or replaced with a not-behind local " +
		"path), refuses to push if any committed go.mod contains a " +
		"`replace` line, and refuses to push if `go.work` / `go.work.sum` " +
		"have been committed (workspaces are local-iteration scaffolding " +
		"and can't be resolved by the Go module proxy)."
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

	// 1. No `replace` directives in any committed go.mod. Replace is
	// fine locally during iteration; it must NEVER reach main.
	if err := checkNoReplaceDirectivesInCommittedGoMods(ctx); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "no-replace check: clean")
	}

	// 1a. No committed go.work / go.work.sum. Workspaces are a
	// local-only convenience; they can't be resolved by the Go module
	// proxy and silently override the published versions for anyone
	// who clones the repo. Same rule as `replace`: fine locally, never
	// in main.
	if err := checkNoCommittedGoWorkFiles(ctx); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "no-go.work check: clean")
	}

	// 1b. `go mod tidy` drift: running tidy should produce no diff. A
	// non-tidy go.mod is a near-certain sign that a recent `go get`
	// wasn't followed by tidy. We swallow tidy's own error (it can fail
	// in workspaces with unreleased local sibling modules); the actual
	// signal is `git diff --quiet` against go.mod/go.sum after tidy ran.
	if _, err := sparkwing.Bash(ctx,
		`go -C .sparkwing mod tidy 2>/dev/null || true; git diff --quiet -- .sparkwing/go.mod .sparkwing/go.sum`,
	).Run(); err != nil {
		failures = append(failures, "go mod tidy drift: run `go -C .sparkwing mod tidy` and commit the result")
	} else {
		sparkwing.Info(ctx, "go mod tidy: no drift")
	}

	// 2. Version freshness: every sparkwing-ecosystem dep must be at
	// the latest released tag (or a not-behind local replace).
	if err := CheckVersionsFreshness(ctx, sparkwing.WorkDir()); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "version freshness: current")
	}

	// 2b. Pre-v1 policy: CHANGELOG and VERSIONING.md must not assert
	// the project has shipped v1.0.0; the version-gate in release.go
	// blocks v1+ tags at cut-time, this check catches the indirect
	// signals (doc edits, manual tag pushes).
	if err := CheckPreV1Policy(ctx, sparkwing.WorkDir()); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "pre-v1 policy: clean")
	}

	// 3. Repo-wide gofmt sweep. golangci-lint runs in .sparkwing/ only,
	// so a struct-alignment fix at the top of the tree never lands here.
	// `gofmt -l` lists every unformatted file; non-empty output = fail.
	if err := sparkwing.Bash(ctx, `gofmt -l $(go list -f '{{.Dir}}' ./...)`).
		MustBeEmpty("gofmt reported unformatted files"); err != nil {
		failures = append(failures, fmt.Sprintf("gofmt: %v", err))
	} else {
		sparkwing.Info(ctx, "gofmt: clean")
	}

	// 4. Full golangci-lint sweep on .sparkwing/ (if a config is
	// present there; falls back to a no-op message otherwise).
	if _, err := sparkwing.Bash(ctx, "cd .sparkwing && golangci-lint run ./...").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("golangci-lint: %v", err))
	} else {
		sparkwing.Info(ctx, "golangci-lint: clean")
	}

	// 4. Test suite with the race detector.
	if _, err := sparkwing.Bash(ctx, "go -C .sparkwing test -race ./...").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("go test -race: %v", err))
	} else {
		sparkwing.Info(ctx, "go test -race: passed")
	}

	// 5. Known-vulnerabilities scan.
	//
	// `go run` compiles govulncheck against the current toolchain so
	// the scan reports against the actual stdlib version the project
	// builds with. The standalone `govulncheck` binary on PATH is
	// frozen to whatever Go version installed it, which produces stale
	// false-positives after a system Go upgrade.
	if _, err := sparkwing.Bash(ctx, "cd .sparkwing && go run golang.org/x/vuln/cmd/govulncheck@latest ./...").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("govulncheck: %v", err))
	} else {
		sparkwing.Info(ctx, "govulncheck: clean")
	}

	// 6. Shell-script gate. bin/check-shell.sh discovers every tracked
	// .sh + bash-shebanged file and runs shellcheck on it. No-op when
	// the repo has no shell scripts.
	if _, err := sparkwing.Bash(ctx, "bash bin/check-shell.sh").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("shellcheck: %v", err))
	} else {
		sparkwing.Info(ctx, "shellcheck: clean")
	}

	// 7. Markdown lint. .markdownlint-cli2.yaml selects which files
	// to check; CHANGELOG.md is exempt (the changelog-style gate
	// owns that surface).
	if _, err := sparkwing.Bash(ctx, "markdownlint-cli2").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("markdownlint: %v", err))
	} else {
		sparkwing.Info(ctx, "markdownlint: clean")
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d pre-push check(s) failed:\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	}
	return nil
}

// checkNoReplaceDirectivesInCommittedGoMods refuses to let any
// committed go.mod ship with a `replace` line. Replace directives
// are intended for local iteration; once they leak into main they
// break every consumer of this repo (Go module proxy can't resolve
// a local-path replace, so anyone cloning will fail to build).
//
// Carve-out: .sparkwing/go.mod's dogfood self-replace
// (`github.com/sparkwing-dev/sparkwing => ..`) is allowed. The
// .sparkwing/ directory is a separate Go module (declared
// `module sparkwing-pipelines`) and is excluded from the parent
// module's proxy archive, so the replace target `..` always
// resolves to the parent checkout for anyone who could possibly
// build it. See isSparkwingDogfoodReplace for the exact pattern.
func checkNoReplaceDirectivesInCommittedGoMods(ctx context.Context) error {
	// Run from the repo root explicitly so the paths git emits are
	// repo-root-relative regardless of where the pipeline binary's
	// process cwd is.
	out, err := sparkwing.Bash(ctx,
		`git -C "$SPARKWING_WORKDIR" ls-files '*go.mod'`,
	).Env("SPARKWING_WORKDIR", sparkwing.Path()).String()
	if err != nil {
		return fmt.Errorf("list go.mod files: %w", err)
	}
	var offenders []string
	for _, rel := range strings.Split(strings.TrimSpace(out), "\n") {
		if rel == "" {
			continue
		}
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

// isSparkwingDogfoodReplace recognizes the one replace the sparkwing
// repo ships in main: .sparkwing/go.mod redirects the sparkwing module
// to the parent checkout (`..`) so the repo's own pipelines compile
// against the in-flight SDK source rather than the last-published tag
// via the module proxy. Anything else in .sparkwing/go.mod or any
// replace in another go.mod still fails the check.
func isSparkwingDogfoodReplace(path string, r *modfile.Replace) bool {
	return path == ".sparkwing/go.mod" &&
		r.Old.Path == "github.com/sparkwing-dev/sparkwing" &&
		r.Old.Version == "" &&
		r.New.Path == ".." &&
		r.New.Version == ""
}

// checkNoCommittedGoWorkFiles refuses to let a workspace file ship.
// `go.work` and `go.work.sum` are local-iteration scaffolding (they
// point at relative paths on the developer's machine) and break
// builds for anyone who clones the repo. The matching gitignore
// patterns should prevent these from ever being staged, but the
// check is belt-and-suspenders.
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
}
