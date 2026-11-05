package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const policyJobID = "policy"

// perf: every step here answers in seconds with a warm build cache. The
// allowance covers a fresh hosted runner compiling the checkers and the CLI
// for the first time.
const policyJobTimeout = 10 * time.Minute

// perf: the policy stage runs before every heavy job of gate and land. Most
// failed lands were a one-second policy finding reported only after a
// twenty-minute test, race and review run; ahead of them, it fails in seconds.
type policyJob struct {
	sparkwing.Base
	Proven sparkwing.Ref[gatePass]
}

// safety: CollectAll, so one run names every policy finding rather than the
// first; each step is cheap enough that finishing the rest costs seconds.
func (j *policyJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.FailurePolicy(sparkwing.CollectAll)
	for _, s := range policySteps {
		sparkwing.Step(w, s.id, s.run)
	}
	return nil, nil
}

type policyStep struct {
	id  string
	run func(context.Context) error
}

// safety: the pre-commit tier's checks plus the changelog, API and CLI
// reference drift checks. A step added here is one no heavy job starts without.
var policySteps = []policyStep{
	{"gofmt", runGofmt},
	{"formatters", runFormatters},
	{"em-dashes", checkEmDashes},
	{"tracker-ids", checkTrackerIDs},
	{"tracked-binaries", checkTrackedBinaries},
	{"docs-mirror", checkDocsMirror},
	{"docs-sidebar", checkDocsSidebar},
	{"changelog-links", checkChangelogLinks},
	{"comments", checkComments},
	{"test-sleeps", checkTestSleeps},
	{"home-resolution", checkHomeResolution},
	{"changelog", checkChangelogRequired},
	{"api-spec", checkAPISpec},
	{"api-snapshot", checkAPISnapshot},
	{"cli-docs", checkCLIDocs},
}

func checkCLIDocs(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/gen-cli-docs.sh --check").Env("GOWORK", "off").Run(); err != nil {
		return fmt.Errorf("the generated CLI reference in docs/ differs from the command registry; run `bash bin/gen-cli-docs.sh` and commit docs/ and pkg/docs: %w", err)
	}
	return nil
}

func checkDocsSidebar(ctx context.Context) error {
	if result, err := sparkwing.Bash(ctx, "go run ./internal/doccheck --sidebar docs").Env("GOWORK", "off").Run(); err != nil {
		return fmt.Errorf("docs sidebar differs from the docs tree; add each page to docs/_sidebar.json or its excluded list:\n%s%w", result.Stdout, err)
	}
	return nil
}
