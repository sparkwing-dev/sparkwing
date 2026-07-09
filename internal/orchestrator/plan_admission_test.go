package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPlanAdmissionFromEnv(t *testing.T) {
	t.Setenv(triggerEnvPlanAdmissionKey, "g:box-budget")
	t.Setenv(triggerEnvPlanAdmissionHolderID, "parent/-")
	t.Setenv(triggerEnvPlanAdmissions, `{"g:other":"other/-"}`)
	t.Setenv(triggerEnvPlanHostAdmission, "1")
	t.Setenv(triggerEnvPlanHostAdmissionKey, "g:box-budget")

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
	if !admission.HostAdmission {
		t.Fatalf("HostAdmission = false, want true")
	}
	if admission.HostAdmissionKey != "g:box-budget" {
		t.Fatalf("HostAdmissionKey = %q, want g:box-budget", admission.HostAdmissionKey)
	}
}

func TestPlanAdmissionProcessBoundaryThroughCommandEnv(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	out := t.TempDir() + "/admission"
	ctx := withPlanAdmission(context.Background(), planAdmission{
		Key:      "g:box-budget",
		HolderID: "parent/-",
		HolderIDs: map[string]string{
			"g:box-budget": "parent/-",
			"g:other":      "other/-",
		},
		HostAdmission:    true,
		HostAdmissionKey: "g:box-budget",
	})

	_, err = sparkwing.Bash(ctx, `"$EXE" -test.run '^TestPlanAdmissionProcessBoundaryHarness$' -- "$OUT"`).
		Env("EXE", exe).
		Env("OUT", out).
		Env("SPARKWING_PLAN_ADMISSION_TEST_HELPER", "1").
		Run()
	if err != nil {
		t.Fatalf("helper run: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read helper output: %v", err)
	}
	got := strings.TrimSpace(string(body))
	want := "g:box-budget parent/- parent/- other/- true g:box-budget"
	if got != want {
		t.Fatalf("helper admission = %q, want %q", got, want)
	}
}

func TestPlanAdmissionProcessBoundaryHarness(t *testing.T) {
	if os.Getenv("SPARKWING_PLAN_ADMISSION_TEST_HELPER") != "1" {
		t.Skip("helper only")
	}
	if len(os.Args) == 0 {
		t.Fatal("missing args")
	}
	out := os.Args[len(os.Args)-1]
	admission := planAdmissionFromEnv()
	body := admission.Key + " " + admission.HolderID + " " + admission.HolderIDs["g:box-budget"] + " " + admission.HolderIDs["g:other"]
	if admission.HostAdmission {
		body += " true"
	} else {
		body += " false"
	}
	body += " " + admission.HostAdmissionKey
	if err := os.WriteFile(out, []byte(body+"\n"), 0o644); err != nil {
		t.Fatalf("write helper output: %v", err)
	}
}
