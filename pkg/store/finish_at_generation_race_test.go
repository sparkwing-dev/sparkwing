package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func reclaimAndSucceed(t *testing.T, s *store.Store, id string) {
	t.Helper()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Errorf("begin re-claim %s: %v", id, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		storetest.Rebind(s, `UPDATE triggers SET claim_seq = claim_seq + 1 WHERE id = ?`), id); err != nil {
		t.Errorf("re-claim %s: %v", id, err)
		return
	}
	if _, err := tx.Exec(
		storetest.Rebind(s, `UPDATE runs SET status = 'success', error = '', finished_at = ? WHERE id = ?`),
		time.Now().UnixNano(), id); err != nil {
		t.Errorf("current claim finishes %s: %v", id, err)
		return
	}
	if err := tx.Commit(); err != nil {
		t.Errorf("commit re-claim %s: %v", id, err)
	}
}

// TestFinishAtGeneration_ConcurrentReclaimKeepsTheCurrentOutcome races a
// superseded dispatch's finish against the re-claim that superseded it. The
// re-claim raises the generation and writes the run itself, so whichever
// order the two land in, the run must end up with the current claim's
// outcome: a dispatch whose read saw the old generation may only write while
// that generation is still current, and holding the trigger row across the
// read and the write is what makes that true. Split into two statements, the
// superseded dispatch's "failed" lands on top of the live "success".
func TestFinishAtGeneration_ConcurrentReclaimKeepsTheCurrentOutcome(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	const rounds = 500
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("run-race-%d", i)
		seedSubmittedRun(t, s, id, "deploy")
		stale, err := s.ClaimNextTrigger(ctx, time.Minute)
		if err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
		if stale.ID != id {
			t.Fatalf("claimed %s, want %s: another trigger was still pending", stale.ID, id)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.FinishRunAtGeneration(ctx, id, stale.ClaimSeq, "failed", "superseded"); err != nil {
				t.Errorf("superseded dispatch finishes %s: %v", id, err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			reclaimAndSucceed(t, s, id)
		}()
		close(start)
		wg.Wait()

		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun %s: %v", id, err)
		}
		if run.Status != "success" {
			t.Fatalf("round %d: run status = %q, want success: the superseded dispatch "+
				"wrote its outcome over the live claim's", i, run.Status)
		}
	}
}
