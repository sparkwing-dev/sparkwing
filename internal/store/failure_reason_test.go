package store_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestJobFinishPersistsMetadataAndRefusesOverwrite(t *testing.T) {
	for _, outcome := range []runstate.NodeState{runstate.NodeFailed, runstate.NodeSuccess} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
			result := &runstate.NodeResult{Outcome: outcome}
			if outcome == runstate.NodeFailed {
				code := 137
				result.Reason, result.MaskedError, result.ExitCode = "oom_killed", "container killed", &code
			}
			finish := store.ReportNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(1), Outcome: outcome, Result: result}
			for i := range 2 {
				applied, err := f.tenant.TransitionNode(t.Context(), finish)
				if err != nil || applied.Replayed != (i == 1) {
					t.Fatalf("finish=%+v, %v", applied, err)
				}
			}
			node, err := f.tenant.GetNode(t.Context(), string(f.ref.RunID), string(f.ref.NodeID))
			if err != nil || node.Outcome != string(outcome) || node.FailureReason != result.Reason || !reflect.DeepEqual(node.ExitCode, result.ExitCode) || node.Error != result.MaskedError || node.FinishedAt == nil {
				t.Fatalf("metadata=%+v, %v", node, err)
			}
			finish.Result = &runstate.NodeResult{Outcome: outcome, MaskedError: "changed"}
			if _, err := f.tenant.TransitionNode(t.Context(), finish); err == nil {
				t.Fatal("different terminal metadata overwrote result")
			}
			after, err := f.tenant.GetNode(t.Context(), string(f.ref.RunID), string(f.ref.NodeID))
			if err != nil || !reflect.DeepEqual(node, after) {
				t.Fatalf("overwrite changed node: %+v -> %+v, %v", node, after, err)
			}
		})
	}
}
