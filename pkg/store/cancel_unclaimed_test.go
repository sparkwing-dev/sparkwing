package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestRequestCancel_FinalizesAnUnclaimedRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-unclaimed", "deploy")

	if err := s.RequestCancel(ctx, "run-unclaimed"); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}

	run, err := s.GetRun(ctx, "run-unclaimed")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", run.Status)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ClaimNextTrigger after cancel: err=%v, want ErrNotFound", err)
	}
	if _, err := s.ClaimSpecificTrigger(ctx, "run-unclaimed", time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ClaimSpecificTrigger after cancel: err=%v, want ErrNotFound", err)
	}
}

func TestRequestCancel_ClaimedRunKeepsTheCooperativePath(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-held", "deploy")
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}

	if err := s.RequestCancel(ctx, "run-held"); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}

	run, err := s.GetRun(ctx, "run-held")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending" {
		t.Fatalf("claimed run finalized to %q; its holder must wind it down", run.Status)
	}
	flagged, err := s.HeartbeatTrigger(ctx, "run-held", time.Minute)
	if err != nil {
		t.Fatalf("HeartbeatTrigger: %v", err)
	}
	if !flagged {
		t.Fatal("holder's lease renewal does not report the cancel")
	}
}

func TestClaim_RefusesACancelledTriggerReturnedToTheQueue(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedSubmittedRun(t, s, "run-requeued", "deploy")
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestCancel(ctx, "run-requeued"); err != nil {
		t.Fatal(err)
	}
	requeued, err := s.RequeueUnstartedClaim(ctx, "run-requeued")
	if err != nil || !requeued {
		t.Fatalf("RequeueUnstartedClaim = %v, %v", requeued, err)
	}

	if _, err := s.ClaimSpecificTrigger(ctx, "run-requeued", time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ClaimSpecificTrigger: err=%v, want ErrNotFound", err)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ClaimNextTrigger: err=%v, want ErrNotFound", err)
	}
	run, err := s.GetRun(ctx, "run-requeued")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", run.Status)
	}
	trig, err := s.GetTrigger(ctx, "run-requeued")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status == "pending" {
		t.Fatal("refused trigger is left pending in the queue")
	}
}

// Whichever of cancel and claim lands first, the run either never
// reaches a runner or reaches one that is told to cancel.
func TestRequestCancel_RaceWithClaim(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	for i := range 20 {
		id := fmt.Sprintf("run-race-%d", i)
		seedSubmittedRun(t, s, id, "deploy")

		var wg sync.WaitGroup
		var claimErr, cancelErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, claimErr = s.ClaimSpecificTrigger(ctx, id, time.Minute)
		}()
		go func() {
			defer wg.Done()
			<-start
			cancelErr = s.RequestCancel(ctx, id)
		}()
		close(start)
		wg.Wait()
		if cancelErr != nil {
			t.Fatalf("%s: RequestCancel: %v", id, cancelErr)
		}
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case claimErr == nil:
			if run.Status != "pending" {
				t.Fatalf("%s: claim won but the run was finalized to %q under its holder", id, run.Status)
			}
			flagged, err := s.HeartbeatTrigger(ctx, id, time.Minute)
			if err != nil {
				t.Fatalf("%s: HeartbeatTrigger: %v", id, err)
			}
			if !flagged {
				t.Fatalf("%s: claim won but its holder is never told to cancel", id)
			}
		case errors.Is(claimErr, store.ErrNotFound):
			if run.Status != "cancelled" {
				t.Fatalf("%s: cancel won but the run is %q", id, run.Status)
			}
		default:
			t.Fatalf("%s: claim: %v", id, claimErr)
		}
	}
}
