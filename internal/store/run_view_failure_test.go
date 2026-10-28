package store_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

// A host that fails before start (a pipeline that does not compile) leaves no attempt, so the job's masked error is
// the only account the run view can give of the failure.
func TestRunViewNamesTheJobOfAnUnstartedFailure(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.New(t))
	result := runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: "internal", MaskedError: "install source entry: ./pipeline.go:3:9: cannot use *** as int value"}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0), Result: &result, Outcome: runstate.NodeFailed}); err != nil {
		t.Fatal(err)
	}
	view, err := f.tenant.ReadRunView(t.Context(), string(f.ref.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if want := []store.RunFailure{{JobID: string(f.ref.NodeID), Error: result.MaskedError}}; len(view.Attempts) != 0 || !reflect.DeepEqual(view.Failures, want) {
		t.Fatalf("unstarted failure view: attempts=%+v failures=%+v", view.Attempts, view.Failures)
	}
}
