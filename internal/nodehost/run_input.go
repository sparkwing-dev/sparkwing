package nodehost

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// NewRunInput captures sealed inputs and the owner fence before placement preparation.
func NewRunInput(unit controllerwire.ClaimedUnit) (RunInput, error) {
	request := unit.Request
	if unit.RunID == "" || request.RunID == "" || request.Request.Team == "" || request.Hash == "" {
		return RunInput{}, errors.New("runner award is missing its sealed request")
	}
	runner := unit.Runner
	runner.Labels = slices.Clone(runner.Labels)
	input := RunInput{
		RunID: unit.RunID, RequestHash: string(request.Hash), GrantedRunner: runner,
		Owner: store.Actor{Kind: store.ActorRunHost, Owner: store.OwnerLeaseRef{
			Kind: store.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(unit.RunID), Holder: unit.Binding.Holder, Generation: runspec.ClaimGeneration(unit.Binding.Generation)},
		}},
	}
	if unit.Unit == capability.UnitEval || unit.Unit == capability.UnitNode {
		input.Owner = store.Actor{}
	}
	var err error
	input.SealedRequest, err = json.Marshal(request.Request)
	if err != nil {
		return RunInput{}, err
	}
	if accepted := unit.Accepted; accepted != nil {
		if accepted.Spec.Request.RunID != request.RunID || accepted.Spec.Request.Request.Team != request.Request.Team || accepted.Spec.Request.Hash != request.Hash || accepted.Spec.Hash == "" || accepted.PlanHash == "" {
			return RunInput{}, errors.New("runner award has a different accepted run or request seal")
		}
		value, err := acceptedInput(unit.RunID, *accepted)
		if err != nil {
			return RunInput{}, err
		}
		input.Accepted = &value
	}
	return input, nil
}

func acceptedInput(runID string, accepted controllerwire.PlanAccepted) (AcceptedRun, error) {
	input := AcceptedRun{RunID: runID, SpecHash: string(accepted.Spec.Hash), PlanHash: string(accepted.PlanHash)}
	var err error
	input.Spec, err = json.Marshal(accepted.Spec)
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(accepted.Plan))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&input.Plan)
		if err == nil && len(bytes.TrimSpace(accepted.Plan[decoder.InputOffset():])) != 0 {
			err = errors.New("accepted plan contains trailing input")
		}
	}
	if err != nil {
		return AcceptedRun{}, errors.New("runner award requires the accepted spec and plan")
	}
	return input, nil
}
