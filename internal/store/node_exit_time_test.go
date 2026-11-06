package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestReportedExitTimePreservesDelayedObservation(t *testing.T) {
	for _, mode := range []string{"observed", "absent", "zero", "future", "before_start"} {
		t.Run(mode, func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET started_at=? WHERE team='a' AND run_id='run' AND node_id='job'`, s.now().Add(-time.Second).UnixNano())
			row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			received := s.now().Add(2 * time.Second)
			observed, want := s.now(), s.now()
			var exited *time.Time
			switch mode {
			case "observed":
				exited = &observed
			case "absent":
				want = received
			case "zero":
				exited, want = new(time.Time), received
			case "future":
				observed, want = received.Add(time.Hour), received
				exited = &observed
			case "before_start":
				observed, want = row.StartedAt.Add(-time.Hour), *row.StartedAt
				exited = &observed
			}
			s.clock = func() time.Time { return received }
			if mode == "observed" {
				stepStarted := observed.Add(time.Second)
				if err := tenant.RecordStep(t.Context(), StepReport{Attempt: attempt, Actor: actor, StepID: "publish", State: "running", StartedAt: &stepStarted}); err != nil {
					t.Fatal(err)
				}
			}
			command := ReportNode{Ref: row.Ref, Actor: actor, SourceAttempt: sourceAttempt(attempt.Attempt), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}, ExitedAt: exited}
			if _, err := tenant.TransitionNode(t.Context(), command); err != nil {
				t.Fatal(err)
			}
			if replay, err := tenant.TransitionNode(t.Context(), command); err != nil || !replay.Replayed {
				t.Fatalf("report replay = %+v, %v", replay, err)
			}
			row, err = tenant.ReadNode(t.Context(), row.Ref)
			if err != nil || row.FinishedAt == nil || !row.FinishedAt.Equal(want) {
				t.Fatalf("job finished = %v, want %v: %v", row.FinishedAt, want, err)
			}
			if mode == "observed" {
				steps, err := tenant.ListNodeSteps(t.Context(), "run")
				if err != nil || len(steps) != 1 || steps[0].FinishedAt == nil || !steps[0].FinishedAt.Equal(received) {
					t.Fatalf("publication step closure = %+v, receipt %v: %v", steps, received, err)
				}
			}
			var finished int64
			if err := s.db.QueryRowContext(t.Context(), `SELECT finished_at FROM node_attempts WHERE team='a' AND run_id='run' AND node_id='job' AND attempt=2`).Scan(&finished); err != nil || finished != want.UnixNano() {
				t.Fatalf("attempt finished = %d, want %d: %v", finished, want.UnixNano(), err)
			}
		})
	}
}

func TestRetriedAttemptPreservesObservedExitAndOwnStart(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET started_at=? WHERE team='a' AND run_id='run' AND node_id='job'`, s.now().Add(-time.Second).UnixNano())
	row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	row.Execution.Retry.Value = 1
	settings, err := json.Marshal(row.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, settings); err != nil {
		t.Fatal(err)
	}
	exited := s.now()
	received := exited.Add(2 * time.Second)
	s.clock = func() time.Time { return received }
	if _, err := tenant.TransitionNode(t.Context(), RetryNode{Ref: row.Ref, Actor: actor, SourceAttempt: sourceAttempt(attempt.Attempt), Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}, ExitedAt: &exited}); err != nil {
		t.Fatal(err)
	}
	var finished int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT finished_at FROM node_attempts WHERE team='a' AND run_id='run' AND node_id='job' AND attempt=2`).Scan(&finished); err != nil || finished != exited.UnixNano() {
		t.Fatalf("retried attempt finished = %d, want %d: %v", finished, exited.UnixNano(), err)
	}
	row, err = tenant.ReadNode(t.Context(), row.Ref)
	if err != nil || row.StartedAt == nil || !row.StartedAt.Equal(received) || row.FinishedAt != nil {
		t.Fatalf("next attempt start = %v, want %v: %v", row.StartedAt, received, err)
	}
	s.clock = func() time.Time { return received.Add(time.Second) }
	if _, err := tenant.TransitionNode(t.Context(), ReportNode{Ref: row.Ref, Actor: actor, SourceAttempt: &row.Attempt, Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}, ExitedAt: &exited}); err != nil {
		t.Fatal(err)
	}
	row, err = tenant.ReadNode(t.Context(), row.Ref)
	if err != nil || row.FinishedAt == nil || !row.FinishedAt.Equal(received) {
		t.Fatalf("clamped exit = %v, want latest start %v: %v", row.FinishedAt, received, err)
	}
}

func TestAbsentExitTimeReplaysLegacyTransitionDigest(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "report", true: "retry"}[retry], func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
			var command NodeCommand = ReportNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(attempt.Attempt), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}
			if retry {
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"retry":{"set":true,"value":1}}`))
				command = RetryNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(attempt.Attempt), Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}}
			}
			if _, err := tenant.TransitionNode(t.Context(), command); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(command.nodeChange())
			if err != nil {
				t.Fatal(err)
			}
			legacy := bytes.ReplaceAll(encoded, []byte(`,"ExitedAt":null`), nil)
			digest := sha256.Sum256(legacy)
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET state_digest=? WHERE team='a' AND run_id='run' AND node_id='job'`, hex.EncodeToString(digest[:]))
			exited := s.now().Add(-time.Second)
			switch c := command.(type) {
			case ReportNode:
				c.ExitedAt = &exited
				command = c
			case RetryNode:
				c.ExitedAt = &exited
				command = c
			}
			if replay, err := tenant.TransitionNode(t.Context(), command); err != nil || !replay.Replayed {
				t.Fatalf("legacy transition replay = %+v, %v", replay, err)
			}
		})
	}
}
