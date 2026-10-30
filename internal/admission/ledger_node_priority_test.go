package admission

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestNodeParticipantPrioritySurvivesOtherParticipantRelease(t *testing.T) {
	l := testLedger(t, 1, 0)
	hog := mustGrant(t, l, Request{ID: "hog", Cores: 1})
	first := string(wingwire.NodeHostParticipant("first-run", "first"))
	second := string(wingwire.NodeHostParticipant("second-run", "second"))
	mustQueue(t, l, Request{ID: first, Cores: 1})
	mustQueue(t, l, Request{ID: second, Cores: 1})
	changed, _ := l.SetPriority("second-run", 5)
	if changed != 1 {
		t.Fatalf("changed %d participants, want 1", changed)
	}
	if p := l.EffectivePriority(string(wingwire.NodeHostParticipant("second-run", "later")), "", 0); p != 5 {
		t.Fatalf("later participant priority = %d, want 5", p)
	}
	mustRelease(t, l, hog.ID, "hog")
	snap := l.Snapshot()
	if len(snap.Leases) != 1 || snap.Leases[0].RequestID != second {
		t.Fatalf("promoted leases: %+v", snap.Leases)
	}
	if p := snap.PriorityOverrides["second-run"]; p != 5 {
		t.Fatalf("override after unrelated release = %d, want 5", p)
	}
	mustRelease(t, l, snap.Leases[0].ID, second)
	if _, ok := l.Snapshot().PriorityOverrides["second-run"]; ok {
		t.Fatal("priority override outlived its run")
	}
}
