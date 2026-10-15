package runners

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type nodeExecution struct {
	accepted            nodehost.AcceptedRun
	execution           runspec.JobExecutionSettings
	engine              engine.Identity
	runner              nodeproto.RunnerIdentity
	job                 string
	generation, attempt int64
}

func executionFor(unit controllerwire.ClaimedUnit) (nodeExecution, error) {
	input, err := nodehost.NewRunInput(unit)
	if err != nil {
		return nodeExecution{}, err
	}
	if input.Accepted == nil || unit.Execution == nil {
		return nodeExecution{}, errors.New("runner execution requires the accepted run and job settings")
	}
	settings, err := json.Marshal(unit.Execution)
	if err != nil {
		return nodeExecution{}, err
	}
	var execution runspec.JobExecutionSettings
	if err := json.Unmarshal(settings, &execution); err != nil {
		return nodeExecution{}, err
	}
	runner := unit.Runner
	runner.Labels = slices.Clone(runner.Labels)
	return nodeExecution{accepted: *input.Accepted, execution: execution, engine: *unit.Engine, runner: runner, job: unit.JobID, generation: unit.Binding.Generation, attempt: unit.Binding.Attempt}, nil
}

func (grant nodeExecution) input(prepared nodehost.NodeInput) (nodehost.NodeInput, error) {
	if prepared.Entry == nil || prepared.Entry.Engine.Identity != grant.engine || prepared.Entry.Engine.Executable == "" || prepared.Entry.Engine.Target.Binary != releaseasset.Sparkwing {
		return nodehost.NodeInput{}, errors.New("runner source is missing its matching selected engine")
	}
	prepared.Engine = prepared.Entry.Engine
	prepared.Accepted, prepared.Execution = grant.accepted, grant.execution
	prepared.GrantedRunner = grant.runner
	prepared.GrantedRunner.Labels = slices.Clone(grant.runner.Labels)
	prepared.NodeID, prepared.Generation, prepared.Attempt = grant.job, grant.generation, grant.attempt
	return prepared, nil
}
