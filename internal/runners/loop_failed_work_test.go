package runners

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func TestFailOnFailedWorkReportsExecutedFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		fail     bool
		outcomes []nodehost.WorkOutcome
		want     []string
	}{
		"failed job and run": {true, []nodehost.WorkOutcome{{RunID: "run-1", JobID: "deploy", State: "failed"}, {RunID: "run-1", State: "failed"}}, []string{"job deploy in run run-1 failed", "run run-1 failed", "sparkwing runs status --run run-1"}},
		"cancelled job":      {true, []nodehost.WorkOutcome{{RunID: "run-1", JobID: "test", State: "cancelled"}}, []string{"job test in run run-1 cancelled"}},
		"success":            {true, []nodehost.WorkOutcome{{RunID: "run-1", JobID: "deploy", State: "success"}, {RunID: "run-1", JobID: "lint", State: "skipped"}, {RunID: "run-1", State: "success"}}, nil},
		"long-lived runner":  {false, []nodehost.WorkOutcome{{RunID: "run-1", State: "failed"}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			loop := &Loop{
				Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1, MaxClaims: 1, FailOnFailedWork: tc.fail},
				Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitTrigger, UnitID: "unit", RunID: "run-1", Capability: "private"}}, nil
				}),
				Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
					for _, outcome := range tc.outcomes {
						nodehost.OutcomeNotify(ctx)(outcome)
					}
					return nil
				}),
				CheckVersion: func(context.Context, string) error { return nil },
			}
			err := loop.Run(t.Context())
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Run = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed work exited cleanly")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Run = %q, missing %q", err, want)
				}
			}
		})
	}
}

func TestFailOnFailedWorkIdleExitWithoutWorkSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		loop := &Loop{
			Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1, IdleExit: time.Minute, PollInterval: time.Second, FailOnFailedWork: true},
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				return controllerwire.ClaimResponse{}, nil
			}),
			Host:         hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected host"); return nil }),
			CheckVersion: func(context.Context, string) error { return nil },
		}
		if err := loop.Run(t.Context()); err != nil {
			t.Fatalf("Run = %v", err)
		}
	})
}
