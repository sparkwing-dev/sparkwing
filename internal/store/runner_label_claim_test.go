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

func TestCloudSizeLabelsRequireCloudLauncher(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "named", true: "queued"}[queued], func(t *testing.T) {
			s, demand, _ := meteredUnitFixture(t, true)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"requires":{"set":true,"value":["sparkwing-16cpu"]}}`))
			indexFixtureUnit(t, s, "a", *demand.Target)
			rates := CreditRateTable{{Cores: 16, MicroPerSecond: 10}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &rates}); err != nil {
				t.Fatal(err)
			}
			if queued {
				demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
			}
			launcher := demand.CapabilityPrefix
			demand.CapabilityPrefix, demand.Labels = "runner", []string{"sparkwing-16cpu"}
			if claim, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil || claim.NodeID != "" {
				t.Fatalf("self-hosted runner received Cloud work: %+v %v", claim, err)
			}
			demand.CapabilityPrefix, demand.Labels = launcher, nil
			if claim, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil || claim.NodeID != "job" {
				t.Fatalf("Cloud launcher did not receive Cloud work: %+v %v", claim, err)
			}
		})
	}
}
