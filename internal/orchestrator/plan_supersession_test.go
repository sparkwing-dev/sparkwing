package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var supersessionPipelineID atomic.Uint64

type supersededPlanPipe struct {
	sparkwing.Base
	started     chan struct{}
	waitForSlot bool
	waitForPlan bool
	finished    <-chan struct{}
}

func (p *supersededPlanPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("parcel-publish", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.CancelOthers,
	}))
	if p.waitForPlan {
		plan.Concurrency(sparkwing.NewConcurrencyGroup("parcel-z-publish", sparkwing.ConcurrencyLimit{Capacity: 1}))
	}
	node := sparkwing.Job(plan, "publish", func(ctx context.Context) error {
		close(p.started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.finished:
			return nil
		}
	})
	if p.waitForSlot {
		node.Concurrency(sparkwing.NewConcurrencyGroup("parcel-node", sparkwing.ConcurrencyLimit{Capacity: 1}))
	}
	return nil
}

type supersededPlanStore struct {
	fakeConcurrency
	started      chan struct{}
	waitForSlot  bool
	waitForPlan  bool
	withdrawn    bool
	heartbeatErr error
	retained     bool
	finished     chan struct{}
}

func (s *supersededPlanStore) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	if (s.waitForSlot && req.NodeID != "") || (s.waitForPlan && req.Key == "g:parcel-z-publish") {
		close(s.started)
		return store.AcquireSlotResponse{Kind: store.AcquireQueued}, nil
	}
	return s.fakeConcurrency.AcquireSlot(ctx, req)
}

func (s *supersededPlanStore) CancelWaiter(ctx context.Context, _, _, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.withdrawn = true
	return true, nil
}

func (s *supersededPlanStore) HeartbeatSlot(ctx context.Context, _, _ string, lease time.Duration) (time.Time, bool, error) {
	select {
	case <-s.started:
		if s.retained {
			close(s.finished)
			return time.Now().Add(lease), false, nil
		}
		return time.Now().Add(lease), s.heartbeatErr == nil, s.heartbeatErr
	case <-ctx.Done():
		return time.Time{}, false, ctx.Err()
	}
}

func TestPlanSupersessionCancelsNode(t *testing.T) {
	for _, tc := range []struct {
		name         string
		waitForSlot  bool
		waitForPlan  bool
		heartbeatErr error
		retained     bool
	}{
		{name: "retained/executing", retained: true},
		{name: "superseded/executing"},
		{name: "superseded/waiting", waitForSlot: true},
		{name: "superseded/plan-waiting", waitForPlan: true},
		{name: "lease-lost/plan-waiting", waitForPlan: true, heartbeatErr: store.ErrLockHeld},
		{name: "lease-lost/executing", heartbeatErr: store.ErrLockHeld},
		{name: "lease-lost/waiting", waitForSlot: true, heartbeatErr: store.ErrLockHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{})
				finished := make(chan struct{})
				pipelineName := fmt.Sprintf("parcel-publish-supersession-%d", supersessionPipelineID.Add(1))
				register(pipelineName, func() sparkwing.Pipeline[sparkwing.NoInputs] {
					return &supersededPlanPipe{started: started, waitForSlot: tc.waitForSlot, waitForPlan: tc.waitForPlan, finished: finished}
				})
				fakes := newFakeBackends()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				coordination := &supersededPlanStore{started: started, waitForSlot: tc.waitForSlot, waitForPlan: tc.waitForPlan, heartbeatErr: tc.heartbeatErr, retained: tc.retained, finished: finished}
				result, err := orchestrator.Run(ctx, orchestrator.Backends{
					State:       fakes.state,
					Logs:        fakes.logs,
					Concurrency: coordination,
				}, orchestrator.Options{Pipeline: pipelineName})
				if ctx.Err() != nil {
					t.Fatalf("superseded plan waited for caller timeout instead of cancelling: result=%+v err=%v", result, err)
				}
				if tc.retained {
					if result == nil || result.Status != "success" {
						t.Fatalf("retained plan result=%+v err=%v, want success", result, err)
					}
					return
				}
				if result == nil || result.Status != "cancelled" {
					t.Fatalf("superseded plan result=%+v err=%v, want cancelled", result, err)
				}
				if result.Error == nil || !strings.Contains(result.Error.Error(), "parcel-publish") || strings.Contains(result.Error.Error(), "before dispatch") {
					t.Fatalf("eviction diagnostic = %v, want group identity without claiming dispatch never started", result.Error)
				}
				if (tc.waitForSlot || tc.waitForPlan) && !coordination.withdrawn {
					t.Fatal("superseded node remained in the concurrency queue")
				}
			})
		})
	}
}
