package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestATerminalRunAlwaysCarriesAFinishTime(t *testing.T) {
	for _, status := range []string{"success", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			s := storetest.Open(t)
			ctx := context.Background()
			before := time.Now()
			if err := s.CreateRun(ctx, store.Run{
				ID: "run-" + status, Pipeline: "p", Status: status, StartedAt: time.Now(),
			}); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}

			run, err := s.GetRun(ctx, "run-"+status)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if run.FinishedAt == nil {
				t.Fatalf("a %q run carries no finish time, so a reader cannot tell it from an "+
					"earlier attempt's leftover row and nothing closes out the claim it belongs to", status)
			}
			if run.FinishedAt.Before(before) || run.FinishedAt.After(time.Now()) {
				t.Errorf("finish time %v falls outside the call that wrote it; a stamp that is not "+
					"when the run ended makes every later comparison against a claim time wrong",
					run.FinishedAt)
			}
		})
	}
}

func TestAPendingRunCarriesNoFinishTime(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-pending", Pipeline: "p", Status: "pending", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	run, err := s.GetRun(ctx, "run-pending")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.FinishedAt != nil {
		t.Error("a pending run was stamped as finished")
	}
}

func TestCreateRunKeepsTheFinishTimeItsCallerSupplied(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	ended := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-replayed", Pipeline: "p", Status: "success",
		StartedAt: ended, FinishedAt: &ended,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	run, err := s.GetRun(ctx, "run-replayed")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !run.FinishedAt.Equal(ended) {
		t.Fatalf("finish time = %v, want %v: a replay carries when the run really ended, and "+
			"restamping it now can make a stale row read as this claim's work", run.FinishedAt, ended)
	}
}

func TestAPendingRunStampedTerminalCarriesAFinishTime(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-upserted", Pipeline: "p", Status: "pending", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-upserted", Pipeline: "p", Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun over a pending row: %v", err)
	}

	run, err := s.GetRun(ctx, "run-upserted")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("status = %q, want failed", run.Status)
	}
	if run.FinishedAt == nil {
		t.Fatal("a pending run moved to a terminal status carries no finish time; this is the path " +
			"a dispatch failure takes, so the claim it belongs to is never closed out")
	}
}
