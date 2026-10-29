package nodehost

import (
	"context"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type releaseSnapshotCounter struct {
	OwnerStore
	reads int
}

func (s *releaseSnapshotCounter) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	s.reads++
	return s.OwnerStore.ReadSnapshot(ctx, run)
}

func TestOwnerReleaseDoesNotReadThePlanAgainForEachJob(t *testing.T) {
	fixture, host, input := newOwnerFixture(t, true)
	snapshots := &releaseSnapshotCounter{OwnerStore: host.Store}
	host.Store = snapshots
	setTestPolicies(&host, func(context.Context, testPolicyName, EvaluationInput, runstate.NodeRow, StorageBinding) (ownerLayerResult, error) {
		return ownerLayerResult{}, nil
	})
	host.Local = func(runstate.NodeRow) bool { return false }
	for index := range 200 {
		row := runstate.NodeRow{Ref: runspec.NodeRef{RunID: "run", NodeID: runspec.NodeID(fmt.Sprintf("job-%03d", index))}, Status: runstate.StatusPending}
		fixture.snapshot.Nodes = append(fixture.snapshot.Nodes, row)
	}
	for _, row := range fixture.snapshot.Nodes {
		if err := host.release(t.Context(), input, *input.Accepted, &source.Entry{}, fixture.snapshot.Run, row, store.DecisionReady, func(context.Context, store.ReconcileRequest) error {
			t.Fatal("remote release entered executor")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if snapshots.reads != 0 {
		t.Fatalf("releasing 200 jobs reread the complete plan %d times", snapshots.reads)
	}
}
