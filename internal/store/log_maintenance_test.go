package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestLogPersistenceMaintenanceRespectsFinalTailGrace(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	reader := authorityMember(t, s, "maintenance-log-reader", RoleReader)
	op := s.AsOperator()
	finished := s.now()
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET status='done',outcome='success',finished_at=?,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE team='a' AND run_id='run'", finished.UnixNano())
	rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=?,outcome='success' WHERE team='a' AND run_id='run'", finished.UnixNano())
	for _, elapsed := range []time.Duration{0, limits.LogSealGrace - time.Nanosecond, limits.LogSealGrace} {
		s.clock = func() time.Time { return finished.Add(elapsed) }
		if n, err := op.SealAbandonedLogStreams(t.Context(), 10); err != nil || n != 0 {
			t.Fatalf("sealed during final tail grace at %s: %d, %v", elapsed, n, err)
		}
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatalf("tail writer refused at %s: %v", elapsed, err)
		}
	}
	s.clock = func() time.Time { return finished.Add(limits.LogSealGrace + time.Nanosecond) }
	if n, err := op.SealAbandonedLogStreams(t.Context(), 10); err != nil || n != 1 {
		t.Fatalf("abandoned stream not sealed: %d, %v", n, err)
	}
	commit.Final = true
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("late writer replaced the maintenance seal")
	}
	blobAccountingDiskStore(t, s)
	got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: commit.Subject})
	if err != nil || got.Complete || got.IncompleteReason != "writer_lost" || len(got.Segments) != 1 || got.Segments[0] != commit.Segments[0] {
		t.Fatalf("abandoned stream read: %+v, %v", got, err)
	}
	if n, err := op.SealAbandonedLogStreams(t.Context(), 10); err != nil || n != 0 {
		t.Fatalf("maintenance replay: %d, %v", n, err)
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || node.Status != runstate.StatusDone || node.Outcome != runstate.NodeSuccess {
		t.Fatalf("log loss changed execution result: %+v, %v", node, err)
	}
}

func TestLogPersistenceMaintenancePreservesWritableSubjects(t *testing.T) {
	// safety: any subject writable before maintenance remains writable after it.
	property := func(kind uint8, elapsed int16) bool {
		return t.Run(fmt.Sprintf("%d/%d", kind, elapsed), func(t *testing.T) {
			var s *Store
			var tenant *Tenant
			var commit SegmentCommit
			if kind%4 == 0 {
				var actor runstate.Actor
				var attempt runspec.AttemptIdentity
				s, tenant, actor, attempt = resultFixture(t)
				commit = logCommit(actor, attempt)
				if err := tenant.RecordSegments(t.Context(), commit); err != nil {
					t.Fatal(err)
				}
				now := s.now()
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET status='done',outcome='success',finished_at=?,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE team='a' AND run_id='run'", now.UnixNano())
				rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=?,outcome='success' WHERE team='a' AND run_id='run'", now.UnixNano())
				s.clock = func() time.Time { return now.Add(limits.LogSealGrace + time.Duration(elapsed)) }
			} else {
				unit := []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval}[(kind%4)-1]
				s, tenant, commit = maintenanceOwnerLogFixture(t, unit)
				table, filter := "triggers", "team='a' AND id='run'"
				if unit == capability.UnitEval {
					table, filter = "eval_units", "team='a' AND run_id='run'"
				}
				rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET lease_expires_at=? WHERE "+filter, s.now().Add(time.Duration(elapsed)).UnixNano())
			}
			writable := tenant.RecordSegments(t.Context(), commit) == nil
			before, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if writable && (n != 0 || tenant.RecordSegments(t.Context(), commit) != nil) {
				t.Fatal("maintenance sealed a writable subject")
			}
			after, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("maintenance changed execution: %+v, %v", after, err)
			}
		})
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 64})); err != nil {
		t.Fatal(err)
	}
}

func TestLogPersistenceMaintenancePreservesPublicationAndFinalSeals(t *testing.T) {
	for _, incomplete := range []string{"", "local_tail_lost"} {
		t.Run("final_"+incomplete, func(t *testing.T) {
			s, tenant, commit := publishedLogFixture(t)
			if err := tenant.RecordSegments(t.Context(), commit); err != nil {
				t.Fatal(err)
			}
			if n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), 10); err != nil || n != 0 {
				t.Fatalf("publisher lost its lease-free stream: %d, %v", n, err)
			}
			commit.Final, commit.IncompleteReason = true, incomplete
			if err := tenant.RecordSegments(t.Context(), commit); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE runs SET intake_mode='execute' WHERE team='a' AND id='run'")
			if n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), 10); err != nil || n != 0 {
				t.Fatalf("final seal replaced: %d, %v", n, err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE runs SET intake_mode='publish' WHERE team='a' AND id='run'")
			if err := tenant.RecordSegments(t.Context(), commit); err != nil {
				t.Fatalf("final seal replay changed: %v", err)
			}
		})
	}
}

func TestLogPersistenceMaintenanceRejectsInvalidBatchSize(t *testing.T) {
	s, _, _ := maintenanceOwnerLogFixture(t, capability.UnitPlan)
	rewriteSchemaExec(t, s.db, "UPDATE triggers SET lease_expires_at=1 WHERE team='a' AND id='run'")
	op := s.AsOperator()
	for _, limit := range []int{-1, 0} {
		if _, err := op.SealAbandonedLogStreams(t.Context(), limit); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid batch size accepted: %d, %v", limit, err)
		}
	}
}

func TestLogPersistenceMaintenanceBoundsAndRollsBackTheBatch(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure_%t", fail), func(t *testing.T) {
			s, tenant, commit := maintenanceOwnerLogFixture(t, capability.UnitTrigger)
			rewriteSchemaExec(t, s.db, "UPDATE triggers SET lease_expires_at=1 WHERE team='a' AND id='run'")
			other := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 8}}
			identity, err := logIdentity(other)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,prefix,retention_class,created_at,updated_at) VALUES ('a','run',?,'owner_unit',8,'plan',?,'r30',1,1)`, identity.id, identity.id)
			reader := authorityMember(t, s, "batch-log-reader", RoleReader)
			countSeals := func(want int) {
				t.Helper()
				sealed := 0
				for _, subject := range []blob.LogSubject{commit.Subject, other} {
					got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: subject})
					if err != nil || got.Complete {
						t.Fatalf("batch read: %+v, %v", got, err)
					}
					if got.IncompleteReason == "writer_lost" {
						sealed++
					} else if got.IncompleteReason != "" {
						t.Fatalf("unexpected log reason: %q", got.IncompleteReason)
					}
				}
				if sealed != want {
					t.Fatalf("sealed %d streams, want %d", sealed, want)
				}
			}
			if fail {
				first, err := logIdentity(commit.Subject)
				if err != nil {
					t.Fatal(err)
				}
				lastID := max(first.id, identity.id)
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER reject_seal BEFORE UPDATE ON log_streams WHEN OLD.id='`+lastID+`' BEGIN SELECT RAISE(ABORT,'seal failed'); END`)
				if n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), 2); err == nil || n != 0 {
					t.Fatalf("failed batch reported success: %d, %v", n, err)
				}
				countSeals(0)
				rewriteSchemaExec(t, s.db, "DROP TRIGGER reject_seal")
			}
			for i := 1; i <= 2; i++ {
				if n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), 1); err != nil || n != 1 {
					t.Fatalf("bounded batch: %d, %v", n, err)
				}
				countSeals(i)
			}
		})
	}
}

func maintenanceOwnerLogFixture(t *testing.T, kind capability.UnitKind) (*Store, *Tenant, SegmentCommit) {
	t.Helper()
	s, claim := releaseFixture(t, kind)
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogUnitKind(kind), Generation: int64(claim.Award.Generation)}}
	if kind == capability.UnitEval {
		subject.Unit.Source = &blob.LogSource{NodeID: "job", Generation: claim.SourceGeneration}
		rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation=? WHERE team='a' AND run_id='run'", claim.SourceGeneration)
	}
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET status='pending',started_at=NULL,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE team='a' AND run_id='run'")
	tenant := &Tenant{s: s, team: "a"}
	commit := SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30"}
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	return s, tenant, commit
}

func TestLogPersistenceMaintenanceSealsLostOwnerUnits(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			for _, change := range []string{"live", "expired", "reclaimed", "source"} {
				if change == "source" && kind != capability.UnitEval {
					continue
				}
				t.Run(change, func(t *testing.T) {
					s, tenant, commit := maintenanceOwnerLogFixture(t, kind)
					table, filter := "triggers", "team='a' AND id='run'"
					if kind == capability.UnitEval {
						table, filter = "eval_units", "team='a' AND run_id='run'"
					}
					switch change {
					case "expired":
						rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET lease_expires_at=? WHERE "+filter, s.now().UnixNano())
					case "reclaimed":
						rewriteSchemaExec(t, s.db, "UPDATE "+table+" SET generation=generation+1 WHERE "+filter)
					case "source":
						rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run'")
					}
					want := int64(1)
					if change == "live" {
						want = 0
					}
					if n, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), 1); err != nil || n != want {
						t.Fatalf("%s owner sealed=%d, want %d: %v", change, n, want, err)
					}
					if change == "live" {
						commit.Final = true
						if err := tenant.RecordSegments(t.Context(), commit); err != nil {
							t.Fatalf("live owner lost its stream: %v", err)
						}
					} else {
						reader := authorityMember(t, s, "owner-maintenance-reader", RoleReader)
						got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: commit.Subject})
						if err != nil || got.Complete || got.IncompleteReason != "writer_lost" {
							t.Fatalf("abandoned owner read: %+v, %v", got, err)
						}
					}
				})
			}
		})
	}
}
