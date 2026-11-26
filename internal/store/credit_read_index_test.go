package store_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestClaimCreditReadsUseTheirIndexesForALongHistory(t *testing.T) {
	testClaimCreditReadIndexes(t, storetest.OpenSQLite(t), "USING COVERING INDEX idx_credit_charges_team_mode_amount", "USING INDEX idx_billing_awards_unsettled", "SCAN c", "SCAN a", "SCAN billing_awards")
}

func TestPostgresClaimCreditReadsUseTheirIndexesForALongHistory(t *testing.T) {
	testClaimCreditReadIndexes(t, storetest.OpenPostgres(t), "Index Only Scan using idx_credit_charges_team_mode_amount", "idx_billing_awards_unsettled", "Seq Scan on credit_charges", "Seq Scan on billing_awards")
}

// safety: the polling team holds most of the ledger, the shape in which these reads scanned every team's rows.
func testClaimCreditReadIndexes(t *testing.T, st *store.Store, chargeIndex, awardIndex string, scans ...string) {
	store.SeedCreditHistoryForTest(t, st, "history", 20000)
	want := map[string]string{"balance": chargeIndex, "grant split": chargeIndex, "active Cloud vCPU": awardIndex}
	for _, generic := range []bool{false, true} {
		for name, plan := range store.CreditReadPlansForTest(t, st, "history", generic) {
			scanned := slices.ContainsFunc(scans, func(scan string) bool { return strings.Contains(plan, scan) })
			if scanned || !strings.Contains(plan, want[name]) {
				t.Errorf("%s read (generic plan %t) does not use %q:\n%s", name, generic, want[name], plan)
			}
		}
	}
}

func TestSchemaUpgradeIndexesCreditReadsFromVersion42(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	store.RevertCreditIndexesForTest(t, st)
	for _, statement := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('history',1,1)`,
		`INSERT INTO billing_awards(team,reservation_id,capability_prefix,run_id,node_id,generation,payer_team,metered,executor_class,billing_start,unit_kind,billing_unit,awarded_at) VALUES ('history','res-1','cap-1','run-1','node-1',1,'history',1,'cloud','at_award','node','job',1)`,
		`INSERT INTO credit_charges(payment_mode,id,run_id,node_id,capability_prefix,kind,seconds,amount_micro,cpu_class,charged_at,team) VALUES ('test','charge-1','run-1','node-1','cap-1','reservation',60,600,2,1,'history')`,
		`UPDATE sparkwing_schema_version SET version=42, baseline_digest='0674849f24548cae23bd6c39f3634113014b66559f193af9522ed30a3dcb523d'`,
	} {
		if _, err := st.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	rows := snapshotRows(t, st, []string{`SELECT * FROM credit_charges ORDER BY id`, `SELECT * FROM billing_awards ORDER BY team, reservation_id`})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded := target.Open(t)
	fresh := storetest.New(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("schema upgrade differs: %s", shapeDiff(a, b))
	}
	if got := snapshotRows(t, upgraded, []string{`SELECT * FROM credit_charges ORDER BY id`, `SELECT * FROM billing_awards ORDER BY team, reservation_id`}); got != rows {
		t.Fatal("the upgrade changed ledger rows")
	}
}
