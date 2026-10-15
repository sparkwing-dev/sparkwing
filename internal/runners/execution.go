package runners

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

// ValidateAwardIdentity keeps origin seals distinct from the execution's claim fence.
func ValidateAwardIdentity(unit controllerwire.ClaimedUnit) error {
	request := unit.Request
	if unit.RunID == "" || unit.Binding.RunID != unit.RunID || request.RunID == "" || request.Request.Team == "" {
		return errors.New("runner award has an invalid execution or origin identity")
	}
	if accepted := unit.Accepted; accepted != nil && (accepted.Spec.Request.RunID != request.RunID || accepted.Spec.Request.Request.Team != request.Request.Team || accepted.Spec.Request.Hash != request.Hash) {
		return errors.New("runner award has a different accepted origin or request seal")
	}
	return nil
}

// RunInputForAward captures sealed inputs and granted identity without assigning execution authority.
func RunInputForAward(unit controllerwire.ClaimedUnit) (nodehost.RunInput, error) {
	if err := ValidateAwardIdentity(unit); err != nil {
		return nodehost.RunInput{}, err
	}
	request := unit.Request
	if request.Hash == "" {
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
		if accepted.Spec.Hash == "" || accepted.PlanHash == "" {
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
		decoder := json.NewDecoder(bytes.NewReader(accepted.Plan))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&input.Plan)
	}
	if err != nil || !json.Valid(accepted.Plan) {
		return nodehost.AcceptedRun{}, errors.New("runner award requires the accepted spec and plan")
	}
	return input, nil
}
