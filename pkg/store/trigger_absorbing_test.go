package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestFinishedTriggerIsNeverReturnedToTheQueue(t *testing.T) {
	revivals := map[string]func(*store.Store, context.Context, string) error{
		"ClaimNextTrigger": func(s *store.Store, ctx context.Context, id string) error {
			_, err := s.ClaimNextTrigger(ctx, time.Minute)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		},
		"RequeueUnstartedClaim": func(s *store.Store, ctx context.Context, id string) error {
			_, err := s.RequeueUnstartedClaim(ctx, id)
			return err
		},
		"ReleaseClaimAtGeneration": func(s *store.Store, ctx context.Context, id string) error {
			seq, err := s.TriggerClaimGeneration(ctx, id)
			if err != nil {
				return err
			}
			_, err = s.ReleaseClaimAtGeneration(ctx, id, seq)
			return err
		},
	}

	for name, revive := range revivals {
		t.Run(name, func(t *testing.T) {
			s := storetest.Open(t)
			ctx := context.Background()
			const id = "run-finished"
			if err := s.CreateTrigger(ctx, store.Trigger{
				ID: id, Pipeline: "p", CreatedAt: time.Now().Add(-time.Hour),
			}); err != nil {
				t.Fatalf("CreateTrigger: %v", err)
			}
			if err := s.FinishTrigger(ctx, id); err != nil {
				t.Fatalf("FinishTrigger: %v", err)
			}

			if err := revive(s, ctx, id); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			got, err := s.GetTrigger(ctx, id)
			if err != nil {
				t.Fatalf("GetTrigger: %v", err)
			}
			if !got.IsFinished() {
				t.Fatalf("%s moved a finished trigger to %q; callers reclaim what a finished "+
					"trigger owns, so reviving one hands a live dispatch a deleted tree", name, got.Status)
			}
		})
	}
}

func TestCancellingATriggerDoesNotOverwriteARunThatAlreadyEnded(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	const id = "run-settled"
	if err := s.CreateTrigger(ctx, store.Trigger{ID: id, Pipeline: "p", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID: id, Pipeline: "p", Status: "success", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	cancelled, err := s.CancelPendingTrigger(ctx, id)
	if err != nil {
		t.Fatalf("CancelPendingTrigger: %v", err)
	}
	if !cancelled {
		t.Fatal("cancelling a pending trigger reported it reached nothing")
	}

	run, gerr := s.GetRun(ctx, id)
	if gerr != nil {
		t.Fatalf("GetRun: %v", gerr)
	}
	if run.Status != "success" {
		t.Errorf("run status = %q, want success: a cancel must not overwrite an outcome "+
			"the finished trigger already produced", run.Status)
	}
}

func TestFinishingATriggerClearsItsLease(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	const id = "run-leased"
	if err := s.CreateTrigger(ctx, store.Trigger{ID: id, Pipeline: "p", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	if err := s.FinishTrigger(ctx, id); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}

	trig, err := s.GetTrigger(ctx, id)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.LeaseExpiresAt != nil {
		t.Error("a finished trigger kept its lease, so the expired-claim reaper can still reach it")
	}
}
