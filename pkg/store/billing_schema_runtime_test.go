package store

import (
	"slices"
	"strings"
	"testing"
)

func TestBillingStateLivesOnlyInAwardLedger(t *testing.T) {
	s := authorityStore(t)
	for table, columns := range map[string][]string{
		"nodes":    {"credit_charged_through", "credit_exhausted_anchor", "credit_cpu_class", "credit_billing_from"},
		"triggers": {"credit_reserved_at", "credit_reservation_id", "credit_paid_seconds", "credit_paid_amount_micro"},
	} {
		rows, err := s.db.QueryContext(t.Context(), "SELECT * FROM "+table+" LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		actual, err := rows.Columns()
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("schema columns: %v, %v", err, closeErr)
		}
		for _, column := range columns {
			if slices.Contains(actual, column) {
				t.Errorf("retired billing state remains: %s.%s", table, column)
			}
		}
	}
}

func TestBillingAwardProjectsActiveUsageAndPrice(t *testing.T) {
	s, demand, _ := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	usage, err := s.AsOperator().ComputeUsage(t.Context())
	if err != nil || usage.Runners != 1 {
		t.Fatalf("active award usage: %+v, %v", usage, err)
	}
	node, err := (&Tenant{s: s, team: claim.Team}).GetNode(t.Context(), claim.RunID, claim.NodeID)
	if err != nil || node.CreditCPUClassCores != 4 {
		t.Fatalf("awarded price projection: %+v, %v", node, err)
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	usage, err = s.AsOperator().ComputeUsage(t.Context())
	if err != nil || usage.Runners != 0 {
		t.Fatalf("settled award usage: %+v, %v", usage, err)
	}
}

func TestBillingAwardUniqueIdentitiesAreSchemaConstraints(t *testing.T) {
	for _, identity := range []string{"capability", "unit_generation"} {
		t.Run(identity, func(t *testing.T) {
			s, demand, _ := meteredUnitFixture(t, true)
			meteredUnitClaim(t, s, demand)
			prefix, generation := "'replacement'", "generation"
			if identity == "capability" {
				prefix, generation = "capability_prefix", "generation+1"
			}
			_, err := s.db.ExecContext(t.Context(), `INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) SELECT team,'replacement',`+prefix+`,run_id,node_id,`+generation+`,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at FROM billing_awards`)
			if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed: billing_awards.team") {
				t.Fatalf("duplicate billing identity refusal: %v", err)
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 1)
		})
	}
}

func TestOnlyAFailedRefillCanBlockRefills(t *testing.T) {
	s, _, _ := refillFixture(t, true)
	r := openRefill(t, s, "a", 1_000, "payment")
	for _, update := range []string{
		`UPDATE credit_checkouts SET refill_blocked=2 WHERE id='` + r.ID + `'`,
		`UPDATE credit_checkouts SET refill_blocked=1 WHERE id='` + r.ID + `'`,
		`UPDATE credit_checkouts SET closed_at=1,failure='failed',refill_blocked=1 WHERE id='checkout'`,
	} {
		if _, err := s.db.ExecContext(t.Context(), update); err == nil {
			t.Fatalf("invalid refill block accepted: %s", update)
		}
	}
}
