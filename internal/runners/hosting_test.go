package runners

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

type runHostFunc func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error)

func (f runHostFunc) Run(ctx context.Context, in nodehost.RunInput) (nodehost.RunCompletion, error) {
	return f(ctx, in)
}

type nodeHostFunc func(context.Context, nodehost.NodeInput) error

func (f nodeHostFunc) Execute(ctx context.Context, in nodehost.NodeInput) error { return f(ctx, in) }

func hostingAward(kind capability.UnitKind) controllerwire.ClaimedUnit {
	binding := capability.Binding{RunID: "run", Holder: "holder", Generation: 2, Kind: capability.BindingTrigger}
	if kind == capability.UnitNode {
		binding.Kind, binding.NodeID, binding.Attempt = capability.BindingNode, "job", 3
	}
	return controllerwire.ClaimedUnit{Unit: kind, UnitID: "unit", RunID: "run", JobID: binding.NodeID, Binding: binding, Capability: "award-secret", LeaseExpiresAt: time.Now().Add(time.Minute)}
}

func TestHostingDispatchesUnderKeeperAndJoinsBeforeCleanup(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			award := hostingAward(kind)
			var events []string
			host := Hosting{Lease: time.Minute, Interval: 10 * time.Second}
			host.Renew = func(_ context.Context, got controllerwire.ClaimedUnit) (lease.Beat, error) {
				if !reflect.DeepEqual(got, award) {
					t.Fatal("renewal lost award authority")
				}
				events = append(events, "renew")
				return lease.Beat{}, nil
			}
			host.StartKeeper = func(ctx context.Context, k lease.Keeper) (context.Context, func()) {
				if k.Lease != time.Minute || k.Interval != 10*time.Second {
					t.Fatal("keeper timing changed")
				}
				events = append(events, "keeper")
				return ctx, func() { events = append(events, "join") }
			}
			cleanup := func() error { events = append(events, "close"); return nil }
			host.PrepareRun = func(_ context.Context, got controllerwire.ClaimedUnit) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
				if !reflect.DeepEqual(got, award) {
					t.Fatal("run preparation lost award")
				}
				events = append(events, "prepare")
				return runHostFunc(func(_ context.Context, in nodehost.RunInput) (nodehost.RunCompletion, error) {
					if kind != capability.UnitTrigger || in.RunID != "run" {
						t.Fatal("wrong run dispatch")
					}
					events = append(events, "run")
					return nodehost.RunCompletion{Outcome: "passed"}, nil
				}), nodehost.RunInput{RunID: "run"}, cleanup, nil
			}
			host.PrepareNode = func(_ context.Context, got controllerwire.ClaimedUnit) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				if !reflect.DeepEqual(got, award) {
					t.Fatal("node preparation lost award")
				}
				events = append(events, "prepare")
				return nodeHostFunc(func(_ context.Context, in nodehost.NodeInput) error {
					if kind != capability.UnitNode || in.NodeID != "job" {
						t.Fatal("wrong node dispatch")
					}
					events = append(events, "run")
					return nil
				}), nodehost.NodeInput{NodeID: "job"}, cleanup, nil
			}
			if err := host.Host(t.Context(), award); err != nil {
				t.Fatal(err)
			}
			if want := []string{"renew", "keeper", "prepare", "run", "join", "close"}; !reflect.DeepEqual(events, want) {
				t.Fatalf("events %v, want %v", events, want)
			}
		})
	}
}

func TestHostingPreparationFailureStopsKeeperAndClosesPartialSource(t *testing.T) {
	failure, cleanupFailure := errors.New("acquisition failed"), errors.New("cleanup failed")
	var stopped, closed bool
	host := Hosting{Lease: time.Minute, Interval: 10 * time.Second,
		Renew: func(context.Context, controllerwire.ClaimedUnit) (lease.Beat, error) { return lease.Beat{}, nil },
		StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
			return ctx, func() { stopped = true }
		},
		PrepareRun: func(context.Context, controllerwire.ClaimedUnit) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
			return nil, nodehost.RunInput{}, func() error {
				if !stopped {
					t.Error("source cleanup raced keeper")
				}
				closed = true
				return cleanupFailure
			}, failure
		},
	}
	err := host.Host(t.Context(), hostingAward(capability.UnitTrigger))
	if !errors.Is(err, failure) || !errors.Is(err, cleanupFailure) || !stopped || !closed {
		t.Fatalf("failure=%v stopped=%v closed=%v", err, stopped, closed)
	}
}

func TestHostingLostLeaseDuringPreparationCannotExecute(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	host := Hosting{Lease: time.Minute, Interval: 10 * time.Second,
		Renew:       func(context.Context, controllerwire.ClaimedUnit) (lease.Beat, error) { return lease.Beat{}, nil },
		StartKeeper: func(context.Context, lease.Keeper) (context.Context, func()) { return ctx, func() {} },
		PrepareNode: func(context.Context, controllerwire.ClaimedUnit) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			cancel(lease.ErrLeaseLost)
			return nodeHostFunc(func(context.Context, nodehost.NodeInput) error { t.Error("executed after lease loss"); return nil }), nodehost.NodeInput{}, nil, nil
		},
	}
	if err := host.Host(t.Context(), hostingAward(capability.UnitNode)); !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("Host = %v", err)
	}
}

func TestHostingRefusesInvalidAuthorityBeforeRenewal(t *testing.T) {
	for _, change := range []func(*controllerwire.ClaimedUnit){
		func(u *controllerwire.ClaimedUnit) { u.Binding.RunID = "other" },
		func(u *controllerwire.ClaimedUnit) { u.Binding.NodeID = "other" },
		func(u *controllerwire.ClaimedUnit) { u.Binding.Holder = "" },
		func(u *controllerwire.ClaimedUnit) { u.Binding.Generation = 0 },
		func(u *controllerwire.ClaimedUnit) { u.Binding.Attempt = 0 },
		func(u *controllerwire.ClaimedUnit) { u.LeaseExpiresAt = time.Time{} },
		func(u *controllerwire.ClaimedUnit) {
			deadline := time.Now().Add(-time.Minute)
			u.Binding.Deadline = &deadline
		},
	} {
		u := hostingAward(capability.UnitNode)
		change(&u)
		host := Hosting{Lease: time.Minute, Interval: 10 * time.Second, Renew: func(context.Context, controllerwire.ClaimedUnit) (lease.Beat, error) {
			t.Error("renewed invalid award")
			return lease.Beat{}, nil
		}, PrepareNode: func(context.Context, controllerwire.ClaimedUnit) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			t.Error("prepared invalid award")
			return nil, nodehost.NodeInput{}, nil, nil
		}}
		if err := host.Host(t.Context(), u); err == nil {
			t.Fatal("invalid authority accepted")
		}
	}
}
