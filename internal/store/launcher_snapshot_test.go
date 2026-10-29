package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func launcherSnapshotFixture(t *testing.T, kind capability.UnitKind, previousAttempts ...int64) (*Store, UnitDemand, UnitClaim, *time.Time) {
	t.Helper()
	return launcherSnapshotFixtureExpiry(t, kind, 2*time.Hour, previousAttempts...)
}

func launcherSnapshotFixtureExpiry(t *testing.T, kind capability.UnitKind, lifetime time.Duration, previousAttempts ...int64) (*Store, UnitDemand, UnitClaim, *time.Time) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	now := s.now()
	s.clock = func() time.Time { return now }
	root := authorityRoot("launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	expires := now.Add(lifetime)
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
		rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('a','run','job',7,'pending')`)
	}
	if len(previousAttempts) > 0 {
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET attempt=?`, previousAttempts[0])
		if kind == capability.UnitEval && previousAttempts[0] > 0 {
			rewriteSchemaExec(t, s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','run','job',?,7,'source-runner','source-capability','runner','source-runner','remote',1,2,'success')`, previousAttempts[0])
		}
	}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
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
				awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil)
				if err != nil || len(awards) != 1 {
					t.Fatalf("awards=%+v, %v", awards, err)
				}
				award := awards[0]
				if award.Claim.Award != claim.Award || award.Claim.Owner != claim.Owner || award.Claim.SourceGeneration != claim.SourceGeneration || award.Claim.Kind != kind || award.Claim.Team != claim.Team || len(award.Claim.Bearer) != 0 || award.Capability.Parent == nil || *award.Capability.Parent != ref || award.Run.ID != claim.RunID || award.ParentCapabilityExpiry == nil {
					t.Fatalf("changed or unsafe award: %+v", award)
				}
				if kind == capability.UnitNode && (award.Node.Ref.NodeID != runspec.NodeID(claim.NodeID) || award.Node.Generation != claim.Award.Generation) {
					t.Fatalf("missing persisted job: %+v", award.Node)
				}
				if exchange == 0 {
					issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
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
	if testing.Short() {
		t.Skip("slow: 1.5s measured in the short class; the full suite runs it")
	}
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
					rewriteSchemaRefuses(t, s.db, `UPDATE `+table+` SET capability_prefix='' WHERE `+predicate)
					return true
				case 3:
					query, args = `UPDATE `+table+` SET lease_expires_at=? WHERE `+predicate, []any{now.UnixNano() - int64(offset)}
				case 4:
					query = `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`
				case 5:
					query = `UPDATE runs SET status='success',finished_at=1 WHERE id='run'`
				case 6:
					if kind == capability.UnitNode {
						query = `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE run_id='run'`
					} else {
						query = `UPDATE ` + table + ` SET status='done',finished_at=1,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,claimed_at=NULL WHERE ` + predicate
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
				awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil)
				rewriteSchemaExec(t, s.db, `UPDATE runs SET status='running',finished_at=NULL,cancel_requested_at=NULL WHERE id='run'`)
				rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=? WHERE id='run'`, now.Add(time.Hour).UnixNano())
				status := "claimed"
				if kind == capability.UnitNode {
					status = "pending"
				}
				extra := ",claimed_at=1,finished_at=NULL"
				if kind == capability.UnitNode {
					extra = ",outcome='',finished_at=NULL"
				}
				rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=?,claimed_by=?,capability_prefix=?,lease_expires_at=?,status=?`+extra+` WHERE `+predicate, claim.Award.Generation, claim.Award.Holder, claim.Award.CapabilityPrefix, claim.Award.LeaseExpiresAt.UnixNano(), status)
				if kind == capability.UnitEval {
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
			awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil)
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
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: other.Prefix}, nil, nil, nil, nil, nil)
	if err != nil || len(awards) != 0 {
		t.Fatalf("foreign awards=%+v, %v", awards, err)
	}
	for _, ref := range []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: "runner"}, {Kind: capability.AuthoritySession, ID: "missing"}, {}} {
		if rows, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil); err == nil || rows != nil {
			t.Fatalf("invalid authority returned awards: %+v, %v", rows, err)
		}
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=? WHERE prefix=?`, now.UnixNano(), ref.ID)
	if rows, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil); err == nil || rows != nil {
		t.Fatal("revoked launcher retained snapshot authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, _, err := s.AsOperator().ReconcileLauncherAwards(ctx, ref, nil, nil, nil, nil, nil)
	if err == nil || rows != nil {
		t.Fatal("cancelled read exposed a partial snapshot")
	}
}

func TestLauncherSnapshotDelegationRetainsRootOwnership(t *testing.T) {
	s, demand, _, _ := launcherSnapshotFixture(t, capability.UnitNode)
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	expires := s.now().Add(time.Hour)
	issued, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: root, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil)
	if err != nil || len(awards) != 1 || *awards[0].Capability.Parent != root {
		t.Fatalf("delegated launcher lost root-owned award: %+v, %v", awards, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, root.ID)
	if rows, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil); err == nil || rows != nil {
		t.Fatal("revoked ancestor retained delegated snapshot authority")
	}
}

func TestLauncherSnapshotIsolatesIntegrityButRejectsStorageFailures(t *testing.T) {
	for _, test := range []struct {
		query  string
		args   []any
		refuse bool
	}{
		{query: `UPDATE nodes SET execution_json=? WHERE run_id='run'`, args: []any{[]byte(`{`)}},
		{query: `UPDATE nodes SET status='running',attempt=0 WHERE run_id='run'`, refuse: true},
		{query: `DROP TABLE eval_units`},
	} {
		t.Run(test.query, func(t *testing.T) {
			s, demand, _, _ := launcherSnapshotFixture(t, capability.UnitNode)
			if test.refuse {
				rewriteSchemaRefuses(t, s.db, test.query, test.args...)
				return
			}
			rewriteSchemaExec(t, s.db, test.query, test.args...)
			rows, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, nil, nil, nil, nil, nil)
			if test.query == `DROP TABLE eval_units` {
				if err == nil || rows != nil {
					t.Fatalf("storage failure returned snapshot: %+v, %v", rows, err)
				}
			} else {
				var integrity *apierr.Error
				if err != nil || len(rows) != 1 || !errors.As(rows[0].Failure, &integrity) || integrity.Code != apierr.IntegrityFailure || rows[0].Fence.ID == "" {
					t.Fatalf("corrupt award lost its typed integrity failure or identity: %+v, %v", rows, err)
				}
			}
		})
	}
}

func TestLauncherReservationOwnershipProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 4.3s measured in the short class; the full suite runs it")
	}
	check := func(kindIndex, mode uint8, offset uint16, delegated bool) bool {
		kind := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}[kindIndex%3]
		s, demand, claim, now := launcherSnapshotFixture(t, kind)
		defer s.Close()
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		if delegated {
			expires := now.Add(time.Hour)
			issued, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: ref, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}, ExpiresAt: &expires})
			if err != nil {
				t.Fatal(err)
			}
			ref.ID = issued.Capability.Prefix
		}
		fence := capability.LeaseRef{Kind: kind, ID: claim.RunID, Holder: claim.Award.Holder, Generation: int64(claim.Award.Generation)}
		var id string
		if err := s.db.QueryRowContext(t.Context(), `SELECT unit_id FROM capabilities WHERE prefix=?`, claim.Award.CapabilityPrefix).Scan(&id); err != nil {
			t.Fatal(err)
		}
		fence.ID = id
		original := fence
		live, refused := true, false
		switch mode % 11 {
		case 1:
			*now = claim.Award.LeaseExpiresAt.Add(time.Nanosecond)
			live = false
		case 2:
			if _, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err != nil {
				t.Fatal(err)
			}
		case 3:
			rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, now.UnixNano())
			live = false
		case 4:
			table := map[capability.UnitKind]string{capability.UnitPlan: "triggers", capability.UnitEval: "eval_units", capability.UnitNode: "nodes"}[kind]
			rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=generation+1`)
			live = false
		case 5:
			fence.Holder += "-foreign"
			refused = true
		case 6:
			fence.Generation += int64(offset) + 1
			refused = true
		case 7:
			fence.ID += "-foreign"
			refused = true
		case 8:
			fence.Kind = capability.UnitTrigger
			refused = true
		case 9:
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET parent_id='foreign' WHERE prefix=?`, claim.Award.CapabilityPrefix)
			refused = true
		case 10:
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET subject_id='foreign' WHERE prefix=?`, claim.Award.CapabilityPrefix)
			refused = true
		}
		reservations := []capability.LeaseRef{fence}
		if refused {
			reservations = append([]capability.LeaseRef{original}, reservations...)
		}
		awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, reservations, nil, nil, nil)
		if refused {
			return err != nil && awards == nil
		}
		if err != nil || len(awards) != boolInt(live) {
			t.Logf("kind=%s mode=%d delegated=%v awards=%+v err=%v", kind, mode%11, delegated, awards, err)
			return false
		}
		return true
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 90}); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherPreparationRefusesEvalWithoutLease(t *testing.T) {
	s, demand, claim, _ := launcherSnapshotFixture(t, capability.UnitEval)
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil, nil)
	if err != nil || len(awards) != 1 {
		t.Fatalf("initial award: %v", err)
	}
	c := awards[0].Capability
	c.Binding.Expansion = nil
	body, err := json.Marshal(CapabilityRecord{Version: 1, Value: c})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET authority_json=?,eval_unit_generation=NULL WHERE prefix=?`, body, c.Prefix)
	fence := capability.LeaseRef{Kind: c.Unit, ID: c.UnitID, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
	awards, prepared, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, []capability.LeaseRef{fence}, nil, nil)
	if err != nil || len(prepared) != 0 || len(awards) != 1 || awards[0].Failure == nil {
		t.Fatalf("source generation was accepted as an eval lease: awards=%+v prepared=%+v err=%v", awards, prepared, err)
	}
	var expires int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT lease_expires_at FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`, claim.Team, claim.RunID, claim.NodeID, claim.SourceGeneration).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires != claim.Award.LeaseExpiresAt.UnixNano() {
		t.Fatal("invalid eval lease changed persisted preparation")
	}
}

func TestLauncherSnapshotCorruptAwardDoesNotHideHealthySibling(t *testing.T) {
	s, demand, _, _ := launcherSnapshotFixture(t, capability.UnitNode)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,run_id,'sibling','pending',1,spec_json,execution_json FROM nodes WHERE node_id='job'`)
	demand.Target.NodeID = "sibling"
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE node_id='job'`, []byte(`{`))
	rows, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, nil, nil, nil, nil, nil)
	if err != nil || len(rows) != 2 {
		t.Fatalf("snapshot=%+v, %v", rows, err)
	}
	live, deferred := 0, 0
	for _, row := range rows {
		if row.Failure != nil {
			deferred++
		} else if row.Claim.NodeID == "sibling" {
			live++
		}
	}
	if live != 1 || deferred != 1 {
		t.Fatalf("healthy=%d deferred=%d", live, deferred)
	}
}
