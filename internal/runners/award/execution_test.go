package runners_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	runners "github.com/sparkwing-dev/sparkwing/internal/runners/award"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestAwardShapeSeparatesEvalLeaseFromSource(t *testing.T) {
	for _, delivery := range []capability.DeliveryPurpose{capability.DeliveryBootstrap, capability.DeliveryDirect} {
		for _, generation := range []int64{0, 7} {
			unit := controllerwire.ClaimedUnit{Unit: capability.UnitEval, Purpose: capability.PurposeEval, Delivery: delivery, UnitID: "eval", RunID: "run", JobID: "source", SourceGeneration: generation, LeaseExpiresAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), Request: runspec.SealedRequest{RunID: "origin", Request: runspec.Request{Team: "team"}}, Engine: &engine.Identity{Version: "v1"}, Binding: capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "source", Generation: generation, Expansion: &capability.LeaseRef{Kind: capability.UnitEval, ID: "eval", Holder: "eval-holder", Generation: 3}}}
			unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Resolved: runspec.Resolved{Engine: *unit.Engine}}}
			if err := runners.ValidateAward(unit, delivery); err != nil {
				t.Fatalf("valid independent source and eval lease: %v", err)
			}
			for name, mutate := range map[string]func(*controllerwire.ClaimedUnit){
				"missing_lease":   func(u *controllerwire.ClaimedUnit) { u.Binding.Expansion = nil },
				"missing_holder":  func(u *controllerwire.ClaimedUnit) { u.Binding.Expansion.Holder = "" },
				"source_mismatch": func(u *controllerwire.ClaimedUnit) { u.SourceGeneration++ },
				"different_lease": func(u *controllerwire.ClaimedUnit) { u.Binding.Expansion.ID = "other" },
			} {
				t.Run(string(delivery)+"/"+name, func(t *testing.T) {
					changed := unit
					lease := *unit.Binding.Expansion
					changed.Binding.Expansion = &lease
					mutate(&changed)
					if err := runners.ValidateAward(changed, delivery); err == nil {
						t.Fatal("invalid eval fence accepted")
					}
				})
			}
		}
	}
}
