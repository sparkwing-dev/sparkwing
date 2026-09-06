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
