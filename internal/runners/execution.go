package runners

import (
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

type nodeExecution struct {
	engine              engine.Identity
	runner              nodeproto.RunnerIdentity
	job                 string
	generation, attempt int64
}

func executionFor(unit controllerwire.ClaimedUnit) nodeExecution {
	runner := unit.Runner
	runner.Labels = slices.Clone(runner.Labels)
	return nodeExecution{engine: *unit.Engine, runner: runner, job: unit.JobID, generation: unit.Binding.Generation, attempt: unit.Binding.Attempt}
}

func (grant nodeExecution) input(prepared nodehost.NodeInput) (nodehost.NodeInput, error) {
	if prepared.Entry == nil || prepared.Entry.Engine.Identity != grant.engine || prepared.Entry.Engine.Executable == "" || prepared.Entry.Engine.Target.Binary != releaseasset.Sparkwing {
		return nodehost.NodeInput{}, errors.New("runner source is missing its matching selected engine")
	}
	prepared.Engine = prepared.Entry.Engine
	prepared.GrantedRunner = grant.runner
	prepared.GrantedRunner.Labels = slices.Clone(grant.runner.Labels)
	prepared.NodeID, prepared.Generation, prepared.Attempt = grant.job, grant.generation, grant.attempt
	return prepared, nil
}
