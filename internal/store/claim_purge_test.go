package store

import "testing"

func TestTenantPurgeRemovesLabeledWorkBeforeItsRequirements(t *testing.T) {
	s, _ := unitClaimFixture(t)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		id, err := persistClaimRequirementsTx(t.Context(), tx, "a", []string{"sparkwing-4cpu"})
		if err != nil {
			return err
		}
		if _, err = tx.execContext(t.Context(), `UPDATE nodes SET claim_requirements=? WHERE team='a'`, id); err != nil {
			return err
		}
		_, err = tx.execContext(t.Context(), `UPDATE triggers SET claim_requirements=? WHERE team='a'`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().deleteTenantRows(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"runs", "nodes", "triggers", "claim_requirements", "claim_requirement_terms"} {
		rewriteSchemaCount(t, s.db, table, 0)
	}
}
