package orchestrator

import "testing"

func TestPlanAdmissionFromEnv(t *testing.T) {
	t.Setenv(triggerEnvPlanAdmissionKey, "g:box-budget")
	t.Setenv(triggerEnvPlanAdmissionHolderID, "parent/-")
	t.Setenv(triggerEnvPlanAdmissions, `{"g:other":"other/-"}`)

	admission := planAdmissionFromEnv()
	if admission.Key != "g:box-budget" {
		t.Fatalf("Key = %q, want g:box-budget", admission.Key)
	}
	if admission.HolderID != "parent/-" {
		t.Fatalf("HolderID = %q, want parent/-", admission.HolderID)
	}
	if got := admission.HolderIDs["g:box-budget"]; got != "parent/-" {
		t.Fatalf("canonical holder = %q, want parent/-", got)
	}
	if got := admission.HolderIDs["g:other"]; got != "other/-" {
		t.Fatalf("additional holder = %q, want other/-", got)
	}
}
