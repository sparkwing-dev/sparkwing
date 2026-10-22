package nodehost

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// StartGate asks the store for permission before acquiring optional host compute.
type StartGate struct {
	state    CapacityStore
	compute  *LocalAdmission
	required bool
	Queued   func(context.Context, controllerwire.NodeRef, store.AcquireSlotResponse) error
}

func ComposeStartGate(state CapacityStore, compute *LocalAdmission, required bool) StartGate {
	return StartGate{state: state, compute: compute, required: required}
}

func (g StartGate) claimedNode(ctx context.Context, input NodeInput, body func(context.Context) error) error {
	if g.state == nil {
		return errors.New("execution requires the store start boundary")
	}
	if g.required && g.compute == nil {
		return errors.New("local execution requires machine admission")
	}
	binding := store.CapacityBinding{RunID: input.Accepted.RunID, NodeID: input.NodeID, Generation: int64(input.Generation)}
	if err := awaitStart(ctx, g.state, binding, executorActor(input), func(response store.AcquireSlotResponse) error {
		if g.Queued == nil {
			return nil
		}
		return g.Queued(ctx, controllerwire.NodeRef{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID)}, response)
	}); err != nil {
		return err
	}
	if g.compute == nil {
		return body(ctx)
	}
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)}, Generation: runspec.ClaimGeneration(input.Generation), Execution: input.Execution}
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		return err
	}
	if spec.Request.Request.Burst == nil {
		return g.compute.node(ctx, input.Accepted, row, body)
	}
	err := g.compute.tryNode(ctx, input.Accepted, row, body)
	if !errors.Is(err, errComputeBusy) {
		return err
	}

	if err := g.state.RelinquishStart(ctx, binding, executorActor(input)); err != nil {
		return err
	}
	return nil
}
