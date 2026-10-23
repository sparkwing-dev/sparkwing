package acceptance_test

import (
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func defaultBranchSeals() error {
	original, plan, err := acceptedSettings()
	if err != nil {
		return err
	}
	if branch := original.GuardContext().Git.DefaultBranch; !branch.Set || branch.Value != "trunk" {
		return errors.New("App default branch missing from sealed guard context")
	}
	resolved := original.Resolved
	resolved.DefaultBranch = runspec.Optional[string]{Set: true, Value: "replacement"}
	resolved.DefaultBranchSource = runspec.DefaultBranchOriginHEAD
	if _, err := runspec.SealSpec(runspec.SealInput{Request: original.Request, Resolved: resolved, Plan: plan}); err == nil {
		return errors.New("origin HEAD replaced App default branch")
	}
	input := original.Request.Request
	input.Git.App = runspec.Optional[runspec.AppFacts]{}
	input.Git.DefaultBranch = runspec.Optional[string]{}
	request, err := runspec.SealRequest(input, runspec.IntakeFacts{RunID: "run-without-app"})
	if err != nil {
		return err
	}
	resolved.DefaultBranch.Value = "trunk"
	discovered, err := runspec.SealSpec(runspec.SealInput{Request: request, Resolved: resolved, Plan: plan})
	if err != nil {
		return err
	}
	if ctx := discovered.GuardContext(); ctx.Git.App.Set || ctx.Git.DefaultBranch.Value != "trunk" || discovered.Request.Request.Git.DefaultBranch.Set {
		return errors.New("origin HEAD changed intake or manufactured App trust")
	}
	return nil
}

func independentJobTimeouts() error {
	spec, _, err := acceptedSettings()
	if err != nil {
		return err
	}
	for _, quiet := range []int64{0, 1000, 90000} {
		plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "work", TimeoutMS: boundaryPointer(int64(60000)), NoProgressTimeoutMS: boundaryPointer(quiet)}}}
		execution, err := settings.ResolvePlan(settings.PlanInputs{Request: spec.Request, Declarations: spec.Declarations, Plan: plan, Context: settings.Context{Local: true}})
		if err != nil {
			return err
		}
		job := execution.Jobs["work"]
		if job.Timeout.Value != time.Minute || job.NoProgressTimeout.Value != time.Duration(quiet)*time.Millisecond || !job.NoProgressTimeout.Set {
			return errors.New("execution and inactivity timeouts coupled or explicit zero lost")
		}
		expansion, err := settings.ResolveExpansion(settings.ExpansionInputs{Spec: spec, Plan: plan, Context: settings.Context{Local: true}})
		if err != nil {
			return err
		}
		if expansion["work"].Timeout.Value != job.Timeout.Value || expansion["work"].NoProgressTimeout.Value != job.NoProgressTimeout.Value {
			return errors.New("generated timeout settings differ")
		}
	}
	return nil
}
