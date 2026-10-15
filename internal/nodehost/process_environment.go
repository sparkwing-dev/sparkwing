package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SDKEnvironment projects sealed runtime facts and persisted job declarations
// into procenv's process policy. Base contains the host's captured inputs;
// native SDK starts never consult the host process's ambient environment.
// Build and Split default to the shared procenv implementations.
type SDKEnvironment struct {
	Base  procenv.Base
	Store EvaluationStore
	Build func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
	Split func([]procenv.Assignment) (procenv.JobEnv, error)
}

func (e SDKEnvironment) planning(input PlanningProcessInput) ([]string, error) {
	if input.Run.RunID == "" || input.Run.Pipeline == "" {
		return nil, errors.New("SDK planning environment requires sealed run identity")
	}
	return e.build(procenv.Plan, sdkRuntime(input.Run, input.Runner))
}

func (e SDKEnvironment) job(ctx context.Context, kind procenv.Kind, accepted AcceptedRun, nodeID string, generation int64, specHash string, runner nodeproto.RunnerIdentity) ([]string, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if e.Store == nil || accepted.RunID == "" || nodeID == "" || generation < 0 || specHash == "" {
		return nil, errors.New("SDK job environment requires persisted job identity")
	}
	var spec runspec.Spec
	if err := json.Unmarshal(accepted.Spec, &spec); err != nil || accepted.SpecHash == "" || accepted.PlanHash == "" || string(spec.Hash) != accepted.SpecHash || string(spec.PlanHash) != accepted.PlanHash {
		return nil, executorError(apierr.SpecDrift, "SDK environment differs from the accepted run")
	}
	ref := store.NodeRef{RunID: accepted.RunID, NodeID: nodeID}
	row, err := e.Store.ReadNode(ctx, ref)
	if err != nil {
		return nil, err
	}
	if row.Ref != ref || row.Generation != generation || row.SpecHash != specHash {
		return nil, executorError(apierr.SpecDrift, "SDK environment differs from the persisted job")
	}
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil || job.ID != nodeID {
		return nil, executorError(apierr.PlanDrift, "SDK environment has an invalid job declaration")
	}
	assignments := make([]procenv.Assignment, 0, len(job.Env))
	for name, value := range job.Env {
		assignments = append(assignments, procenv.Assignment{Name: name, Value: value.Value, Startup: value.Startup})
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].Name < assignments[j].Name })
	split := e.Split
	if split == nil {
		split = procenv.SplitJobEnv
	}
	jobEnv, err := split(assignments)
	if err != nil {
		return nil, err
	}
	runtime := sdkRuntime(executorRunContext(spec), runner)
	// safety: retry seals retain the source run ID; runtime identity belongs to the destination row.
	runtime.RunID = accepted.RunID
	runtime.JobID, runtime.Startup = nodeID, jobEnv.Startup
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return e.build(kind, runtime)
}

func (e SDKEnvironment) build(kind procenv.Kind, runtime procenv.Runtime) ([]string, error) {
	build := e.Build
	if build == nil {
		build = procenv.Build
	}
	base := e.Base
	base.Captured, base.Ambient, base.SubmitEnvAllow = slices.Clone(base.Captured), slices.Clone(base.Ambient), slices.Clone(base.SubmitEnvAllow)
	env, err := build(kind, base, runtime)
	if err != nil {
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
