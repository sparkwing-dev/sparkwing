package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLogPersistenceRejectsMalformedSubjects(t *testing.T) {
	_, tenant, actor, attempt := resultFixture(t)
	valid := logCommit(actor, attempt)
	for _, mutate := range []func(*blob.LogSubject){
		func(s *blob.LogSubject) { s.Unit = &blob.LogUnit{Kind: blob.LogPlan, Generation: 7} },
		func(s *blob.LogSubject) { s.Attempt = nil },
		func(s *blob.LogSubject) { s.Attempt.Attempt = 0 },
		func(s *blob.LogSubject) { s.Attempt.Generation = 0 },
		func(s *blob.LogSubject) { s.Attempt.Team = "b" },
		func(s *blob.LogSubject) { s.Attempt.RunID = "other" },
		func(s *blob.LogSubject) { s.Attempt.NodeID = "" },
		func(s *blob.LogSubject) {
			s.Kind = blob.LogOwnerUnit
			s.Attempt = nil
			s.Unit = &blob.LogUnit{Kind: blob.LogEval, Generation: 7}
		},
		func(s *blob.LogSubject) {
			s.Kind = blob.LogOwnerUnit
			s.Attempt = nil
			s.Unit = &blob.LogUnit{Kind: blob.LogPlan, Generation: 7, Source: &blob.LogSource{NodeID: "job", Generation: 7}}
		},
	} {
		commit := valid
		a := attempt
		commit.Subject.Attempt = &a
		mutate(&commit.Subject)
		if err := tenant.RecordSegments(t.Context(), commit); err == nil {
			t.Fatalf("accepted subject: %+v", commit.Subject)
		}
		if _, err := tenant.ReadLogSegments(t.Context(), actor, blob.LogReadRequest{Subject: commit.Subject}); err == nil {
			t.Fatal("read accepted malformed subject")
		}
	}
}

func TestLogPersistenceFinalEmptySealAndReplay(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	commit.Segments = nil
	commit.Final = true
	commit.IncompleteReason = "worker lost tail"
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	reader := authorityMember(t, s, "log-reader", RoleReader)
	got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: commit.Subject})
	if err != nil || got.Complete || got.IncompleteReason != commit.IncompleteReason || len(got.Segments) != 0 {
		t.Fatalf("empty sealed stream: %+v, %v", got, err)
	}
	var outcome string
	if err := s.db.QueryRow("SELECT outcome FROM nodes WHERE run_id='run'").Scan(&outcome); err != nil || outcome != "" {
		t.Fatalf("log failure changed job: %q, %v", outcome, err)
	}
	commit.Final = false
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("unsealed final log")
	}
}

func TestLogPersistenceFailureRollsBackWholeCommit(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	bad := commit.Segments[0]
	bad.Index, bad.Offset = 2, 3
	commit.Segments = append(commit.Segments, bad)
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("gap accepted")
	}
	rewriteSchemaCount(t, s.db, "log_streams", 0)
	rewriteSchemaCount(t, s.db, "log_segments", 0)
}

func TestLogPersistenceRefusesUnsealedIncompleteAndForeignObject(t *testing.T) {
	for _, problem := range []string{"unsealed", "object"} {
		t.Run(problem, func(t *testing.T) {
			_, tenant, actor, attempt := resultFixture(t)
			commit := logCommit(actor, attempt)
			if problem == "unsealed" {
				commit.IncompleteReason = "lost"
			} else {
				commit.Segments[0].ObjectKey = "teams/b/logs/private"
			}
			if err := tenant.RecordSegments(t.Context(), commit); err == nil {
				t.Fatal("invalid commit accepted")
			}
		})
	}
}

func TestLogPersistenceSealedReplayStillChecksAncestry(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	commit.Final = true
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=? WHERE prefix='machine'", time.Unix(999, 0).UnixNano())
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("revoked writer replayed")
	}
}

func TestLogPersistenceOwnerUnitsHaveIndependentFences(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			s, claim := releaseFixture(t, kind)
			tenant := &Tenant{s: s, team: "a"}
			actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogUnitKind(kind), Generation: claim.Award.Generation}}
			if kind == capability.UnitEval {
				subject.Unit.Source = &blob.LogSource{NodeID: "job", Generation: claim.SourceGeneration}
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation=? WHERE run_id='run' AND node_id='job'", claim.SourceGeneration)
			}
			commit := SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30", Final: true}
			if err := tenant.RecordSegments(t.Context(), commit); err != nil {
				t.Fatal(err)
			}
			if kind == capability.UnitEval {
				rewriteSchemaExec(t, s.db, "UPDATE eval_units SET generation=generation+1 WHERE run_id='run'")
			} else {
				rewriteSchemaExec(t, s.db, "UPDATE triggers SET generation=generation+1 WHERE id='run'")
			}
			if err := tenant.RecordSegments(t.Context(), commit); err == nil {
				t.Fatal("stale unit replayed seal")
			}
			reader := authorityMember(t, s, "owner-log-reader", RoleReader)
			if got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: subject}); err != nil || !got.Complete {
				t.Fatalf("historical unit log=%+v,%v", got, err)
			}
		})
	}
}

func TestLogPersistenceReadDoesNotBorrowSealedReplayGrace(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "sealed"}[final], func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			commit := logCommit(actor, attempt)
			commit.Final = final
			if err := tenant.RecordSegments(t.Context(), commit); err != nil {
				t.Fatal(err)
			}
			finished := s.now()
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET status='done',outcome='success',finished_at=?,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE run_id='run'", finished.UnixNano())
			rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=?,outcome='success' WHERE run_id='run'", finished.UnixNano())
			reader := authorityMember(t, s, "historical-log-reader", RoleReader)
			request := blob.LogReadRequest{Subject: commit.Subject}
			if _, err := tenant.ReadLogSegments(t.Context(), actor, request); err != nil {
				t.Fatalf("final attempt grace: %v", err)
			}
			s.clock = func() time.Time { return finished.Add(limits.LogSealGrace + time.Nanosecond) }
			if _, err := tenant.ReadLogSegments(t.Context(), actor, request); err == nil {
				t.Fatal("ended writer read after final-attempt grace")
			}
			if got, err := tenant.ReadLogSegments(t.Context(), reader, request); err != nil || len(got.Segments) != 1 {
				t.Fatalf("historical member read=%+v: %v", got, err)
			}
			if final {
				if err := tenant.RecordSegments(t.Context(), commit); err != nil {
					t.Fatalf("identical sealed replay: %v", err)
				}
			}
		})
	}
}

func TestLogPersistenceTriggerReadRechecksItsOwnerLease(t *testing.T) {
	for _, stale := range []string{"expired", "generation", "holder", "prefix", "ended"} {
		t.Run(stale, func(t *testing.T) {
			s, claim := releaseFixture(t, capability.UnitTrigger)
			tenant := &Tenant{s: s, team: "a"}
			actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: claim.Award.Generation}}
			if err := tenant.RecordSegments(t.Context(), SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30", Final: true}); err != nil {
				t.Fatal(err)
			}
			jobSubject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: 2}}
			identity, err := logIdentity(jobSubject)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','run','job',1,2,'prior-writer','prior-award','runner','machine','remote',1,2,'success')`)
			rewriteSchemaExec(t, s.db, `INSERT INTO log_streams(team,run_id,id,subject_kind,node_id,attempt,generation,prefix,retention_class,created_at,updated_at) VALUES ('a','run',?,'job_attempt','job',1,2,?,'r30',1,1)`, identity.id, identity.id)
			requests := []blob.LogReadRequest{{Subject: subject}, {Subject: jobSubject}}
			for _, request := range requests {
				if _, err := tenant.ReadLogSegments(t.Context(), actor, request); err != nil {
					t.Fatalf("live trigger read of %s: %v", request.Subject.Kind, err)
				}
			}
			changes := map[string]string{"expired": "lease_expires_at=1", "generation": "generation=generation+1", "holder": "claimed_by='other'", "prefix": "capability_prefix='other'", "ended": "status='done',claimed_by=NULL,lease_expires_at=NULL"}
			rewriteSchemaExec(t, s.db, "UPDATE triggers SET "+changes[stale]+" WHERE id='run'")
			for _, request := range requests {
				if _, err := tenant.ReadLogSegments(t.Context(), actor, request); err == nil {
					t.Fatalf("stale trigger borrowed runs.read for %s logs", request.Subject.Kind)
				}
			}
		})
	}
}
