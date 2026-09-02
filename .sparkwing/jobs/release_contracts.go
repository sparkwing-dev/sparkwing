package jobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type contractCheck struct {
	Label   string
	Command string
}

var releaseContractChecks = []contractCheck{
	{
		Label:   "embedded documentation mirror",
		Command: "go test -count=1 ./pkg/docs",
	},
	{
		Label:   "documentation, help, and environment-variable contracts",
		Command: "go test -count=1 ./cmd/sparkwing -run " + releaseContractTestPattern,
	},
}

const releaseContractTestPattern = "'^Test(Docs|EnvVarWalk|EmbeddedReferencePages|RepoDocsName|ShippedProse|HonestyCheck|IntentPages|EveryPageExemption|EveryRegistryTopLevelVerb)'"

type checkContractsJob struct {
	sparkwing.Base
	RepoDir string
}

func (j *checkContractsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (j *checkContractsJob) run(ctx context.Context) error {
	for _, check := range releaseContractChecks {
		res, err := sparkwing.Bash(ctx, withoutInherited(check.Command, productTestUnset)).Dir(j.RepoDir).Capture()
		if err != nil {
			return fmt.Errorf("release contract preflight: %s: %w", check.Label, err)
		}
		if err := requireContractTestsRan(check, res.Stdout+res.Stderr); err != nil {
			return err
		}
		sparkwing.Info(ctx, "contract preflight: %s passed", check.Label)
	}
	return nil
}

func requireContractTestsRan(check contractCheck, output string) error {
	if !strings.Contains(output, "[no tests to run]") {
		return nil
	}
	return fmt.Errorf("release contract preflight: %s matched no tests; the checks were renamed out from under `%s`, so the preflight proved nothing",
		check.Label, check.Command)
}
