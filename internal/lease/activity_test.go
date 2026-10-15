package lease_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
)

func FuzzKeeperAdoptionRequiresExactUnit(f *testing.F) {
	for change := range uint8(6) {
		f.Add("run", "job", "holder", int64(2), change)
	}
	f.Fuzz(func(t *testing.T, run, node, holder string, generation int64, change uint8) {
		identity := lease.Identity{Kind: capability.UnitNode, RunID: run, NodeID: node, Holder: holder, Generation: generation}
		ctx, computing := lease.Bind(t.Context(), identity)
		candidate := identity
		switch change % 6 {
		case 1:
			candidate.RunID += "\x00"
		case 2:
			candidate.NodeID += "\x00"
		case 3:
			candidate.Holder += "\x00"
		case 4:
			candidate.Generation++
		case 5:
			candidate.Kind = capability.UnitTrigger
		}
		want := candidate == identity && run != "" && node != "" && holder != "" && generation > 0
		adopted := lease.Adopt(ctx, candidate, func() bool { return false })
		if adopted != want || computing() == want {
			t.Fatalf("identity=%+v candidate=%+v adopted=%v computing=%v", identity, candidate, adopted, computing())
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if lease.Adopt(cancelled, identity, func() bool { return true }) {
			t.Fatal("cancelled keeper accepted adoption")
		}
	})
}
