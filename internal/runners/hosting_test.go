package runners

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type runHostFunc func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error)

func (f runHostFunc) Run(ctx context.Context, in nodehost.RunInput) (nodehost.RunCompletion, error) {
	return f(ctx, in)
}

type nodeHostFunc func(context.Context, nodehost.NodeInput) error

func (f nodeHostFunc) Execute(ctx context.Context, in nodehost.NodeInput) error { return f(ctx, in) }

func hostingAward(kind capability.UnitKind) controllerwire.ClaimedUnit {
	binding := capability.Binding{RunID: "run", Holder: "holder", Generation: 2, Kind: capability.BindingTrigger}
	purpose := capability.PurposeTrigger
	if kind == capability.UnitNode {
		binding.Kind, binding.NodeID, binding.Attempt = capability.BindingNode, "job", 3
		purpose = capability.PurposeWork
	}
	unit := controllerwire.ClaimedUnit{Unit: kind, Purpose: purpose, Delivery: capability.DeliveryDirect, UnitID: "unit", RunID: "run", JobID: binding.NodeID, Binding: binding, Capability: "award-secret", LeaseExpiresAt: time.Now().Add(time.Minute)}
	unit.Request = runspec.SealedRequest{RunID: unit.RunID, Hash: "request-hash", Request: runspec.Request{Team: "team"}}
	if kind == capability.UnitNode {
		unit.Engine = &engine.Identity{Version: "v0.20.0"}
		unit.Runner = nodeproto.RunnerIdentity{Name: "granted-runner", Type: "runner"}
		unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Hash: "spec-hash", Resolved: runspec.Resolved{Engine: *unit.Engine}}, Plan: []byte(`{"jobs":[{"id":"job"}]}`), PlanHash: "plan-hash"}
		unit.Execution = &runspec.JobExecutionSettings{}
	}
	return unit
}

func TestHostingDispatchesUnderKeeperAndJoinsBeforeCleanup(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			award := hostingAward(kind)
			var events []string
			host := Hosting{ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second}
			host.NewController = hostingClient(func(context.Context) (lease.Beat, error) {
				events = append(events, "renew")
				return lease.Beat{}, nil
			})
			host.StartKeeper = func(ctx context.Context, k lease.Keeper) (context.Context, func()) {
				if k.Lease != time.Minute || k.Interval != 10*time.Second {
					t.Fatal("keeper timing changed")
				}
				events = append(events, "keeper")
				return ctx, func() { events = append(events, "join") }
			}
			cleanup := func() error { events = append(events, "close"); return nil }
			host.PrepareRun = func(_ context.Context, got controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
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
			host.PrepareNode = func(_ context.Context, got controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
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
				}), hostingInput(got), cleanup, nil
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

func hostingInput(unit controllerwire.ClaimedUnit) nodehost.NodeInput {
	return nodehost.NodeInput{Entry: &source.Entry{Engine: engine.Resolved{Identity: *unit.Engine, Executable: "/selected/engine", Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}}}
}

func TestHostingPreservesCancellationReasonBeforeAcquisition(t *testing.T) {
	host := Hosting{ControllerURL: "https://controller.example", Lease: time.Minute, Interval: time.Second,
		NewController: hostingClient(func(context.Context) (lease.Beat, error) {
			return lease.Beat{CancelRequested: true, CancelReason: "requested by owner"}, nil
		}),
		PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			t.Fatal("cancelled award reached source acquisition")
			return nil, nodehost.NodeInput{}, nil, nil
		},
	}
	var cancelled *lease.CancelError
	if err := host.Host(t.Context(), hostingAward(capability.UnitNode)); !errors.As(err, &cancelled) || cancelled.Reason != "requested by owner" {
		t.Fatalf("cancellation reason was lost: %v", err)
	}
}

func TestHostingCapturesExecutionIdentityBeforePreparation(t *testing.T) {
	unit := hostingAward(capability.UnitNode)
	unit.Runner.Labels = []string{"pool=granted"}
	input := hostingInput(unit)
	executed := false
	host := Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: hostingClient(func(context.Context) (lease.Beat, error) {
			unit.Engine.Version = "v9.9.9"
			unit.Accepted.Spec.Resolved.Engine.Version = "v9.9.9"
			unit.Runner.Labels[0] = "pool=ambient"
			return lease.Beat{}, nil
		}),
		StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) { return ctx, func() {} },
		PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			return nodeHostFunc(func(_ context.Context, got nodehost.NodeInput) error {
				executed = true
				if got.Engine.Identity.Version != "v0.20.0" || got.GrantedRunner.Labels[0] != "pool=granted" {
					t.Error("execution reread changed award data after admission")
				}
				return nil
			}), input, nil, nil
		},
	}
	if err := host.Host(t.Context(), unit); err != nil || !executed {
		t.Fatalf("captured identity: executed=%v error=%v", executed, err)
	}
}

func TestHostingPreparationFailureStopsKeeperAndClosesPartialSource(t *testing.T) {
	failure, cleanupFailure := errors.New("acquisition failed"), errors.New("cleanup failed")
	var stopped, closed bool
	host := Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: hostingClient(func(context.Context) (lease.Beat, error) { return lease.Beat{}, nil }),
		StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
			return ctx, func() { stopped = true }
		},
		PrepareRun: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
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
	host := Hosting{
		ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
		NewController: hostingClient(func(context.Context) (lease.Beat, error) { return lease.Beat{}, nil }),
		StartKeeper:   func(context.Context, lease.Keeper) (context.Context, func()) { return ctx, func() {} },
		PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
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
		func(u *controllerwire.ClaimedUnit) { u.Binding.Attempt = -1 },
		func(u *controllerwire.ClaimedUnit) { u.LeaseExpiresAt = time.Time{} },
		func(u *controllerwire.ClaimedUnit) {
			deadline := time.Now().Add(-time.Minute)
			u.Binding.Deadline = &deadline
		},
	} {
		u := hostingAward(capability.UnitNode)
		change(&u)
		host := Hosting{ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second, NewController: hostingClient(func(context.Context) (lease.Beat, error) {
			t.Error("renewed invalid award")
			return lease.Beat{}, nil
		}), PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
			t.Error("prepared invalid award")
			return nil, nodehost.NodeInput{}, nil, nil
		}}
		if err := host.Host(t.Context(), u); err == nil {
			t.Fatal("invalid authority accepted")
		}
	}
}

type hostingRenewalClient struct {
	controllerwire.Controller
	renew func(context.Context) (lease.Beat, error)
}

func (c *hostingRenewalClient) Renew(ctx context.Context, _ string, _ controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	beat, err := c.renew(ctx)
	return controllerwire.RenewalResponse{CancelRequested: beat.CancelRequested, CancelReason: beat.CancelReason, Watched: beat.Watched, WatchedJobs: beat.WatchedNodes}, err
}

func hostingClient(renew func(context.Context) (lease.Beat, error)) func(client.EngineOptions) (controllerwire.Controller, error) {
	return func(client.EngineOptions) (controllerwire.Controller, error) {
		return &hostingRenewalClient{renew: renew}, nil
	}
}

func TestHostingControllerFailuresPreventPreparation(t *testing.T) {
	failure := errors.New("controller unavailable")
	for _, tc := range []struct {
		name string
		open func(client.EngineOptions) (controllerwire.Controller, error)
	}{
		{"connect", func(client.EngineOptions) (controllerwire.Controller, error) { return nil, failure }},
		{"no_client", func(client.EngineOptions) (controllerwire.Controller, error) { return nil, nil }},
		{"confirm", hostingClient(func(context.Context) (lease.Beat, error) { return lease.Beat{}, failure })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := Hosting{
				ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
				NewController: tc.open,
				StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) {
					t.Error("started keeper without controller authority")
					return ctx, func() {}
				},
				PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
					t.Error("prepared source without controller authority")
					return nil, nodehost.NodeInput{}, nil, nil
				},
			}
			err := host.Host(t.Context(), hostingAward(capability.UnitNode))
			if err == nil || tc.name != "no_client" && !errors.Is(err, failure) {
				t.Fatalf("controller failure = %v", err)
			}
		})
	}
}
