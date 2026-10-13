package admission

import "testing"

func TestAPinnedLeaseKeepsItsGroupUntilThePinningMemberLeaves(t *testing.T) {
	l, err := New(Config{TotalCores: 4, TotalMemoryBytes: 8 << 30})
	if err != nil {
		t.Fatal(err)
	}
	group := []SemaphoreClaim{{Key: "deploy", Capacity: 1, Cost: 1, Policy: PolicyFail}}
	run, _, err := l.Submit(Request{ID: "parent", Semaphores: group})
	if err != nil {
		t.Fatal(err)
	}
	node, _, err := l.Submit(Request{ID: "parent/node", Cores: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Attach(node.Lease.ID, "child", ""); err != nil {
		t.Fatal(err)
	}
	if err := l.Pin("child", []LeaseID{run.Lease.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Release(run.Lease.ID, "parent"); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(l.Snapshot(), nil)
	if err != nil {
		t.Fatalf("restore a ledger holding a pinned lease: %v", err)
	}
	for _, ledger := range []*Ledger{l, restored} {
		if d, _, err := ledger.Submit(Request{ID: "other", Semaphores: group}); err != nil || d.Kind != DecisionFailed {
			t.Fatalf("another run took the pinned group: %+v %v", d, err)
		}
		if _, err := ledger.Release(node.Lease.ID, "child"); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.Release(node.Lease.ID, "parent/node"); err != nil {
			t.Fatal(err)
		}
		if d, _, err := ledger.Submit(Request{ID: "after", Semaphores: group}); err != nil || d.Kind != DecisionGranted {
			t.Fatalf("the group stayed held after the child left: %+v %v", d, err)
		}
	}
}
