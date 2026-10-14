package orchestrator

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestAHostedRetryCopiesOnlyWhenItPlannedItsSourcesPlan(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: "retry", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	plan := func(runID string) []byte {
		return []byte(`{"pipeline":"p","run_id":"` + runID + `","nodes":[{"id":"a"}]}`)
	}
	state := func(hash string) *hostedRunState {
		return &hostedRunState{StateBackend: localState{st: st}, delegate: quietRunLog{}, lineage: store.Run{RetryOf: "source"}, retryPlanHash: hash}
	}
	if err := state(hashBytes(plan("source"))).UpdatePlanSnapshot(ctx, "retry", plan("retry")); err != nil {
		t.Fatalf("the source's own plan under the retry's id: %v", err)
	}
	err = state(hashBytes([]byte(`{"pipeline":"p","run_id":"source","nodes":[]}`))).UpdatePlanSnapshot(ctx, "retry", plan("retry"))
	if err == nil || !strings.Contains(err.Error(), "retry provenance drift") {
		t.Fatalf("a changed plan: err=%v, want provenance drift, so no source result is copied into it", err)
	}
}

// hack: the store records an automatic retry's causes only when it creates an agent-loss retry, so the run row
// is the fake's.
type retryCauses struct{ StateBackend }

func (retryCauses) GetRun(context.Context, string) (*store.Run, error) {
	return &store.Run{ID: "retry", RetryCauseNodeIDs: []string{"build"}}, nil
}

func TestAHostedAutomaticRetryRunsItsCauses(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name   string
		opts   Options
		causes []string
		none   bool
	}{
		{"not a retry", Options{}, nil, true},
		{"full", Options{RetryOf: "source", Full: true}, nil, true},
		{"manual", Options{RetryOf: "source", RetrySource: store.RetrySourceManual}, nil, false},
		{"automatic", Options{RetryOf: "source", RetrySource: store.RetrySourceAuto}, []string{"build"}, false},
	} {
		r, err := hostedRehydration(ctx, retryCauses{}, tc.opts, "retry")
		switch {
		case err != nil:
			t.Fatalf("%s: %v", tc.name, err)
		case tc.none != (r == nil):
			t.Errorf("%s: rehydration %+v, want none=%v", tc.name, r, tc.none)
		case r != nil && (r.SourceRunID != "source" || !slices.Equal(r.Causes, tc.causes)):
			t.Errorf("%s: rehydration %+v, want source with causes %v", tc.name, r, tc.causes)
		}
	}
}
