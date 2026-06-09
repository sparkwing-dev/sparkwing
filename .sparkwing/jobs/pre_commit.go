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
// tracker IDs (IMP-, SDK-, LOCAL-, RUN-, ORG-, REG-, TOD-); the
// docs-mirror check fails when docs/ (the source) and pkg/docs/mirror/
// (the embedded copy) have drifted, so an edit to docs/ can't be
// committed without re-running bin/sync-docs.sh.
//
// Wire it to git: declare the `pre_commit:` trigger in sparkwing.yaml
// and run `sparkwing pipeline hooks install`.
type PreCommit struct{ sparkwing.Base }

func (PreCommit) ShortHelp() string {
	return "Fast pre-commit gate: format, vet, em-dash + tracker-ID sweeps, docs-mirror sync"
}

func (PreCommit) Help() string {
	return "Runs gofmt and go vet on the .sparkwing/ module, plus repo-wide checks: no em dashes, no internal tracker IDs (IMP-/SDK-/LOCAL-/RUN-/ORG-/REG-/TOD-), and that pkg/docs/mirror/ matches the docs/ source (run bin/sync-docs.sh if it drifted)."
}

func (PreCommit) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Manually invoke the pre-commit gate", Command: "sparkwing run pre-commit"},
	}
}

func (p *PreCommit) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p)
	return nil
}

// Work declares one step per check so they dispatch in parallel. A
// failed step doesn't block its siblings; the node's terminal outcome
// rolls up from the steps, and the dashboard surfaces each check's
// status independently. No Needs() edges between steps -- they're
// fully independent.
func (p *PreCommit) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "gofmt", runGofmt)
	sparkwing.Step(w, "vet", runVet)
	sparkwing.Step(w, "em-dashes", checkEmDashes)
	sparkwing.Step(w, "tracker-ids", checkTrackerIDs)
	sparkwing.Step(w, "docs-mirror", checkDocsMirror)
	return nil, nil
}

func runGofmt(ctx context.Context) error {
	return sparkwing.Bash(ctx, `gofmt -l .sparkwing/`).MustBeEmpty("files need formatting")
}

// checkDocsMirror fails when the embedded pkg/docs/mirror/ has drifted
// from the canonical docs/ source. Read-only (a recursive diff, no
// mutation) so it's safe to run alongside the other parallel steps. The
// fix is `bash bin/sync-docs.sh && git add pkg/docs/mirror`.
func checkDocsMirror(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "diff -rq docs pkg/docs/mirror").Run(); err != nil {
		return fmt.Errorf("docs/ and pkg/docs/mirror/ are out of sync; run `bash bin/sync-docs.sh && git add pkg/docs/mirror` (edit docs/, never the mirror)")
	}
	return nil
}

func runVet(ctx context.Context) error {
	_, err := sparkwing.Bash(ctx, "go -C .sparkwing vet ./...").Run()
	return err
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
		// Skip binary files: a null byte in the first 8KB
		// is a strong signal the content isn't prose. Lambda
		// bootstrap binaries, archives, etc. can contain bytes
		// that match the em-dash sequence coincidentally.
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
		// Skip binary files: a null byte in the first 8KB
		// is a strong signal the content isn't prose. Lambda
		// bootstrap binaries, archives, etc. can contain bytes
		// that match the em-dash sequence coincidentally.
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
