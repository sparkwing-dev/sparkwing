package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func preflightPlan(secrets string) string {
	return `{"nodes":[{"id":"a","deps":[],"spec_hash":"` + hashA + `"}],"secrets":[` + secrets + `]}`
}

// Every required secret the run cannot read is named in one refusal, each
// with the scope it is missing from and the command that sets it, and no node
// of the refused plan is created.
func TestAcceptPlan_SecretPreflightNamesEveryUnreadableSecret(t *testing.T) {
	f := newDispatchRun(t, "run-preflight")
	now := time.Now()
	for _, sec := range []store.Secret{
		{Name: "TEAM_ONLY", Value: "v"},
		{Name: "ELSEWHERE", Value: "v", Pipeline: "release"},
		{Name: "OWN", Value: "v", Pipeline: "demo"},
		{Name: "SHARED", Value: "v", Shared: true},
	} {
		if err := f.s.CreateOrReplaceSecret(sec, now); err != nil {
			t.Fatal(err)
		}
	}
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	_, err := f.accept(plan, preflightPlan(`{"name":"OWN"},{"name":"SHARED","required":true},{"name":"ABSENT"},`+
		`{"name":"TEAM_ONLY"},{"name":"ELSEWHERE"},{"name":"MAYBE","optional":true}`))
	if !errors.Is(err, store.ErrPlanInvalid) {
		t.Fatalf("accept = %v, want a refusal wrapping ErrPlanInvalid", err)
	}
	t.Logf("refusal:\n%s", err)
	msg := err.Error()
	for _, want := range []string{
		`pipeline "demo" did not start: it declares 3 required secrets this run cannot read.`,
		`- ABSENT is not set: pipeline "demo" has no copy and the team has no shared one.`,
		`Fix: sparkwing secrets set --name ABSENT --file <path> --profile <profile> --pipeline demo`,
		`- TEAM_ONLY is set team-wide but not shared with runs, so only an admin can read it.`,
		`Fix: sparkwing secrets set --name TEAM_ONLY --file <path> --profile <profile> --shared`,
		`or: sparkwing secrets set --name TEAM_ONLY --file <path> --profile <profile> --pipeline demo`,
		`- ELSEWHERE is set only for pipeline "release"; pipeline "demo" has no copy and the team has no shared one.`,
		"`sw:\"NAME,optional\"`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q:\n%s", want, msg)
		}
	}
	for _, readable := range []string{"- OWN", "- SHARED", "- MAYBE"} {
		if strings.Contains(msg, readable) {
			t.Errorf("refusal names %s, which the run can read or can go without:\n%s", readable, msg)
		}
	}
	if _, err := f.s.GetNode(t.Context(), f.run, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("node of the refused plan: err = %v, want not found", err)
	}
	if _, err := f.report(plan, store.AttemptReport{Outcome: "failed", Error: msg}); err != nil {
		t.Fatalf("report the refusal: %v", err)
	}
	f.wantRun(t, "failed")
}

// An optional secret that is missing does not hold the run back.
func TestAcceptPlan_SecretPreflightAllowsAMissingOptionalSecret(t *testing.T) {
	f := newDispatchRun(t, "run-optional")
	if err := f.s.CreateOrReplaceSecret(store.Secret{Name: "OWN", Value: "v", Pipeline: "demo"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.mustAccept(t, preflightPlan(`{"name":"OWN"},{"name":"DEPLOY_KEY","optional":true}`))
	f.wantReleased(t, "a", true)
}
