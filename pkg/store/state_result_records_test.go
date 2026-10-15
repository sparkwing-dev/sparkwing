package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type nodeResultRecordsFunc func(context.Context, *storeTx, Team, Actor, NodeRow, NodeResult) error

func (f nodeResultRecordsFunc) AttachResultRecords(ctx context.Context, tx *storeTx, team Team, actor Actor, row NodeRow, result NodeResult) error {
	return f(ctx, tx, team, actor, row, result)
}

func TestResultRecordsCommitWithTerminalState(t *testing.T) {
	for _, scenario := range []string{"success", "bad_record", "expired_holder"} {
		t.Run(scenario, func(t *testing.T) {
			tenant, now, actor := liveHolderFixture(t)
			record := blob.RecordKey{Team: "a", Kind: blob.KindOutput, Name: "result"}
			recordErr := errors.New("result record is not readable")
			tenant.s.resultRecords = nodeResultRecordsFunc(func(ctx context.Context, tx *storeTx, team Team, got Actor, row NodeRow, result NodeResult) error {
				if team != "a" || got != actor || row.Attempt != 3 || row.Generation != 2 || result.OutputRecord == nil || *result.OutputRecord != record {
					t.Fatal("attachment lost the attempt fence")
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('attached',?)`, []byte("yes")); err != nil {
					return err
				}
				if scenario == "bad_record" {
					return recordErr
				}
				if scenario == "expired_holder" {
					*now = now.Add(time.Hour)
				}
				return nil
			})
			change := NodeTransition{Ref: NodeRef{"run", "job"}, To: NodeSuccess, Cause: CauseReport, Actor: actor, SourceAttempt: sourceAttempt(3), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess, OutputRecord: &record}}}
			_, err := tenant.TransitionNode(t.Context(), change)
			if (scenario == "success") != (err == nil) {
				t.Fatalf("%s: %v", scenario, err)
			}
			node, readErr := tenant.ReadNode(t.Context(), change.Ref)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario != "success" {
				if node.Status != StatusRunning || node.StateSeq != 0 {
					t.Fatalf("failed attachment ended job: %+v", node)
				}
				rewriteSchemaCount(t, tenant.s.db, "settings", 0)
				rewriteSchemaCount(t, tenant.s.db, "events", 0)
				return
			}
			if node.Status != StatusDone || node.Outcome != NodeSuccess {
				t.Fatalf("successful attachment: %+v", node)
			}
			replayed, err := tenant.TransitionNode(t.Context(), change)
			if err != nil || !replayed.Replayed {
				t.Fatalf("result replay: %+v, %v", replayed, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "settings", 1)
			rewriteSchemaCount(t, tenant.s.db, "events", 1)
		})
	}
}

func TestResultRecordsFromAnEarlierAttemptCannotFinishARetry(t *testing.T) {
	s, records, finish := blobResultFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"retry":{"set":true,"value":1}}`))
	retry := NodeTransition{Ref: finish.Ref, Actor: finish.Actor, To: NodeRunning, Cause: CauseRetry, SourceAttempt: sourceAttempt(2), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: "verify"}}}
	if applied, err := records.tenant.TransitionNode(t.Context(), retry); err != nil || applied.Attempt != 3 {
		t.Fatalf("retry: %+v, %v", applied, err)
	}
	before, err := records.tenant.ReadNode(t.Context(), finish.Ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, ordinal := range []int64{2, 3} {
		finish.SourceAttempt = sourceAttempt(ordinal)
		if _, err := records.tenant.TransitionNode(t.Context(), finish); err == nil {
			t.Fatalf("attempt %d attached earlier output", ordinal)
		}
		after, err := records.tenant.ReadNode(t.Context(), finish.Ref)
		if err != nil || after.Status != before.Status || after.Attempt != before.Attempt || after.StateSeq != before.StateSeq || after.CapabilityPrefix != before.CapabilityPrefix {
			t.Fatalf("stale output changed retry: %+v, %v", after, err)
		}
	}
	rewriteSchemaCount(t, s.db, "events", 1)
	finish.Extra.Result = &NodeResult{Outcome: NodeSuccess}
	if _, err := records.tenant.TransitionNode(t.Context(), finish); err != nil {
		t.Fatalf("clean retry result refused: %v", err)
	}
}
