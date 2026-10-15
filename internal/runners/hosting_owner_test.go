package runners

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type hostingOwnerState struct {
	nodehost.OwnerStore
	snapshot store.RunSnapshot
}

func (s hostingOwnerState) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return s.snapshot, nil
}

func (hostingOwnerState) FinishRunAndUnit(context.Context, store.FinishRunRequest) (store.Applied, error) {
	return store.Applied{}, nil
}

type hostingActivityController struct {
	controllerwire.Controller
	renew func(context.Context, controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error)
}

func (c hostingActivityController) Renew(ctx context.Context, _ string, request controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	return c.renew(ctx, request)
}

func TestHostingAndOwnerShareOneKeeperAndActivity(t *testing.T) {
	unit := hostingAward(capability.UnitTrigger)
	spec := runspec.Spec{Request: unit.Request, Hash: "spec", PlanHash: "plan"}
	unit.Accepted = &controllerwire.PlanAccepted{Spec: spec, Plan: []byte(`{"jobs":[]}`), PlanHash: "plan"}
	input, err := nodehost.NewRunInput(unit)
	if err != nil {
		t.Fatal(err)
	}
	state := hostingOwnerState{snapshot: store.RunSnapshot{Run: store.RunRow{
		ID: unit.RunID, State: store.RunRunning, Request: unit.Request, Owner: &input.Owner.Owner,
		Spec: &spec, Plan: unit.Accepted.Plan, PlanHash: "plan",
	}}}
	var innerRenewals, starts atomic.Int32
	var preparing, computing atomic.Bool
	preparing.Store(true)
	var renew func(context.Context) (lease.Beat, error)
	check := func(ctx context.Context, executing bool) {
		t.Helper()
		computing.Store(executing)
		if _, err := renew(ctx); err != nil {
			t.Fatal(err)
		}
	}
	owner := &nodehost.OwnerHost{
		Store: state, Lease: lease.Keeper{Lease: time.Minute, Interval: 10 * time.Second},
		Mask: func(value string) string { return value },
		Renew: func(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
			innerRenewals.Add(1)
			return store.LeaseBeat{}, nil
		},
		Computing: func(string) bool { return computing.Load() },
		Acquire: func(ctx context.Context, _ nodehost.RunInput) (*source.Entry, error) {
			check(ctx, false)
			return &source.Entry{}, nil
		},
		Around: func(ctx context.Context, _ nodehost.AcceptedRun, body func(context.Context) (nodehost.RunCompletion, error)) (nodehost.RunCompletion, error) {
			preparing.Store(false)
			check(ctx, false)
			check(ctx, true)
			check(ctx, false)
			return body(ctx)
		},
		Wait: func(context.Context, store.RunSnapshot) error { return errors.New("empty plan should settle") },
	}
	host := Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
			return hostingActivityController{renew: func(_ context.Context, request controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
				if want := preparing.Load() || computing.Load(); request.Executing != want {
					t.Errorf("renewal executing=%v, want current activity=%v", request.Executing, want)
				}
				return controllerwire.RenewalResponse{}, nil
			}}, nil
		},
		StartKeeper: func(ctx context.Context, keeper lease.Keeper) (context.Context, func()) {
			starts.Add(1)
			renew = keeper.Renew
			ready := make(chan struct{})
			var observed sync.Once
			keeper.Observe = func(lease.Beat) { observed.Do(func() { close(ready) }) }
			ctx, stop := keeper.Run(ctx)
			select {
			case <-ready:
			case <-ctx.Done():
			}
			return ctx, stop
		},
		PrepareRun: func(ctx context.Context, _ controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
			check(ctx, false)
			return owner, input, nil, nil
		},
	}
	if err := host.Host(t.Context(), unit); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || innerRenewals.Load() != 0 {
		t.Fatalf("keepers=%d nested owner renewals=%d", starts.Load(), innerRenewals.Load())
	}
}
