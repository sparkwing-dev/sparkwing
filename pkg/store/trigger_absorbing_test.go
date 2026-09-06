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
		"CancelPendingTrigger": func(s *store.Store, ctx context.Context, id string) error {
			_, err := s.CancelPendingTrigger(ctx, id)
			return err
		},
		"ReapExpiredTriggers": func(s *store.Store, ctx context.Context, id string) error {
			_, err := store.Maintenance.ReapExpiredTriggers(s, ctx)
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
