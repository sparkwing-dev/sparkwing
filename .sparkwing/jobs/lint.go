package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Lint struct{ sparkwing.Base }

func (Lint) ShortHelp() string {
	return "Fast static check: gofmt + go vet + changelog gates + API snapshot + shell/markdown lint"
}

func (Lint) Help() string {
	return "Fast static checks across the public sparkwing module: gofmt compliance, go vet, the CHANGELOG-required gate (bin/check-changelog.sh), the CHANGELOG-style gate enforcing docs/changelog-style.md (dedupe sub-headings + breaking-entry migration links), and the API-surface drift gate (bin/check-api-snapshot.sh). It also runs shellcheck over every tracked script (bin/check-shell.sh), the installer report (bin/install-test.sh), the shell and changelog script-portability checks (bin/check-shell-test.sh, bin/check-changelog-test.sh), and markdownlint over the markdown tree. shellcheck and markdownlint-cli2 must be on PATH. See VERSIONING.md."
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
	if _, err := sparkwing.Bash(ctx, "bash bin/check-changelog-test.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "changelog gate portability: clean")

	if err := CheckChangelogLint(ctx, sparkwing.WorkDir()); err != nil {
		return err
	}
	sparkwing.Info(ctx, "changelog style gate: clean")

	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-snapshot.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "api snapshot gate: ok")

	if _, err := sparkwing.Bash(ctx, "bash bin/check-shell-test.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "shellcheck script portability: clean")

	if _, err := sparkwing.Bash(ctx, "bash bin/check-shell.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "shellcheck: clean")

	if _, err := sparkwing.Bash(ctx, "bash bin/install-test.sh").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "installer report: clean")

	if err := runMarkdownlint(ctx); err != nil {
		return err
	}
	sparkwing.Info(ctx, "markdownlint: clean")

	return nil
}

func init() {
	sparkwing.Register("lint", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Lint{} })
}
