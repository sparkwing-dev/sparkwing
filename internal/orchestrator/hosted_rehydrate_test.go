package orchestrator

import (
	"path/filepath"
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

func TestHostedRetryRehydratesOnlyPartialManualRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		none bool
	}{
		{"not a retry", Options{}, true},
		{"full", Options{RetryOf: "source", Full: true}, true},
		{"manual", Options{RetryOf: "source", RetrySource: store.RetrySourceManual}, false},
	} {
		r := hostedRehydration(tc.opts)
		if tc.none != (r == nil) {
			t.Errorf("%s: rehydration %+v, want none=%t", tc.name, r, tc.none)
		}
		if r != nil && r.SourceRunID != "source" {
			t.Errorf("%s: rehydration %+v, want source run", tc.name, r)
		}
	}
}
