package runners

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func runInputFor(unit controllerwire.ClaimedUnit) (nodehost.RunInput, error) {
	request := unit.Request
	if request.RunID != unit.RunID || request.Request.Team == "" || request.Hash == "" {
		return nodehost.RunInput{}, errors.New("runner award is missing its sealed request")
	}
	input := nodehost.RunInput{RunID: unit.RunID, RequestHash: string(request.Hash), GrantedRunner: unit.Runner}
	input.GrantedRunner.Labels = slices.Clone(unit.Runner.Labels)
	var err error
	input.SealedRequest, err = json.Marshal(request.Request)
	if err != nil {
		return nodehost.RunInput{}, err
	}
	if accepted := unit.Accepted; accepted != nil {
		if accepted.Spec.Request.RunID != unit.RunID || accepted.Spec.Request.Request.Team != request.Request.Team || accepted.Spec.Request.Hash != request.Hash || accepted.Spec.Hash == "" || accepted.PlanHash == "" {
			return nodehost.RunInput{}, errors.New("runner award has a different accepted run or request seal")
		}
		value, err := acceptedInput(unit.RunID, *accepted)
		if err != nil {
			return nodehost.RunInput{}, err
		}
		input.Accepted = &value
	}
	return input, nil
}

func acceptedInput(runID string, accepted controllerwire.PlanAccepted) (nodehost.AcceptedRun, error) {
	input := nodehost.AcceptedRun{RunID: runID, SpecHash: string(accepted.Spec.Hash), PlanHash: string(accepted.PlanHash)}
	var err error
	input.Spec, err = json.Marshal(accepted.Spec)
	if err == nil {
		err = json.Unmarshal(accepted.Plan, &input.Plan)
	}
	if err != nil {
		return nodehost.AcceptedRun{}, errors.New("runner award requires the accepted spec and plan")
	}
	return input, nil
}
