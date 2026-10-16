package store

import "testing"

func TestAdmissionRollsBackWhenRunCounterDoesNotAdvance(t *testing.T) {
	tenant, request, _ := intakeFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_number BEFORE UPDATE OF next_run_number ON teams BEGIN SELECT RAISE(IGNORE); END`)
	if result, err := tenant.AdmitRun(t.Context(), request); err == nil || result.RunID != "" {
		t.Fatalf("unallocated admission = %+v, %v", result, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 0)
	rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
	rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_number`)
	result, err := tenant.AdmitRun(t.Context(), request)
	if err != nil || result.Number != 1 {
		t.Fatalf("next admission = %+v, %v", result, err)
	}
}
