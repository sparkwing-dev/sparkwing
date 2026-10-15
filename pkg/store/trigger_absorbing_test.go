package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestFinishedTriggerCannotBeClaimedByName(t *testing.T) {
	revivals := map[string]func(*store.Store, context.Context, string) error{
		"ClaimSpecificTrigger": func(s *store.Store, ctx context.Context, id string) error {
			_, err := s.ClaimSpecificTrigger(ctx, id, time.Minute)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
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
			if err := teststore.ForTeam(t, s, store.DefaultTeam).FinishTrigger(ctx, id); err != nil {
				t.Fatalf("FinishTrigger: %v", err)
			}

			if err := revive(s, ctx, id); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			got, err := teststore.ForTeam(t, s, store.DefaultTeam).GetTrigger(ctx, id)
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

func TestFinishingATriggerClearsItsLease(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	const id = "run-leased"
	if err := s.CreateTrigger(ctx, store.Trigger{ID: id, Pipeline: "p", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if _, err := s.ClaimSpecificTrigger(ctx, id, time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTrigger: %v", err)
	}
	if err := teststore.ForTeam(t, s, store.DefaultTeam).FinishTrigger(ctx, id); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}

	trig, err := teststore.ForTeam(t, s, store.DefaultTeam).GetTrigger(ctx, id)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.LeaseExpiresAt != nil {
		t.Error("a finished trigger kept its lease, so the expired-claim reaper can still reach it")
	}
}
