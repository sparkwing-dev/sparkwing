package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func claimTrigger(t *testing.T, s *store.Store, id, pipeline string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: pipeline, CreatedAt: time.Now(), TriggerSource: "runs-submit",
	}); err != nil {
		t.Fatalf("seed trigger %s: %v", id, err)
	}
	claimed, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim %s: %v", id, err)
	}
	if claimed.ID != id {
		t.Fatalf("claimed %s, want %s", claimed.ID, id)
	}
}

// TestRequeueUnstartedClaim_AnswersByRunStatus walks the four states the
// requeue distinguishes. The run status now lives in the UPDATE's own WHERE
// rather than in a read before it, so this pins that the answers did not
// move when the check did.
func TestRequeueUnstartedClaim_AnswersByRunStatus(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		runStatus string
		noRun     bool
		claim     bool
		want      bool
	}{
		{name: "no run row yet", noRun: true, claim: true, want: true},
		{name: "run still pending", runStatus: "pending", claim: true, want: true},
		{name: "run already running", runStatus: "running", claim: true, want: false},
		{name: "run already finished", runStatus: "success", claim: true, want: false},
		{name: "trigger was never claimed", runStatus: "pending", claim: false, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStoreT(t)
			if tc.claim {
				claimTrigger(t, s, "run-1", "deploy")
			} else if err := s.CreateTrigger(ctx, store.Trigger{
				ID: "run-1", Pipeline: "deploy", CreatedAt: time.Now(), TriggerSource: "runs-submit",
			}); err != nil {
				t.Fatalf("seed trigger: %v", err)
			}
			if !tc.noRun {
				if err := s.CreateRun(ctx, store.Run{
					ID: "run-1", Pipeline: "deploy", Status: tc.runStatus,
					CreatedAt: time.Now(), StartedAt: time.Now(),
				}); err != nil {
					t.Fatalf("seed run: %v", err)
				}
			}

			got, err := s.RequeueUnstartedClaim(ctx, "run-1")
			if err != nil {
				t.Fatalf("RequeueUnstartedClaim: %v", err)
			}
			if got != tc.want {
				t.Fatalf("RequeueUnstartedClaim = %v, want %v", got, tc.want)
			}

			trig, err := s.GetTrigger(ctx, "run-1")
			if err != nil {
				t.Fatalf("GetTrigger: %v", err)
			}
			wantStatus := "claimed"
			if !tc.claim {
				wantStatus = "pending"
			} else if tc.want {
				wantStatus = "pending"
			}
			if trig.Status != wantStatus {
				t.Errorf("trigger status = %q, want %q", trig.Status, wantStatus)
			}
			if tc.want && trig.LeaseExpiresAt != nil {
				t.Errorf("requeued trigger kept a lease: %v", trig.LeaseExpiresAt)
			}
		})
	}
}
