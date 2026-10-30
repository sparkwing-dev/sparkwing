package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLauncherSnapshotAcrossTeamsInObservationOrder(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, first, _ := launcherSnapshotFixture(t, kind)
			rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b','other',number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE id='run'`)
			rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT 'b','other',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`)
			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json,generation,outcome,finished_at,expansion) SELECT 'b','other',node_id,status,ready_at,spec_json,execution_json,generation,outcome,finished_at,expansion FROM nodes WHERE run_id='run'`)
			if kind == capability.UnitEval {
				rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status) VALUES ('b','other','job',7,'pending')`)
			}
			if _, err := (&Tenant{s: s, team: "b"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 100000000, Reference: "other-team", CreatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
			demand.Target.RunID = "other"
			indexFixtureUnit(t, s, "b", *demand.Target)
			second, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			var observed []capability.LauncherObservation
			for _, claim := range []UnitClaim{second, first} {
				principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				observed = append(observed, capability.LauncherObservation{Award: capability.LeaseRef{Kind: kind, ID: principal.Capability.UnitID, Holder: claim.Award.Holder, Generation: int64(claim.Award.Generation)}})
			}
			root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
			for _, cancelled := range []bool{false, true} {
				if cancelled {
					rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1 WHERE team='b' AND id='other'`)
				}
				for _, hosts := range [][]capability.LauncherObservation{observed, nil} {
					awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, hosts, nil, nil, nil, nil)
					if err != nil {
						t.Fatalf("snapshot cancelled=%v global=%v: %v", cancelled, hosts == nil, err)
					}
					live, gone := 0, 0
					for _, award := range awards {
						if award.Failure != nil {
							t.Fatalf("award deferred: %v", award.Failure)
						}
						if award.Gone {
							gone++
						} else {
							live++
						}
					}
					wantLive, wantGone := 2, 0
					if cancelled {
						wantLive = 1
						if hosts != nil {
							wantGone = 1
						}
					}
					if live != wantLive || gone != wantGone {
						t.Fatalf("cancelled=%v global=%v: live=%d gone=%d; want %d/%d", cancelled, hosts == nil, live, gone, wantLive, wantGone)
					}
				}
			}
		})
	}
}
