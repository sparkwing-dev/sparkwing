package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func committed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// A held claim sleeps until a commit, so a write that makes work claimable must
// wake it, and the claim's own fruitless search must not, or it would spin.
func TestNextCommitWakesOnWritesOnly(t *testing.T) {
	f := admissionFixture(t, storetest.New(t), 1, 1)
	admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
	plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
		timeout := int64(1000)
		doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
	demand := store.UnitDemand{CapabilityPrefix: owner.CapabilityPrefix, Kinds: []capability.UnitKind{capability.UnitNode}}

	signal := f.s.NextCommit()
	if _, err := f.tenant.ReadNode(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if committed(signal) {
		t.Fatal("a read woke the commit signal")
	}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	if !committed(signal) {
		t.Fatal("releasing a job did not wake the commit signal")
	}

	// safety: the first search starts the job's group-wait clock, which is a write.
	signal = f.s.NextCommit()
	if _, err := f.s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("full group claim: %v", err)
	}
	if !committed(signal) {
		t.Fatal("starting a group-wait clock did not wake the commit signal")
	}
	signal = f.s.NextCommit()
	if _, err := f.s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("full group claim: %v", err)
	}
	if committed(signal) {
		t.Fatal("an empty claim search woke the commit signal")
	}
}

// A held claim must search again when a group wait runs out, because that
// search is what settles the waiting job.
func TestNextGroupWaitDeadlineNamesTheEarliestLapse(t *testing.T) {
	f := admissionFixture(t, storetest.New(t), 1, 1)
	admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
	plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
		timeout := int64(1000)
		doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	if _, timed, err := f.s.NextGroupWaitDeadline(t.Context()); err != nil || timed {
		t.Fatalf("deadline before any group wait started: timed=%v err=%v", timed, err)
	}
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	f.s.SetTestClock(t, func() time.Time { return now })
	demand := store.UnitDemand{CapabilityPrefix: owner.CapabilityPrefix, Kinds: []capability.UnitKind{capability.UnitNode}}
	if _, err := f.s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("full group claim: %v", err)
	}
	f.s.SetTestClock(t, func() time.Time { return now.Add(400 * time.Millisecond) })
	wait, timed, err := f.s.NextGroupWaitDeadline(t.Context())
	if err != nil || !timed || wait != 600*time.Millisecond {
		t.Fatalf("deadline 400ms into a 1s group wait: wait=%s timed=%v err=%v, want 600ms", wait, timed, err)
	}
}
