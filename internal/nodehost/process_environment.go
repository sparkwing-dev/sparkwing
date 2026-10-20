package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// SDKEnvironment projects sealed runtime facts and persisted job declarations
// into procenv's process policy. Base contains the host's captured inputs;
// native SDK starts never consult the host process's ambient environment.
// Local bases must match the request's snapshot, loaded through Snapshots when supplied.
// Build and Split default to the shared procenv implementations.
type SDKEnvironment struct {
	Base      procenv.Base
	Store     EvaluationStore
	Masks     *MaskRegistry
	Snapshots CapturedEnvironmentLoader
	Build     func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
	Split     func([]procenv.Assignment) (procenv.JobEnv, error)
}

func (e SDKEnvironment) planning(ctx context.Context, input PlanningProcessInput) ([]string, error) {
	if input.Run.RunID == "" || input.Run.Pipeline == "" {
		return nil, errors.New("SDK planning environment requires sealed run identity")
	}
	return e.build(ctx, procenv.Plan, input.Environment, sdkRuntime(input.Run, input.Runner))
}

func (e SDKEnvironment) job(ctx context.Context, kind procenv.Kind, accepted AcceptedRun, nodeID string, generation int64, specHash string, runner nodeproto.RunnerIdentity) ([]string, error) {
	env, _, err := e.jobEnvironments(ctx, kind, accepted, nodeID, generation, specHash, runner, false)
	return env, err
}

func (e SDKEnvironment) jobEnvironments(ctx context.Context, kind procenv.Kind, accepted AcceptedRun, nodeID string, generation int64, specHash string, runner nodeproto.RunnerIdentity, cleanup bool) ([]string, []string, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	if e.Store == nil || accepted.RunID == "" || nodeID == "" || generation < 0 || specHash == "" {
		return nil, nil, errors.New("SDK job environment requires persisted job identity")
	}
	var spec runspec.Spec
	if err := json.Unmarshal(accepted.Spec, &spec); err != nil || accepted.SpecHash == "" || accepted.PlanHash == "" || string(spec.Hash) != accepted.SpecHash || string(spec.PlanHash) != accepted.PlanHash {
		return nil, nil, executorError(apierr.SpecDrift, "SDK environment differs from the accepted run")
	}
	ref := controllerwire.NodeRef{RunID: accepted.RunID, NodeID: nodeID}
	row, err := e.Store.ReadNode(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	if row.Ref != ref || row.Generation != generation || row.SpecHash != specHash {
		return nil, nil, executorError(apierr.SpecDrift, "SDK environment differs from the persisted job")
	}
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil || job.ID != nodeID {
		return nil, nil, executorError(apierr.PlanDrift, "SDK environment has an invalid job declaration")
	}
	runtime := sdkRuntime(executorRunContext(spec), runner)
	// safety: retry seals retain the source run ID; runtime identity belongs to the destination row.
	runtime.RunID = accepted.RunID
	runtime.JobID = nodeID
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	return projectJobEnvironment(kind, job.Env, runtime, cleanup, e.Split, func(kind procenv.Kind, runtime procenv.Runtime) ([]string, error) {
		return e.build(ctx, kind, spec.Request.Request.Env, runtime)
	})
}

func projectJobEnvironment(kind procenv.Kind, env map[string]nodeproto.EnvValue, runtime procenv.Runtime, cleanup bool, split func([]procenv.Assignment) (procenv.JobEnv, error), build func(procenv.Kind, procenv.Runtime) ([]string, error)) ([]string, []string, error) {
	assignments := make([]procenv.Assignment, 0, len(env))
	for name, value := range env {
		assignments = append(assignments, procenv.Assignment{Name: name, Value: value.Value, Startup: value.Startup})
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].Name < assignments[j].Name })
	if split == nil {
		split = procenv.SplitJobEnv
	}
	jobEnv, err := split(assignments)
	if err != nil {
		return nil, nil, err
	}
	runtime.Startup = jobEnv.Startup
	if kind == procenv.StepTool || kind == procenv.Cleanup {
		runtime.Startup = append(runtime.Startup, jobEnv.Body...)
		attempt, err := build(procenv.Attempt, runtime)
		if err != nil {
			return nil, nil, err
		}
		env, err := build(kind, procenv.Runtime{AttemptEnv: attempt})
		return env, nil, err
	}
	startup, err := build(kind, runtime)
	if err != nil || !cleanup {
		return startup, nil, err
	}
	complete := startup
	if len(jobEnv.Body) != 0 {
		runtime.Startup = append(slices.Clone(runtime.Startup), jobEnv.Body...)
		complete, err = build(procenv.Attempt, runtime)
		if err != nil {
			return nil, nil, err
		}
	}
	complete, err = build(procenv.Cleanup, procenv.Runtime{AttemptEnv: complete})
	if err != nil {
		return nil, nil, err
	}
	return startup, complete, nil
}

func (e SDKEnvironment) build(ctx context.Context, kind procenv.Kind, seal runspec.EnvSnapshot, runtime procenv.Runtime) ([]string, error) {
	build := e.Build
	if build == nil {
		build = procenv.Build
	}
	base, err := capturedBase(ctx, e.Base, e.Snapshots, seal)
	if err != nil {
		return nil, err
	}
	runtime.GateIndex = ""
	if base.Local {
		for _, entry := range base.Captured {
			name, value, _ := strings.Cut(entry, "=")
			if name == "SPARKWING_GATE_INDEX" {
				runtime.GateIndex = value
			}
		}
	}
	if e.Masks != nil {
		build = e.Masks.environment(build)
	}
	env, err := build(kind, base, runtime)
	if err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	// safety: an empty built environment must remain explicit to os/exec.
	return append([]string{}, env...), nil
}

func sdkRuntime(run nodeproto.RunContext, runner nodeproto.RunnerIdentity) procenv.Runtime {
	runtime := procenv.Runtime{
		RunID: run.RunID, DryRun: run.DryRun,
		Runner: procenv.RunnerInfo{Name: runner.Name, Type: runner.Type, Labels: slices.Clone(runner.Labels)},
	}
	if run.Git != nil {
		runtime.Branch, runtime.Commit, runtime.Repo = run.Git.Branch, run.Git.SHA, run.Git.Repo
	}
	if run.Trigger != nil {
		runtime.TriggerSource = run.Trigger.Source
	}
	return runtime
}
