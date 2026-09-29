package store_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestFinishRunIfActiveHasOneCommittedWinner(t *testing.T) {
	target := storetest.New(t)
	first, second := target.Open(t), target.Open(t)
	if err := first.CreateRun(t.Context(), store.Run{ID: "run", Pipeline: "finish", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"", "running", "pending", "invalid"} {
		won, err := first.FinishRunIfActive(t.Context(), "run", status, "")
		if err == nil || won {
			t.Fatalf("nonterminal %q accepted: won=%t error=%v", status, won, err)
		}
	}
	before, err := first.GetRun(t.Context(), "run")
	if err != nil || before == nil || before.Status != "running" || before.FinishedAt != nil {
		t.Fatalf("invalid status changed run=%+v error=%v", before, err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	ready := make(chan struct{})
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			st := []*store.Store{first, second}[i%2]
			won, err := st.FinishRunIfActive(t.Context(), "run", "success", "")
			if err != nil {
				t.Error(err)
			}
			if won {
				wins.Add(1)
			}
		}()
	}
	close(ready)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("committed winners=%d, want one", wins.Load())
	}
	before, err = first.GetRun(t.Context(), "run")
	if err != nil || before == nil || before.FinishedAt == nil {
		t.Fatalf("winning run=%+v error=%v", before, err)
	}
	for _, status := range []string{"success", "failed", "cancelled"} {
		won, err := second.FinishRunIfActive(t.Context(), "run", status, "retry")
		if err != nil || won {
			t.Fatalf("terminal retry won=%t error=%v", won, err)
		}
	}
	after, err := second.GetRun(t.Context(), "run")
	if err != nil || after == nil || after.FinishedAt == nil || after.Status != "success" || after.Error != "" || !after.FinishedAt.Equal(*before.FinishedAt) {
		t.Fatalf("retry changed terminal state: before=%+v after=%+v error=%v", before, after, err)
	}
}

func TestFinishRunIfActivePreservesMutationFences(t *testing.T) {
	st := storetest.Open(t)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "token"}
	if err := st.CreateTrigger(t.Context(), store.Trigger{ID: "run", Pipeline: "finish", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimNextTriggerFor(t.Context(), identity, time.Minute, nil, nil)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v error=%v", claim, err)
	}
	if err := st.CreateRun(t.Context(), store.Run{ID: "run", Pipeline: "finish", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	fence := store.TriggerClaimFence{Claimant: identity, ClaimGeneration: claim.ClaimSeq + 1}
	for _, ctx := range []context.Context{store.WithTriggerClaimFence(t.Context(), fence), store.WithNodeClaimFence(t.Context(), store.NodeClaimFence{})} {
		won, err := st.FinishRunIfActive(ctx, "run", "success", "")
		if won || !errors.Is(err, store.ErrLockHeld) {
			t.Fatalf("unauthorized finish won=%t error=%v", won, err)
		}
	}
	fence.ClaimGeneration = claim.ClaimSeq
	if won, err := st.FinishRunIfActive(store.WithTriggerClaimFence(t.Context(), fence), "run", "success", ""); err != nil || !won {
		t.Fatalf("authorized finish won=%t error=%v", won, err)
	}
}
