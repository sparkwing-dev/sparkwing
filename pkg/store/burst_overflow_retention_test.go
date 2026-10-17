package store

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

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
	claim, _ := meteredUnitClaim(t, s, demand)
	if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	for range 7 {
		*now = now.Add(10 * time.Second)
		if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
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
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_subject_kind,created_at,log_retention_class,log_retention_days) VALUES ('a','run',999,'replacement','pending','{}','replacement',1,'gh:1','host','execute','[]',?,'account',1,'default',1)`, name+"-other")
	check("after run identity reuse")
}
