package store

import (
	"context"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func launcherSnapshotFixture(t *testing.T, kind capability.UnitKind) (*Store, UnitDemand, UnitClaim, *time.Time) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	now := s.now()
	s.clock = func() time.Time { return now }
	root := authorityRoot("launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	expires := now.Add(2 * time.Hour)
	root.ExpiresAt = &expires
	authorityPersist(t, s, root)
	demand.CapabilityPrefix = root.Prefix
	demand.Target = &UnitRef{Kind: kind, RunID: "run"}
	if kind == capability.UnitNode {
		demand.Target.NodeID = "job"
	}
	if kind != capability.UnitNode {
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	}
	if kind == capability.UnitEval {
		demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
		rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, now.Add(time.Hour).UnixNano())
		rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',7,'pending',3)`)
	}
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	return s, demand, claim, &now
}

func TestLauncherSnapshotRetainsCurrentAwardThroughExchange(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, _ := launcherSnapshotFixture(t, kind)
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
			for exchange := range 2 {
				awards, err := s.ReadLauncherAwards(t.Context(), ref)
				if err != nil || len(awards) != 1 {
					t.Fatalf("awards=%+v, %v", awards, err)
				}
				award := awards[0]
				if award.Claim.Award != claim.Award || award.Claim.Owner != claim.Owner || award.Claim.SourceGeneration != claim.SourceGeneration || award.Claim.Kind != kind || award.Claim.Team != claim.Team || len(award.Claim.Bearer) != 0 || award.Capability.Parent == nil || *award.Capability.Parent != ref || award.Run.ID != claim.RunID || award.ParentCapabilityExpiry == nil {
					t.Fatalf("changed or unsafe award: %+v", award)
				}
				if kind == capability.UnitNode && (award.Node.Ref.NodeID != claim.NodeID || award.Node.Generation != claim.Award.Generation) {
					t.Fatalf("missing persisted job: %+v", award.Node)
				}
				if exchange == 0 {
					issued, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
					if err != nil {
						t.Fatal(err)
					}
					claim.Award.CapabilityPrefix = issued.Capability.Prefix
				} else if award.Capability.Delivery != capability.DeliveryDirect {
					t.Fatal("snapshot did not read the exchanged credential")
				}
			}
		})
	}
}

func TestLauncherSnapshotFencingProperty(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, now := launcherSnapshotFixture(t, kind)
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
			table, predicate := "triggers", `id='run'`
			if kind == capability.UnitNode {
				table, predicate = "nodes", `run_id='run'`
			} else if kind == capability.UnitEval {
				table, predicate = "eval_units", `run_id='run'`
			}
			check := func(change uint8, offset uint16) bool {
				query, args := "", []any{}
				switch change % 9 {
				case 0:
					query, args = `UPDATE `+table+` SET generation=generation+? WHERE `+predicate, []any{int64(offset) + 1}
				case 1:
					query = `UPDATE ` + table + ` SET claimed_by='another-holder' WHERE ` + predicate
				case 2:
					query = `UPDATE ` + table + ` SET capability_prefix='' WHERE ` + predicate
				case 3:
					query, args = `UPDATE `+table+` SET lease_expires_at=? WHERE `+predicate, []any{now.UnixNano() - int64(offset)}
				case 4:
					query = `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`
				case 5:
					query = `UPDATE runs SET status='success',finished_at=1 WHERE id='run'`
				case 6:
					if kind == capability.UnitEval {
						query = `UPDATE leases SET generation=generation+1 WHERE name='controller.owner'`
					} else if kind == capability.UnitNode {
						query = `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE run_id='run'`
					} else {
						query = `UPDATE ` + table + ` SET status='done',finished_at=1 WHERE ` + predicate
					}
				case 7:
					if kind == capability.UnitEval {
						query = `UPDATE nodes SET expansion='accepted' WHERE run_id='run'`
					} else {
						query = `UPDATE ` + table + ` SET cancel_requested_at=1 WHERE ` + predicate
					}
				case 8:
					query, args = `UPDATE triggers SET expires_at=? WHERE id='run'`, []any{now.UnixNano() - int64(offset)}
				}
				if _, err := s.db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
				awards, err := s.ReadLauncherAwards(t.Context(), ref)
				rewriteSchemaExec(t, s.db, `UPDATE runs SET status='running',finished_at=NULL,cancel_requested_at=NULL WHERE id='run'`)
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=? WHERE id='run'`, now.Add(time.Hour).UnixNano())
				status := "claimed"
				if kind == capability.UnitNode {
					status = "pending"
				}
				extra := ""
				if kind == capability.UnitNode {
					extra = ",outcome='',finished_at=NULL"
				}
				rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=?,claimed_by=?,capability_prefix=?,lease_expires_at=?,status=?`+extra+` WHERE `+predicate, claim.Award.Generation, claim.Award.Holder, claim.Award.CapabilityPrefix, claim.Award.LeaseExpiresAt.UnixNano(), status)
				if kind == capability.UnitEval {
					rewriteSchemaExec(t, s.db, `UPDATE leases SET generation=3 WHERE name='controller.owner'`)
					rewriteSchemaExec(t, s.db, `UPDATE nodes SET expansion='expected' WHERE run_id='run'`)
				} else {
					rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET finished_at=NULL,cancel_requested_at=NULL WHERE `+predicate)
				}
				if err != nil || len(awards) != 0 {
					t.Logf("change=%d, offset=%d, awards=%+v, err=%v", change%9, offset, awards, err)
					return false
				}
				return true
			}
			if err := quick.Check(check, &quick.Config{MaxCount: 100}); err != nil {
				t.Fatal(err)
			}
			awards, err := s.ReadLauncherAwards(t.Context(), ref)
			if err != nil || len(awards) != 1 {
				t.Fatalf("property damaged live award: %+v, %v", awards, err)
			}
		})
	}
}

func TestLauncherSnapshotAuthority(t *testing.T) {
	s, demand, _, now := launcherSnapshotFixture(t, capability.UnitNode)
	other := authorityRoot("other", "")
	other.Subject.Kind = capability.SubjectService
	other.Subject.ID = "launcher"
	other.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, other)
	awards, err := s.ReadLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: other.Prefix})
	if err != nil || len(awards) != 0 {
		t.Fatalf("foreign awards=%+v, %v", awards, err)
	}
	for _, ref := range []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: "runner"}, {Kind: capability.AuthoritySession, ID: "missing"}, {}} {
		if rows, err := s.ReadLauncherAwards(t.Context(), ref); err == nil || rows != nil {
			t.Fatalf("invalid authority returned awards: %+v, %v", rows, err)
		}
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=? WHERE prefix=?`, now.UnixNano(), ref.ID)
	if rows, err := s.ReadLauncherAwards(t.Context(), ref); err == nil || rows != nil {
		t.Fatal("revoked launcher retained snapshot authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := s.ReadLauncherAwards(ctx, ref)
	if err == nil || rows != nil {
		t.Fatal("cancelled read exposed a partial snapshot")
	}
}

func TestLauncherSnapshotDelegationRetainsRootOwnership(t *testing.T) {
	s, demand, _, _ := launcherSnapshotFixture(t, capability.UnitNode)
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	expires := s.now().Add(time.Hour)
	issued, err := s.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: root, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}
	awards, err := s.ReadLauncherAwards(t.Context(), ref)
	if err != nil || len(awards) != 1 || *awards[0].Capability.Parent != root {
		t.Fatalf("delegated launcher lost root-owned award: %+v, %v", awards, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, root.ID)
	if rows, err := s.ReadLauncherAwards(t.Context(), ref); err == nil || rows != nil {
		t.Fatal("revoked ancestor retained delegated snapshot authority")
	}
}

func TestLauncherSnapshotDiscardsStorageAndIntegrityFailures(t *testing.T) {
	for _, query := range []string{`UPDATE nodes SET execution_json='{' WHERE run_id='run'`, `DROP TABLE eval_units`} {
		t.Run(query, func(t *testing.T) {
			s, demand, _, _ := launcherSnapshotFixture(t, capability.UnitNode)
			rewriteSchemaExec(t, s.db, query)
			if rows, err := s.ReadLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}); err == nil || rows != nil {
				t.Fatalf("storage failure returned a snapshot: %+v, %v", rows, err)
			}
		})
	}
}
