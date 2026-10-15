package runners

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// NodePreparation joins bound host services to acquisition of the accepted source.
type NodePreparation struct {
	Host    nodehost.NodeHost
	Input   nodehost.NodeInput
	Source  source.Options
	Acquire func(context.Context, runspec.Spec, source.Options) (*source.Entry, func() error, error)
	Close   func() error
}

// Prepare constructs executor inputs from the award and returns ownership of every acquired resource.
func (p NodePreparation) Prepare(ctx context.Context, unit controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
	var releaseSource func() error
	closeResources := func() error {
		var err error
		if releaseSource != nil {
			err = releaseSource()
		}
		if p.Close != nil {
			err = errors.Join(err, p.Close())
		}
		return err
	}
	input := p.Input
	if err := validateHostedAward(unit); err != nil {
		return nil, input, closeResources, err
	}
	if unit.Unit != capability.UnitNode || unit.Execution == nil || p.Host == nil || input.SpecHash == "" {
		return nil, input, closeResources, errors.New("node preparation requires an execution award, host and job hash")
	}
	execution := executionFor(unit)
	routes := input.RouteBinding
	if routes.RunID != unit.RunID || routes.JobID != unit.JobID || routes.Routes.URL == "" || routes.Routes.Bearer == "" || routes.Routes.Bearer == unit.Capability {
		return nil, input, closeResources, errors.New("node preparation requires delegated routes for the awarded job")
	}
	var err error
	input.Accepted, err = acceptedInput(unit.RunID, *unit.Accepted)
	if err != nil {
		return nil, input, closeResources, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, input, closeResources, err
	}
	acquire := p.Acquire
	if acquire == nil {
		acquire = acquireExecution
	}
	input.Entry, releaseSource, err = acquire(ctx, unit.Accepted.Spec, p.Source)
	if err != nil {
		return nil, input, closeResources, err
	}
	input.Execution = *unit.Execution
	input, err = execution.input(input)
	return p.Host, input, closeResources, err
}

func acquireExecution(ctx context.Context, spec runspec.Spec, options source.Options) (*source.Entry, func() error, error) {
	projection, err := source.ForExecution(spec)
	if err != nil {
		return nil, nil, err
	}
	entry, err := source.Acquire(ctx, projection, options)
	if entry == nil {
		return nil, nil, err
	}
	return entry, entry.Close, err
}
