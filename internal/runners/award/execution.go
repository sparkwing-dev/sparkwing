package runners

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func validateAwardIdentity(unit controllerwire.ClaimedUnit) error {
	request := unit.Request
	if unit.RunID == "" || unit.Binding.RunID != unit.RunID || request.RunID == "" || request.Request.Team == "" {
		return errors.New("runner award has an invalid execution or origin identity")
	}
	if accepted := unit.Accepted; accepted != nil && (accepted.Spec.Request.RunID != request.RunID || accepted.Spec.Request.Request.Team != request.Request.Team || accepted.Spec.Request.Hash != request.Hash) {
		return errors.New("runner award has a different accepted origin or request seal")
	}
	return nil
}

// ValidateAward checks a persisted award for hosting, bootstrap exchange and launcher reconciliation.
// Credential material may be absent during reconciliation.
func ValidateAward(unit controllerwire.ClaimedUnit, delivery capability.DeliveryPurpose) error {
	if err := validateAwardIdentity(unit); err != nil {
		return err
	}
	if unit.UnitID == "" {
		return errors.New("runner award is missing its unit identity")
	}
	if unit.Delivery != delivery || delivery != capability.DeliveryDirect && delivery != capability.DeliveryBootstrap {
		return errors.New("runner award has an invalid delivery purpose")
	}
	binding := unit.Binding
	if unit.Unit != capability.UnitEval && (binding.Holder == "" || binding.Generation <= 0) {
		return errors.New("runner award has an invalid run or claim fence")
	}
	switch unit.Unit {
	case capability.UnitNode:
		if unit.Purpose != capability.PurposeWork || binding.Kind != capability.BindingNode || unit.JobID == "" || binding.NodeID != unit.JobID || binding.Attempt < 0 || binding.Expansion != nil || unit.SourceGeneration != 0 {
			return errors.New("runner award has an invalid job or attempt binding")
		}
		if unit.Engine == nil || unit.Accepted == nil || unit.Execution == nil || unit.Accepted.Spec.Resolved.Engine != *unit.Engine {
			return errors.New("runner award is missing its matching accepted engine, execution settings and run")
		}
		if unit.Delivery == capability.DeliveryDirect && (unit.Runner.Name == "" || unit.Runner.Type == "") {
			return errors.New("runner award is missing its granted runner identity")
		}
	case capability.UnitTrigger:
		if (unit.Purpose != capability.PurposeTrigger && unit.Purpose != capability.PurposeHere) || binding.Kind != capability.BindingTrigger || unit.JobID != "" || binding.NodeID != "" || binding.Expansion != nil || binding.Attempt != 0 || unit.SourceGeneration != 0 {
			return errors.New("runner award has an invalid trigger binding")
		}
	case capability.UnitPlan:
		if unit.Purpose != capability.PurposePlan || binding.Kind != capability.BindingRun || unit.JobID != "" || binding.NodeID != "" || binding.Expansion != nil || binding.Attempt != 0 || unit.SourceGeneration != 0 {
			return errors.New("runner award has an invalid plan binding")
		}
	case capability.UnitEval:
		fence := binding.Expansion
		if unit.Purpose != capability.PurposeEval || binding.Kind != capability.BindingNode || unit.JobID == "" || unit.JobID != binding.NodeID || unit.SourceGeneration < 0 || unit.SourceGeneration != binding.Generation || fence == nil || fence.Kind != capability.UnitEval || fence.ID != unit.UnitID || fence.Holder == "" || fence.Generation <= 0 || unit.Accepted == nil || unit.Engine == nil || unit.Accepted.Spec.Resolved.Engine != *unit.Engine {
			return errors.New("runner award has an invalid source or eval lease binding")
		}
	default:
		return errors.New("controller awarded a unit this runner does not execute")
	}
	if unit.LeaseExpiresAt.IsZero() {
		return errors.New("runner award is missing its lease expiry")
	}
	return nil
}
