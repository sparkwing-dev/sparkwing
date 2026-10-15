package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestMeteredUnpricedClaimLeavesWorkUnchanged(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		for _, queued := range []bool{false, true} {
			s, demand, _ := meteredUnitFixture(t, cloud)
			table := CreditRateTable{{Cores: 1, MicroPerSecond: 2}}
			if _, err := s.AsOperator().SetCreditSettings(t.Context(), CreditSettingsUpdate{RateTable: &table}); err != nil {
				t.Fatal(err)
			}
			demand.Capacity, demand.Available = match.Resources{Cores: 8}, match.Resources{Cores: 8}
			if queued {
				demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
			}
			_, err := s.ClaimUnit(t.Context(), demand)
			var unpriced *UnpricedCPUClassError
			if !errors.As(err, &unpriced) || unpriced.Cores != 4 || unpriced.MaxCores != 1 {
				t.Fatalf("cloud=%t queued=%t: unpriced claim=%v", cloud, queued, err)
			}
			row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), NodeRef{"run", "job"})
			if err != nil || row.Status != StatusPending || row.ClaimedBy != "" || row.CapabilityPrefix != "" || row.Attempt != 0 || row.FinishedAt != nil {
				t.Fatalf("unpriced claim changed work: %+v: %v", row, err)
			}
			for _, name := range []string{"credit_charges", "billing_awards", "billable_intervals", "node_attempts", "events"} {
				rewriteSchemaCount(t, s.db, name, 0)
			}
			if balance, err := (&Tenant{s: s, team: "a"}).CreditBalanceMicro(t.Context()); err != nil || balance != 1000000 {
				t.Fatalf("unpriced claim changed balance: %d: %v", balance, err)
			}
		}
	}
}
