package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestRunnerLabelSelectionPreservesAlternatives(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "named", true: "queued"}[queued], func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			setClaimRunnerSelection(t, s, runspec.RunnerSelection{Kind: runspec.RunnerLabel, Value: "gpu, cpu"})
			if queued {
				demand.Target = nil
				demand.Kinds = []capability.UnitKind{capability.UnitNode}
			}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil {
				t.Fatal("runner label selection accepted a missing term")
			}
			demand.Labels = append(demand.Labels, "cpu")
			if claim, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil || claim.NodeID != "job" {
				t.Fatalf("runner label alternative claim: %+v %v", claim, err)
			}
		})
	}
}
