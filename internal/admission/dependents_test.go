package admission

import (
	"slices"
	"testing"
)

func orphaned(events []Event) []string {
	var runs []string
	for _, ev := range events {
		if ev.Kind == EventOrphaned {
			runs = append(runs, ev.RequestID)
		}
	}
	return runs
}

func attachedChild(t *testing.T, policy Policy) (*Ledger, LeaseID, []SemaphoreClaim) {
	t.Helper()
	l, err := New(Config{TotalCores: 4, TotalMemoryBytes: 8 << 30})
	if err != nil {
		t.Fatal(err)
	}
	group := []SemaphoreClaim{{Key: "deploy", Capacity: 1, Cost: 1, Policy: policy}}
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
	if err := l.Depend("child", []LeaseID{run.Lease.ID, node.Lease.ID}); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(l.Snapshot(), nil)
	if err != nil {
		t.Fatalf("restore a ledger with a dependent: %v", err)
	}
	return restored, run.Lease.ID, group
}

func TestReleasingAnAncestorLeaseOrphansItsAttachedChild(t *testing.T) {
	l, run, _ := attachedChild(t, policyFail)
	events, err := l.Release(run, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if got := orphaned(events); !slices.Equal(got, []string{"child"}) {
		t.Fatalf("orphaned %v, want the child told to end with its parent's lease", got)
	}
}

func TestEvictingAnAncestorLeaseOrphansItsAttachedChild(t *testing.T) {
	l, _, group := attachedChild(t, PolicyCancelOthers)
	_, events, err := l.Submit(Request{ID: "newer", Semaphores: group})
	if err != nil {
		t.Fatal(err)
	}
	if got := orphaned(events); !slices.Equal(got, []string{"child"}) {
		t.Fatalf("orphaned %v, want the child evicted with the lease whose group it was handed", got)
	}
}

func TestAChildThatLeavesFirstIsNotOrphaned(t *testing.T) {
	l, run, _ := attachedChild(t, policyFail)
	snap := l.Snapshot()
	var node LeaseID
	for _, le := range snap.Leases {
		if le.RequestID == "parent/node" {
			node = le.ID
		}
	}
	if _, err := l.Release(node, "child"); err != nil {
		t.Fatal(err)
	}
	events, err := l.Release(run, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if got := orphaned(events); len(got) != 0 {
		t.Fatalf("orphaned %v after the child had already ended", got)
	}
}

func TestAnOrphanedChildIsMarkedOnItsOwnLeaseUntilItLeaves(t *testing.T) {
	l, run, _ := attachedChild(t, policyFail)
	if _, err := l.Release(run, "parent"); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(l.Snapshot(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Orphaned("child") {
		t.Fatal("the child's cancellation did not survive a restore")
	}
	var node LeaseID
	for _, le := range restored.Snapshot().Leases {
		if le.RequestID == "parent/node" {
			node = le.ID
		}
	}
	if _, err := restored.Release(node, "child"); err != nil {
		t.Fatal(err)
	}
	if restored.Orphaned("child") {
		t.Fatal("the mark outlived the child's membership")
	}
}
