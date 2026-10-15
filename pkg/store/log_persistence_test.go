package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
