package jobs

import (
	"context"
	"fmt"
	"strings"

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
	return "Pre-push gate: lint, test -race, vuln, freshness, no replace directives"
}

func (PrePush) Help() string {
	return "Final gate before main. Runs the full golangci-lint set, " +
		"`go test -race ./...`, `govulncheck ./...`, the " +
		"sparkwing-ecosystem version-freshness check (deps must be at " +
		"the latest released tag, or replaced with a not-behind local " +
		"path), and refuses to push if any committed go.mod contains " +
		"a `replace` line."
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

	// 2. Version freshness: every sparkwing-ecosystem dep must be at
	// the latest released tag (or a not-behind local replace).
	if err := CheckVersionsFreshness(ctx, sparkwing.WorkDir()); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "version freshness: current")
	}

	// 3. Full golangci-lint sweep on .sparkwing/ (if a config is
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
	if _, err := sparkwing.Bash(ctx, "cd .sparkwing && govulncheck ./...").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("govulncheck: %v", err))
	} else {
		sparkwing.Info(ctx, "govulncheck: clean")
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
func checkNoReplaceDirectivesInCommittedGoMods(ctx context.Context) error {
	out, err := sparkwing.Bash(ctx,
		`git ls-files '*go.mod' | xargs -I {} grep -lE '^replace ' {} 2>/dev/null || true`,
	).String()
	if err != nil {
		return fmt.Errorf("scan go.mod files: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	files := strings.Split(out, "\n")
	return fmt.Errorf(
		"refusing to push: %d committed go.mod file(s) contain `replace` lines (remove the replace and pin a released tag):\n    %s",
		len(files), strings.Join(files, "\n    "),
	)
}

func init() {
	sparkwing.Register("pre-push", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PrePush{} })
}
