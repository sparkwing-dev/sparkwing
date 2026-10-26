package store

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestOverflowUsageLookupIndexesTeamAndRequester(t *testing.T) {
	s := authorityStore(t)
	var id, parent, unused int
	var plan string
	if err := s.db.QueryRow("EXPLAIN QUERY PLAN SELECT reservation_id FROM billing_awards WHERE team=? AND requester_account=?", "a", "requester").Scan(&id, &parent, &unused, &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "team=? AND requester_account=?") {
		t.Fatalf("usage lookup scans other requesters: %s", plan)
	}
}

func TestOverflowUsageSurvivesRunDeletionAndIdentityReuse(t *testing.T) {
	overflowRetainedUsage(t, "requester", true)
}

func FuzzOverflowUsageRetainsAccountAttribution(f *testing.F) {
	f.Add([]byte("requester"), true)
	f.Add([]byte{0, 255, 39}, false)
	f.Fuzz(func(t *testing.T, name []byte, account bool) {
		if len(name) > 64 {
			return
		}
		overflowRetainedUsage(t, "requester-"+hex.EncodeToString(name), account)
	})
}

func overflowRetainedUsage(t *testing.T, name string, account bool) {
	t.Helper()
	s, demand, now := meteredUnitFixture(t, true)
	owner := authorityMember(t, s, name, RoleOwner)
	other := authorityMember(t, s, name+"-other", RoleEditor)
	kind := capability.SubjectMachine
	want := int64(0)
	if account {
		kind, want = capability.SubjectAccount, 70
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET created_principal=?,created_subject_kind=? WHERE team='a' AND id='run'`, name, kind)
	fixtureRunWriter(t, s, "run", runspec.PrincipalWriter(string(kind), name))
	claim, _ := meteredUnitClaim(t, s, demand)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	for range 7 {
		*now = now.Add(10 * time.Second)
		if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	check := func(stage string) {
		t.Helper()
		for _, expected := range []struct {
			authority capability.AuthorityRef
			seconds   int64
		}{{owner.Authority, want}, {other.Authority, 0}} {
			control, err := tenant.ReadOverflowControl(t.Context(), expected.authority)
			if err != nil || control.UsedSeconds != expected.seconds {
				t.Fatalf("%s: usage=%d want=%d error=%v", stage, control.UsedSeconds, expected.seconds, err)
			}
		}
	}
	check("before deletion")
	if err := tenant.RemoveRunForTest(t.Context(), claim.RunID); err != nil {
		t.Fatal(err)
	}
	check("after deletion")
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_subject_kind,created_at,log_retention_class,log_retention_days) VALUES ('a','run',999,'replacement','pending',?,'replacement',1,'gh:1','host','execute',?,'account',1,'default',1)`, []byte(`{}`), name+"-other")
	check("after run identity reuse")
}

// A member's overflow budget is checked at every Cloud renewal against the reservation that renewal adds: a one-minute
// allowance lets the job run the minute it prepaid at award, no renewal extends its lease past that minute, and the
// job then ends naming the allowance rather than as a generic lapse.
func TestOverflowBudgetBoundsARunningJobAtRenewal(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	owner := authorityMember(t, s, "owner", RoleOwner)
	policy := runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}}
	body := sealWith(t, runspec.SealedRequest{Request: runspec.Request{Burst: &policy, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}}, assertedProvenance(t, "ci", "", ""))
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,created_principal='owner',created_subject_kind='account' WHERE team='a' AND id='run'`, body)
	fixtureRunWriter(t, s, "run", runspec.PrincipalWriter("account", "owner"))
	limit := int64(1)
	if err := (&Tenant{s: s, team: "a"}).SetOverflowLimit(t.Context(), owner.Authority, &limit); err != nil {
		t.Fatal(err)
	}
	claim, _ := meteredUnitClaim(t, s, demand)
	start := *now
	for _, at := range []time.Duration{0, 20 * time.Second, 40 * time.Second, 55 * time.Second} {
		*now = start.Add(at)
		beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
		if err != nil || beat.CancelRequested {
			t.Fatalf("renewal at %s inside the prepaid minute = %+v, %v", at, beat, err)
		}
		if !beat.ExpiresAt.Equal(start.Add(time.Minute)) {
			t.Fatalf("renewal at %s leased until %s; want the prepaid minute's end %s", at, beat.ExpiresAt, start.Add(time.Minute))
		}
	}
	var reserved int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(seconds),0) FROM credit_charges WHERE kind IN ('reservation','slice')`).Scan(&reserved); err != nil || reserved != limit*60 {
		t.Fatalf("reserved %d seconds under a %d-minute allowance: %v", reserved, limit, err)
	}
	var reason string
	if err := s.db.QueryRow(`SELECT stop_reason FROM billing_awards WHERE team='a'`).Scan(&reason); err != nil || reason != overflowAllowanceReached {
		t.Fatalf("declined renewal recorded %q, %v; want the overflow allowance", reason, err)
	}
	*now = start.Add(time.Minute + time.Second)
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "overflow", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil {
			t.Fatal(err)
		}
	}
	row, err := (&Tenant{s: s, team: "a"}).ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.Outcome != runstate.NodeCancelled || row.CancelReason != overflowAllowanceReached {
		t.Fatalf("job after its prepaid minute = outcome %q reason %q, %v; want stopped at the overflow allowance", row.Outcome, row.CancelReason, err)
	}
}
