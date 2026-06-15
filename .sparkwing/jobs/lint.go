package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Lint runs fast, repo-wide checks: gofmt compliance, go vet across
// every package in the sparkwing module, the CHANGELOG-required gate
// (any change to a covered surface needs an [Unreleased] entry), the
// CHANGELOG-style gate (dedupe sub-headings + breaking-entry migration
// guides, per docs/changelog-style.md), and the API surface gate that
// diffs HEAD's public API against the checked-in snapshots under
// .apidiff/. Cross-repo callers (a downstream release-all orchestration
// pipeline) can invoke `sparkwing run lint` here as a gate.
type Lint struct{ sparkwing.Base }

func (Lint) ShortHelp() string {
	return "Fast static check: gofmt + go vet + changelog gates + API snapshot"
}

func (Lint) Help() string {
	return "Fast static checks across the public sparkwing module: gofmt compliance, go vet, the CHANGELOG-required gate (bin/check-changelog.sh), the CHANGELOG-style gate enforcing docs/changelog-style.md (dedupe sub-headings + breaking-entry migration links), and the API-surface drift gate (bin/check-api-snapshot.sh). See VERSIONING.md."
}

func (Lint) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Quick static check before pushing a local change", Command: "sparkwing run lint"},
	}
}

func (p *Lint) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p.run)
	return nil
}

func (p *Lint) run(ctx context.Context) error {
	if err := sparkwing.Bash(ctx, `gofmt -l $(go list -f '{{.Dir}}' ./...)`).
		MustBeEmpty("gofmt reported unformatted files"); err != nil {
		return err
	}
	sparkwing.Info(ctx, "gofmt: all files formatted")

	if _, err := sparkwing.Bash(ctx, "go vet ./...").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "go vet: no issues")

	if _, err := sparkwing.Bash(ctx, "bash bin/check-changelog.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "changelog gate: ok")

	if err := CheckChangelogLint(ctx, sparkwing.WorkDir()); err != nil {
		return err
	}
	sparkwing.Info(ctx, "changelog style gate: clean")

	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-snapshot.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "api snapshot gate: ok")

	if _, err := sparkwing.Bash(ctx, "bash bin/check-shell.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "shellcheck: clean")

	if _, err := sparkwing.Bash(ctx, "markdownlint-cli2").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "markdownlint: clean")

	return nil
}

func init() {
	sparkwing.Register("lint", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Lint{} })
}
