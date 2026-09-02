package jobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the -run pattern is derived from this list and every name must
// report a pass. Matching on the pattern alone would let a renamed check
// shrink the preflight to nothing while the release still went green.
var releaseContractTests = []string{
	"TestDocsNameEveryEnvironmentVariableTheCodeReads",
	"TestEmbeddedReferencePagesNameOnlyDispatchedCommands",
	"TestRepoDocsNameOnlyDispatchedCommandsOutsideTheirDesignRegions",
	"TestEveryPageExemptionMatchesAPageThatExists",
	"TestIntentPagesDeclareThatTheyAreNotShipped",
	"TestEveryRegistryTopLevelVerbIsDispatched",
	"TestHelpListingMatchesRegistry",
	"TestSubcommandOrderMatchesRegistry",
	"TestEveryRegistryFlagIsRegisteredInSource",
	"TestProfilesRegistryMatchesDispatcher",
}

type contractCheck struct {
	Label   string
	Command string
	Tests   []string
}

func releaseContractChecks() []contractCheck {
	return []contractCheck{
		{
			Label:   "embedded documentation mirror",
			Command: "go test -count=1 ./pkg/docs",
		},
		{
			Label:   "documentation, help, and environment-variable contracts",
			Command: "go test -count=1 -v ./cmd/sparkwing -run " + contractTestPattern(releaseContractTests),
			Tests:   releaseContractTests,
		},
	}
}

func contractTestPattern(names []string) string {
	return "'^(" + strings.Join(names, "|") + ")$'"
}

type checkContractsJob struct {
	sparkwing.Base
	RepoDir string
}

func (j *checkContractsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (j *checkContractsJob) run(ctx context.Context) error {
	for _, check := range releaseContractChecks() {
		res, err := sparkwing.Bash(ctx, withoutInherited(check.Command, productTestUnset)).Dir(j.RepoDir).Capture()
		if err != nil {
			return fmt.Errorf("release contract preflight: %s: %w", check.Label, err)
		}
		if err := requireContractTestsPassed(check, res.Stdout+res.Stderr); err != nil {
			return err
		}
		sparkwing.Info(ctx, "contract preflight: %s passed (%d named check(s))", check.Label, len(check.Tests))
	}
	return nil
}

func requireContractTestsPassed(check contractCheck, output string) error {
	var missing []string
	for _, name := range check.Tests {
		if !strings.Contains(output, "--- PASS: "+name+" ") {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("release contract preflight: %s did not report a pass for %d check(s):\n  - %s\nThey were renamed or removed, so `%s` no longer proves what the preflight claims",
		check.Label, len(missing), strings.Join(missing, "\n  - "), check.Command)
}
