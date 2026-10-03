package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestClaimPlacement_HeldPollReadsByBatchWithoutWrites(t *testing.T) {
	s, recorder := newRecordingExecutorStore(t)
	ctx := WithClaimPlacement(t.Context(), ClaimPlacement{
		Hold: time.Hour,
		Live: []RunnerPresence{{Name: "laptop", Labels: []string{"location=local"}, FreeSlots: 2}},
	})
	if err := s.CreateRun(ctx, Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CoordinatorID(ctx); err != nil {
		t.Fatal(err)
	}
	seeded, firstReads := 0, 0
	for _, count := range []int{1, claimScanBatch - 1, claimScanBatch} {
		for ; seeded < count; seeded++ {
			id := fmt.Sprintf("held%03d", seeded)
			if err := s.CreateNode(ctx, Node{RunID: "run", NodeID: id, Status: "pending", PrefersLabels: []string{"location=local"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkNodeReady(ctx, "run", id); err != nil {
				t.Fatal(err)
			}
		}
		recorder.reset()
		_, err := s.ClaimNextReadyNode(ctx, ClaimIdentity{Principal: "cloud", TokenPrefix: "swr_cloud"},
			"runner:cloud:1", time.Minute, []string{"location=cloud"})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("poll over %d held nodes: %v", count, err)
		}
		statements := recorder.snapshot()
		if len(statements) == 0 {
			t.Fatal("held poll recorded no reads")
		}
		for _, statement := range statements {
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statement)), "SELECT ") {
				t.Fatalf("held poll issued a non-read statement: %s", statement)
			}
		}
		if count == 1 {
			firstReads = len(statements)
		}
		// perf: a full batch needs an empty-page read to establish the end of the queue.
		if want := firstReads + count/claimScanBatch; len(statements) != want {
			t.Fatalf("reads over %d held nodes = %d, want %d including batch lookahead", count, len(statements), want)
		}
		t.Logf("%d held nodes: %d reads, no writes", count, len(statements))
	}
}
