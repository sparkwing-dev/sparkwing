package store_test

import (
	"encoding/json"
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
	var refused *store.SecretPreflightError
	if !errors.As(err, &refused) || !errors.Is(err, store.ErrPlanInvalid) {
		t.Fatalf("accept = %v, want a SecretPreflightError wrapping ErrPlanInvalid", err)
	}
	answered := err.Error()
	if want := `pipeline "demo" did not start: this run cannot read 3 required secrets it declares: ABSENT, TEAM_ONLY, ELSEWHERE. ` +
		`A team member sees which scope each is missing from, and the command that sets it, with: ` +
		`sparkwing runs status --run run-preflight --profile <profile>`; answered != want {
		t.Fatalf("planner's refusal =\n%s\nwant\n%s", answered, want)
	}
	events, err := f.s.ListEventsAfter(t.Context(), f.run, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var msg string
	for _, ev := range events {
		if ev.Kind == store.EventKindSecretPreflightRefused {
			var payload struct{ Message string }
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			msg = payload.Message
		}
	}
	if msg != refused.Detail() {
		t.Fatalf("recorded detail = %q, want the refusal's detail", msg)
	}
	t.Logf("team detail:\n%s", msg)
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
			t.Errorf("detail lacks %q:\n%s", want, msg)
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
	if _, err := f.report(plan, store.AttemptReport{Outcome: "failed", Error: answered}); err != nil {
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
