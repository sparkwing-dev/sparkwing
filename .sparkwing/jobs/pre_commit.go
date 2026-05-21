package jobs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PreCommit gates local commits with fast deterministic checks. The
// gofmt + go vet pair covers the .sparkwing/ Go module; the two regex
// sweeps cover the whole tracked tree for em dashes and internal
// tracker IDs (IMP-, SDK-, LOCAL-, RUN-, ORG-, REG-, TOD-).
//
// Wire it to git: declare the `pre_commit:` trigger in pipelines.yaml
// and run `sparkwing pipeline hooks install`.
type PreCommit struct{ sparkwing.Base }

func (PreCommit) ShortHelp() string {
	return "Fast pre-commit gate: format, vet, em-dash + tracker-ID sweeps"
}

func (PreCommit) Help() string {
	return "Runs gofmt and go vet on the .sparkwing/ module, plus two repo-wide regex checks: no em dashes, no internal tracker IDs (IMP-/SDK-/LOCAL-/RUN-/ORG-/REG-/TOD-)."
}

func (PreCommit) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Manually invoke the pre-commit gate", Command: "sparkwing run pre-commit"},
	}
}

func (p *PreCommit) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p.run)
	return nil
}

func (p *PreCommit) run(ctx context.Context) error {
	var failures []string

	if err := sparkwing.Bash(ctx, `gofmt -l .sparkwing/`).MustBeEmpty("files need formatting"); err != nil {
		failures = append(failures, fmt.Sprintf("gofmt: %v", err))
	} else {
		sparkwing.Info(ctx, "gofmt: clean")
	}

	if _, err := sparkwing.Bash(ctx, "go -C .sparkwing vet ./...").Run(); err != nil {
		failures = append(failures, fmt.Sprintf("go vet: %v", err))
	} else {
		sparkwing.Info(ctx, "go vet: clean")
	}

	if err := checkEmDashes(ctx); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "em dashes: none")
	}

	if err := checkTrackerIDs(ctx); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "tracker IDs: none")
	}

	if err := CheckNoRawCSS(ctx, regexCheckRoot()); err != nil {
		failures = append(failures, err.Error())
	} else {
		sparkwing.Info(ctx, "raw CSS: none")
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d pre-commit check(s) failed:\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	}
	return nil
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
		if bytes.Contains(data, []byte("—")) {
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

// regexCheckFiles returns the tracked-file list filtered for the
// regex sweeps: tickets/ and archive/ are exempt because historical
// content is allowed to carry whatever style it was written with.
func regexCheckFiles(ctx context.Context) ([]string, error) {
	all, err := sparkwing.Bash(ctx, "git ls-files").Lines()
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
