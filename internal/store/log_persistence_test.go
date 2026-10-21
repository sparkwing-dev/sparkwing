package store

import (
	"errors"
	"math/rand"
	"slices"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLogPersistenceRetainedBytesCountSegmentsOnce(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	for range 2 {
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*Segment){
		func(s *Segment) { s.Digest = "b" + s.Digest[1:] },
		func(s *Segment) { s.Size++ },
		func(s *Segment) { s.Size = limits.LogSegmentUploadBytes + 1 },
	} {
		bad := commit
		bad.Segments = slices.Clone(commit.Segments)
		change(&bad.Segments[0])
		if err := tenant.RecordSegments(t.Context(), bad); err == nil {
			t.Fatal("conflicting or oversized segment accepted")
		}
	}
	blobAccountingDiskStore(t, s)
	key, err := blob.LogObjectKey(commit.Subject, "r30", 2)
	if err != nil {
		t.Fatal(err)
	}
	commit.Segments = []Segment{{Index: 2, Offset: 3, Size: 2, StoredBytes: 2, Digest: commit.Segments[0].Digest, ObjectKey: key}}
	commit.Final = true
	for range 2 {
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
	}
	if size, err := tenant.StorageRetainedBytes(t.Context()); err != nil || size != 5 {
		t.Fatalf("retained log bytes=%d, want 5: %v", size, err)
	}
	rewriteSchemaCount(t, s.db, "log_streams", 1)
	rewriteSchemaCount(t, s.db, "log_segments", 2)
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	rewriteSchemaCount(t, s.db, "log_streams", 0)
	rewriteSchemaCount(t, s.db, "log_segments", 0)
	if size, err := tenant.StorageRetainedBytes(t.Context()); err != nil || size != 0 {
		t.Fatalf("deleted run retained log bytes=%d: %v", size, err)
	}
}

func TestLogPersistenceAttemptAuthorityDoesNotTransfer(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	property := func(suffix string, number uint16, field uint8) bool {
		forged := attempt
		switch field % 5 {
		case 0:
			forged.Team += "other_" + suffix
		case 1:
			forged.RunID += "other_" + suffix
		case 2:
			forged.NodeID += "other_" + suffix
		case 3:
			forged.Attempt += int64(number) + 1
		case 4:
			forged.Generation += int64(number) + 1
		}
		subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: forged.Team, RunID: forged.RunID, Attempt: &forged}
		if err := tenant.RecordSegments(t.Context(), SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30"}); err == nil {
			t.Logf("forged writer=%+v: %v", forged, err)
			return false
		}
		if _, err := tenant.ReadLogSegments(t.Context(), actor, blob.LogReadRequest{Subject: subject}); err == nil || errors.Is(err, ErrNotFound) {
			t.Logf("forged reader=%+v: %v", forged, err)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(92075))}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "log_streams", 1)
	rewriteSchemaCount(t, s.db, "log_segments", 1)
}

func TestLogPersistenceAttemptByteLimitPreservesCommittedBytes(t *testing.T) {
	_, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	segment := commit.Segments[0]
	for offset := int64(0); offset < limits.AttemptLogBytes; offset += segment.Size {
		segment.Offset = offset
		segment.Size = min(limits.LogSegmentUploadBytes, limits.AttemptLogBytes-offset)
		segment.StoredBytes = segment.Size
		key, err := blob.LogObjectKey(commit.Subject, "r30", segment.Index)
		if err != nil {
			t.Fatal(err)
		}
		segment.ObjectKey = key
		commit.Segments = []Segment{segment}
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
		segment.Index++
	}
	segment.Offset, segment.Size = limits.AttemptLogBytes, 1
	key, err := blob.LogObjectKey(commit.Subject, "r30", segment.Index)
	if err != nil {
		t.Fatal(err)
	}
	segment.ObjectKey = key
	commit.Segments = []Segment{segment}
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("accepted a segment above the attempt byte limit")
	}
	if size, err := tenant.StorageRetainedBytes(t.Context()); err != nil || size != limits.AttemptLogBytes {
		t.Fatalf("refused segment changed retained bytes=%d: %v", size, err)
	}
}

func TestLogPersistenceLaterNodeFinishCannotExtendAttemptGrace(t *testing.T) {
	property := func(offset int16, later uint8) bool {
		s, tenant, actor, attempt := resultFixture(t)
		commit := logCommit(actor, attempt)
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
		reader := authorityMember(t, s, "grace-reader", RoleReader)
		lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "logs", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		finished := s.now()
		nodeFinished := finished.Add(time.Duration(1+later%59) * time.Second)
		rewriteSchemaExec(t, s.db, "UPDATE nodes SET status='done',outcome='success',finished_at=?,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE team='a' AND run_id='run'", nodeFinished.UnixNano())
		rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=?,outcome='success' WHERE team='a' AND run_id='run'", finished.UnixNano())
		s.clock = func() time.Time { return finished.Add(limits.LogSealGrace + time.Duration(offset)) }
		request := blob.LogReadRequest{Subject: commit.Subject}
		allowed := offset <= 0
		read, readErr := tenant.ReadLogSegments(t.Context(), actor, request)
		writeErr := tenant.RecordSegments(t.Context(), commit)
		if (readErr == nil) != allowed || (writeErr == nil) != allowed || (!allowed && len(read.Segments) != 0) {
			t.Logf("offset=%d later=%d read=%v write=%v", offset, later, readErr, writeErr)
			return false
		}
		sealed, err := s.AsOperator().SealAbandonedLogStreams(t.Context(), lease, 1)
		if err != nil || sealed != int64(boolInt(!allowed)) {
			t.Logf("offset=%d maintenance sealed=%d: %v", offset, sealed, err)
			return false
		}
		commit.Final = true
		if err := tenant.RecordSegments(t.Context(), commit); (err == nil) != allowed {
			t.Logf("offset=%d final seal=%v", offset, err)
			return false
		}
		got, err := tenant.ReadLogSegments(t.Context(), reader, request)
		wantReason := ""
		if !allowed {
			wantReason = "writer_lost"
		}
		if err != nil || got.Complete != allowed || got.IncompleteReason != wantReason || !slices.Equal(got.Segments, commit.Segments) {
			t.Logf("offset=%d historical log=%+v: %v", offset, got, err)
			return false
		}
		if size, err := tenant.StorageRetainedBytes(t.Context()); err != nil || size != 3 {
			t.Logf("offset=%d retained bytes=%d: %v", offset, size, err)
			return false
		}
		row, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil || row.Status != StatusDone || row.Outcome != NodeSuccess || row.FinishedAt == nil || !row.FinishedAt.Equal(nodeFinished) {
			t.Logf("log mutation changed job: %+v: %v", row, err)
			return false
		}
		rewriteSchemaCount(t, s.db, "log_streams", 1)
		rewriteSchemaCount(t, s.db, "log_segments", 1)
		return true
	}
	for _, offset := range []int16{-1, 0, 1} {
		if !property(offset, 58) {
			t.Fatalf("attempt grace boundary at %d ns", offset)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(92076))}); err != nil {
		t.Fatal(err)
	}
}

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
			actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogUnitKind(kind), Generation: int64(claim.Award.Generation)}}
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
			actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: int64(claim.Award.Generation)}}
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
