package store

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func resultFixture(t *testing.T) (*Store, *Tenant, Actor, runspec.AttemptIdentity) {
	t.Helper()
	s := authorityStore(t)
	s.clock = func() time.Time { return time.Unix(1000, 0).UTC() }
	spec := authorityClaimFixture(t, s, authorityRoot("machine", "a"))
	tx, err := s.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	issued, err := s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &spec})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tx, "UPDATE nodes SET capability_prefix = ? WHERE team = 'a' AND run_id = 'run' AND node_id = 'job'", issued.Capability.Prefix)
	rewriteSchemaExec(t, tx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',2,7,'machine',?,'runner','machine','remote',?)`, issued.Capability.Prefix, s.now().Add(-time.Second).UnixNano())
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Holder: "machine", Generation: 7}
	return s, &Tenant{s: s, team: "a"}, actor, runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 2, Generation: 7}
}

func TestResultStepFencesAttemptsAndAbsorbsTerminalState(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	started := s.now().Add(-time.Second)
	finished := s.now()
	report := StepReport{Attempt: attempt, Actor: actor, StepID: "compile", State: "running", StartedAt: &started}
	if err := tenant.RecordStep(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	report.State = "failed"
	report.FinishedAt = &finished
	report.MaskedError = "masked failure"
	if err := tenant.RecordStep(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RecordStep(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	report.State = "passed"
	if err := tenant.RecordStep(t.Context(), report); err == nil {
		t.Fatal("terminal step rewritten")
	}
	report.StepID = "late"
	report.Attempt.Attempt = 1
	if err := tenant.RecordStep(t.Context(), report); err == nil {
		t.Fatal("stale attempt step accepted")
	}
	var state, message string
	var generation, ordinal int64
	if err := s.db.QueryRow("SELECT status,error,generation,attempt FROM node_steps WHERE step_id = 'compile'").Scan(&state, &message, &generation, &ordinal); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || message != "masked failure" || generation != 7 || ordinal != 2 {
		t.Fatalf("step = %s %s %d %d", state, message, generation, ordinal)
	}
}

func TestResultAnnotationsSummariesAndStaleWrites(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	now := s.now()
	if err := tenant.RecordStep(t.Context(), StepReport{Attempt: attempt, Actor: actor, StepID: "compile", State: "running", StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	line := 12
	annotation := AnnotationWrite{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, StepID: "compile", Level: "warning", Message: "masked", File: "src/main.go", Line: &line}
	if err := tenant.WriteAnnotation(t.Context(), annotation); err != nil {
		t.Fatal(err)
	}
	if err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: annotation.Ref, Actor: actor, StepID: "compile", Markdown: "result"}); err != nil {
		t.Fatal(err)
	}
	var body []byte
	var summary string
	if err := s.db.QueryRow("SELECT annotations_json,summary FROM node_steps WHERE step_id = 'compile'").Scan(&body, &summary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"file":"src/main.go"`) || !strings.Contains(string(body), `"line":12`) || summary != "result" {
		t.Fatalf("metadata lost: %s, %s", body, summary)
	}
	annotation.Message = strings.Repeat("x", int(limits.AnnotationBytes)+1)
	if err := tenant.WriteAnnotation(t.Context(), annotation); err == nil {
		t.Fatal("oversize annotation accepted")
	}
	annotation.Message = "late"
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation = 8 WHERE run_id = 'run'")
	if err := tenant.WriteAnnotation(t.Context(), annotation); err == nil {
		t.Fatal("stale annotation accepted")
	}
	if err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: annotation.Ref, Actor: actor, Markdown: "late"}); err == nil {
		t.Fatal("stale summary accepted")
	}
}

func logCommit(actor Actor, attempt runspec.AttemptIdentity) SegmentCommit {
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: attempt.Team, RunID: attempt.RunID, Attempt: &attempt}
	key, err := blob.LogObjectKey(subject, "r30", 1)
	if err != nil {
		panic(err)
	}
	return SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30", Segments: []Segment{{Index: 1, Offset: 0, Size: 3, StoredBytes: 3, Digest: strings.Repeat("a", 64), ObjectKey: key}}}
}

func TestResultSegmentsRoundTripContiguityAndSeal(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	commit := logCommit(actor, attempt)
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	key, err := blob.LogObjectKey(commit.Subject, "r30", 2)
	if err != nil {
		t.Fatal(err)
	}
	commit.Segments = []Segment{{Index: 2, Offset: 4, Size: 2, StoredBytes: 2, Digest: strings.Repeat("b", 64), ObjectKey: key}}
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("non-contiguous offset accepted")
	}
	commit.Segments[0].Offset = 3
	commit.Final = true
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	commit.Segments[0].ObjectKey = "other"
	if err := tenant.RecordSegments(t.Context(), commit); err == nil {
		t.Fatal("different final commit replayed")
	}
	reader := authorityMember(t, s, "reader", RoleReader)
	got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: commit.Subject, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || len(got.Segments) != 1 || got.Segments[0].Offset != 3 || got.Segments[0].ObjectKey != key {
		t.Fatalf("log read = %#v", got)
	}
	foreign := *commit.Subject.Attempt
	foreign.Team = "b"
	subject := commit.Subject
	subject.Team = "b"
	subject.Attempt = &foreign
	if _, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: subject}); err == nil {
		t.Fatal("foreign log read accepted")
	}
}

func TestResultFinalLogsUseExactWriterAndBoundedGrace(t *testing.T) {
	for _, kind := range []string{"allowed", "late", "new_generation", "new_attempt", "revoked", "wrong_writer"} {
		t.Run(kind, func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			finished := s.now().Add(-time.Second)
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET status = 'done', outcome = 'success', finished_at = ?, claimed_by = NULL, lease_expires_at = NULL, capability_prefix = '' WHERE run_id = 'run'", finished.UnixNano())
			rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at = ?,outcome = 'success' WHERE run_id = 'run'", finished.UnixNano())
			switch kind {
			case "late":
				s.clock = func() time.Time { return finished.Add(limits.LogSealGrace + time.Nanosecond) }
			case "new_generation":
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation = 8 WHERE run_id = 'run'")
			case "new_attempt":
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET attempt = 3 WHERE run_id = 'run'")
			case "revoked":
				rewriteSchemaExec(t, s.db, "DELETE FROM capabilities WHERE prefix = 'machine'")
			case "wrong_writer":
				actor.Holder = "other"
			}
			commit := logCommit(actor, attempt)
			commit.Final = true
			err := tenant.RecordSegments(t.Context(), commit)
			if kind == "allowed" && err != nil {
				t.Fatal(err)
			}
			if kind != "allowed" && err == nil {
				t.Fatal("ended writer accepted")
			}
		})
	}
}

func TestResultJobMetadataStartsEmptyOnEveryAttempt(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET execution_json=?", []byte(`{"retry":{"set":true,"value":2}}`))
	ref := controllerwire.NodeRef{RunID: attempt.RunID, NodeID: attempt.NodeID}
	for retry := range 2 {
		if err := tenant.WriteSummary(t.Context(), SummaryWrite{Ref: ref, Actor: actor, Markdown: "old result"}); err != nil {
			t.Fatal(err)
		}
		if err := tenant.WriteAnnotation(t.Context(), AnnotationWrite{Ref: ref, Actor: actor, Level: "warning", Message: "old annotation"}); err != nil {
			t.Fatal(err)
		}
		applied, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: actor, To: NodeRunning, Cause: CauseRetry, SourceAttempt: sourceAttempt(attempt.Attempt + int64(retry)), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: "verify"}}})
		if err != nil {
			t.Fatal(err)
		}
		node, err := tenant.GetNode(t.Context(), ref.RunID, ref.NodeID)
		if err != nil || node.Summary != "" || len(node.Annotations) != 0 || int64(node.AttemptsConsumed) != applied.Attempt {
			t.Fatalf("new attempt inherited metadata: %+v, %v", node, err)
		}
	}
}
