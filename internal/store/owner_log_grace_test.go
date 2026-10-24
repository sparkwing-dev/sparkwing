package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestCompletedOwnerLogsRetainOnlyTheirFinalTail(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			for _, change := range []string{"tail", "deadline", "late", "holder", "generation", "revoked parent"} {
				t.Run(change, func(t *testing.T) {
					s, tenant, commit := maintenanceOwnerLogFixture(t, kind)
					finished := s.now()
					table, filter := "triggers", "id='run'"
					if kind == capability.UnitEval {
						table, filter = "eval_units", "run_id='run'"
					}
					rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET status='done',finished_at=?,claimed_by=NULL,capability_prefix='',claimed_at=NULL,lease_expires_at=NULL WHERE "+filter, finished.UnixNano())
					s.clock = func() time.Time { return finished.Add(limits.LogSealGrace - time.Nanosecond) }
					switch change {
					case "deadline":
						s.clock = func() time.Time { return finished.Add(limits.LogSealGrace) }
					case "late":
						s.clock = func() time.Time { return finished.Add(limits.LogSealGrace + time.Nanosecond) }
					case "holder":
						commit.Actor.Holder = "another-holder"
					case "generation":
						rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET generation=generation+1 WHERE "+filter)
					case "revoked parent":
						rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=? WHERE prefix='machine'", s.now().UnixNano())
					}
					_, signErr := tenant.PrepareLogWrite(t.Context(), commit.Subject, commit.Actor)
					commit.Final = true
					writeErr := tenant.RecordSegments(t.Context(), commit)
					if allowed := change == "tail"; (signErr == nil) != allowed || (writeErr == nil) != allowed {
						t.Fatalf("sign=%v write=%v allowed=%v", signErr, writeErr, allowed)
					}
					if change == "tail" {
						reader := authorityMember(t, s, "tail-reader", RoleReader)
						if got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: commit.Subject}); err != nil || !got.Complete {
							t.Fatalf("tail log=%+v: %v", got, err)
						}
					}
				})
			}
		})
	}
}

func TestCompletedOwnerLogMaintenanceWaitsForFinalTail(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, commit := maintenanceOwnerLogFixture(t, kind)
			finished := s.now()
			table, filter := "triggers", "id='run'"
			if kind == capability.UnitEval {
				table, filter = "eval_units", "run_id='run'"
			}
			rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET status='done',finished_at=?,claimed_by=NULL,capability_prefix='',claimed_at=NULL,lease_expires_at=NULL WHERE "+filter, finished.UnixNano())
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "tail", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), lease, 10); err != nil || n != 0 {
				t.Fatalf("sealed before tail grace: %d %v", n, err)
			}
			err = s.inTx(t.Context(), func(tx *storeTx) error {
				p, err := resolveActorTx(t.Context(), tx, commit.Actor)
				if err != nil {
					return err
				}
				state, err := authenticationBindingTx(t.Context(), tx, p.Capability)
				if err != nil {
					return err
				}
				if state.FinishedAt == nil || !state.FinishedAt.Equal(finished) {
					t.Fatalf("owner finish absent from authentication: %+v", state)
				}
				if kind == capability.UnitEval && (state.Expansion == nil || state.Expansion.Lease.Holder != p.Capability.Binding.Expansion.Holder) {
					t.Fatalf("eval tail lost original holder: %+v", state.Expansion)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s.clock = func() time.Time { return finished.Add(limits.LogSealGrace) }
			if n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), lease, 10); err != nil || n != 1 {
				t.Fatalf("abandoned tail not sealed: %d %v", n, err)
			}
		})
	}
}

func TestPlanAcceptanceRetainsFinalLogPublication(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchController)
	var record CapabilityRecord
	var body []byte
	if err := tenant.s.db.QueryRowContext(t.Context(), "SELECT authority_json FROM capabilities WHERE prefix='planner'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.Value.Grants = append(record.Value.Grants, capability.LogTail)
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(record.Value.Grants)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, "UPDATE capabilities SET authority_json=?,grants=? WHERE prefix='planner'", body, grants)
	actor := request.Actor
	actor.Holder, actor.Generation = "host", 1
	if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
		return createLogStreamTx(t.Context(), tx, blob.LogSubject{Kind: blob.LogOwnerUnit, Team: string(tenant.team), RunID: request.RunID, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: request.RunID, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}}
	if _, err := tenant.PrepareLogWrite(t.Context(), subject, actor); err != nil {
		t.Fatalf("accepted plan lost log signing: %v", err)
	}
	if err := tenant.RecordSegments(t.Context(), SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30", Final: true}); err != nil {
		t.Fatalf("accepted plan lost final logs: %v", err)
	}
}
