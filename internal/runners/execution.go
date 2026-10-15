package runners

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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
