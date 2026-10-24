package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// TestOnlyOwnerSnapshotFunding checks projection funding on the selected dialect.
func (s *Store) TestOnlyOwnerSnapshotFunding(t *testing.T) {
	t.Helper()
	demand := s.TestOnlyUnitDemand(t)
	now := s.now()
	s.SetTestClock(t, func() time.Time {
		now = now.Add(time.Microsecond)
		return now
	})
	s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	root := authorityRoot("metered-owner", "a")
	root.Grants = []capability.Grant{capability.TriggersClaim, capability.NodesClaim}
	root.RootMetered = true
	authorityPersist(t, s, root)
	fundUnitClaim(t, s)
	demand.CapabilityPrefix = root.Prefix
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}
	owner, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	s.testOnlyExec(t, `UPDATE nodes SET ready_at=NULL,spec_hash='declaration',execution_json=?`, []byte(`{}`))
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
	if _, err := s.db.ExecContext(t.Context(), `UPDATE credit_grants SET amount_micro=(SELECT SUM(amount_micro) FROM credit_charges WHERE team='a') WHERE team='a'`); err != nil {
		t.Fatal(err)
	}
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("exhausted fixture balance=%d error=%v", balance, err)
	}
	var ordinal int64
	if err := s.db.QueryRow(`SELECT ordinal FROM claim_awards WHERE singleton=1`).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	for _, funded := range []bool{false, true} {
		if funded {
			if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 100000000, Reference: "projection-restored", CreatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
		}
		snapshot, err := tenant.ReadOwnerSnapshot(t.Context(), ref, "run")
		if err != nil {
			t.Fatal(err)
		}
		if got := OwnerCanExecute(owner)(snapshot.Nodes[0]); got != funded {
			t.Fatalf("funded=%v owner execution=%v", funded, got)
		}
	}
	var after int64
	if err := s.db.QueryRow(`SELECT ordinal FROM claim_awards WHERE singleton=1`).Scan(&after); err != nil || after != ordinal {
		t.Fatalf("snapshot changed scheduling: ordinal=%d -> %d error=%v", ordinal, after, err)
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || node.ReadyAt != nil || node.ClaimedBy != "" || node.CapabilityPrefix != "" {
		t.Fatalf("snapshot released or awarded the job: %+v error=%v", node, err)
	}
}
