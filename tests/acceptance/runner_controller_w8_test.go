package acceptance_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type runnerControllerW8 struct {
	controllerwire.Controller
	claim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
	renew func(context.Context, string, controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error)
}

func (c *runnerControllerW8) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.claim(ctx, request)
}

func (c *runnerControllerW8) Renew(ctx context.Context, unit string, request controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	return c.renew(ctx, unit, request)
}

func runnerControllerFactoryW8(renew func(context.Context) (lease.Beat, error)) func(client.EngineOptions) (controllerwire.Controller, error) {
	return func(client.EngineOptions) (controllerwire.Controller, error) {
		return &runnerControllerW8{renew: func(ctx context.Context, _ string, _ controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
			beat, err := renew(ctx)
			return controllerwire.RenewalResponse{CancelRequested: beat.CancelRequested, Watched: beat.Watched, WatchedJobs: beat.WatchedNodes}, err
		}}, nil
	}
}

func TestRunnerControllerW8Components(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			unit := runnerAuthorityAwardW8(kind)
			unit.Runner.Name = "server-runner"
			var events []string
			watched := []string{"watched-job"}
			bound := &runnerControllerW8{renew: func(_ context.Context, id string, request controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
				if id != unit.UnitID || request.Executing {
					t.Fatalf("renewal lost its unit or started execution: %s %+v", id, request)
				}
				events = append(events, "renew")
				return controllerwire.RenewalResponse{Watched: true, WatchedJobs: watched}, nil
			}}
			host := runners.Hosting{
				ControllerURL: "https://controller.example",
				Lease:         time.Minute, Interval: 10 * time.Second,
				NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
					bearer, err := options.Bearer(t.Context())
					if err != nil || bearer != unit.Capability || options.URL != "https://controller.example" || options.Launcher {
						t.Fatal("host client did not receive the unit's private controller binding")
					}
					events = append(events, "connect")
					return bound, nil
				},
				Observe: func(got controllerwire.ClaimedUnit, beat lease.Beat) {
					if !reflect.DeepEqual(got, unit) || !beat.Watched || !reflect.DeepEqual(beat.WatchedNodes, []string{"watched-job"}) {
						t.Fatal("renewal observation lost its award or watch projection")
					}
					beat.WatchedNodes[0] = "observer mutation"
				},
				StartKeeper: func(ctx context.Context, keeper lease.Keeper) (context.Context, func()) {
					if _, err := keeper.Renew(ctx); err != nil {
						t.Fatal(err)
					}
					return ctx, func() { events = append(events, "join") }
				},
			}
			prepare := func(got controllerwire.ClaimedUnit, controller controllerwire.Controller) {
				if !reflect.DeepEqual(got, unit) || controller != bound {
					t.Fatal("host preparation received another controller or award")
				}
				events = append(events, "prepare")
			}
			cleanup := func() error { events = append(events, "cleanup"); return nil }
			host.PrepareRun = func(_ context.Context, got controllerwire.ClaimedUnit, controller controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
				prepare(got, controller)
				return runnerRunW8(func() { events = append(events, "execute") }), nodehost.RunInput{}, cleanup, nil
			}
			host.PrepareNode = func(_ context.Context, got controllerwire.ClaimedUnit, controller controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				prepare(got, controller)
				return runnerDeadlineNodeW8(func(context.Context) { events = append(events, "execute") }), runnerIdentityInputW8(got), cleanup, nil
			}
			if err := host.Host(t.Context(), unit); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(watched, []string{"watched-job"}) {
				t.Fatal("observer mutated controller-owned watch data")
			}
			if !reflect.DeepEqual(events, []string{"connect", "renew", "renew", "prepare", "execute", "join", "cleanup"}) {
				t.Fatalf("hosting sequence: %v", events)
			}
		})
	}
}

type runnerRunW8 func()

func (run runnerRunW8) Run(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error) {
	run()
	return nodehost.RunCompletion{}, nil
}

func TestRunnerMixedClaimsKeepControllerBindingsSeparateW8(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		awards := []controllerwire.ClaimedUnit{runnerAuthorityAwardW8(capability.UnitTrigger), runnerAuthorityAwardW8(capability.UnitNode)}
		awards[0].Capability, awards[1].Capability = "trigger-award", "job-award"
		awards[0].UnitID, awards[1].UnitID = "trigger-unit", "job-unit"
		opened := make(chan client.EngineOptions, 2)
		release := make(chan struct{})
		defer close(release)
		clients := make(map[string]*runnerControllerW8)
		for _, award := range awards {
			clients[award.Capability] = &runnerControllerW8{renew: func(_ context.Context, id string, _ controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
				if id != award.UnitID {
					t.Error("concurrent units shared their controller binding")
				}
				return controllerwire.RenewalResponse{}, nil
			}}
		}
		claimed := 0
		root := &runnerControllerW8{claim: func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			if !slices.Equal(request.Kinds, []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}) {
				t.Error("runner split the unified queue selection")
			}
			award := awards[claimed]
			claimed++
			return controllerwire.ClaimResponse{Claim: &award}, nil
		}}
		prepare := func(unit controllerwire.ClaimedUnit, controller controllerwire.Controller) {
			if controller != clients[unit.Capability] {
				t.Error("preparation received the root client or another unit's client")
			}
		}
		host := runners.Hosting{
			ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
			NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
				opened <- options
				<-release
				bearer, err := options.Bearer(t.Context())
				return clients[bearer], err
			},
			StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) { return ctx, func() {} },
			PrepareRun: func(_ context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
				prepare(unit, controller)
				return runnerRunW8(func() {}), nodehost.RunInput{}, nil, nil
			},
			PrepareNode: func(_ context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				prepare(unit, controller)
				return runnerDeadlineNodeW8(func(context.Context) {}), runnerIdentityInputW8(unit), nil, nil
			},
		}
		loop := runners.Loop{
			Config: runners.LoopConfig{Version: "v0.20.0", MaxConcurrent: 2, MaxClaims: 2}, Controller: root, Host: host,
			CheckVersion: func(context.Context, string) error { return nil },
		}
		done := make(chan error, 1)
		go func() { done <- loop.Run(t.Context()) }()
		synctest.Wait()
		if len(opened) != 2 {
			t.Fatalf("opened %d controllers, want two concurrent awards", len(opened))
		}
		first, second := <-opened, <-opened
		firstBearer, firstErr := first.Bearer(t.Context())
		secondBearer, secondErr := second.Bearer(t.Context())
		if firstErr != nil || secondErr != nil || firstBearer == secondBearer || clients[firstBearer] == nil || clients[secondBearer] == nil {
			t.Error("concurrent controllers did not retain their separate award bearers")
		}
		release <- struct{}{}
		release <- struct{}{}
		if err := <-done; err != nil || claimed != 2 {
			t.Fatalf("mixed runner = %v, claims = %d", err, claimed)
		}
	})
}

func FuzzRunnerControllerBindingW8(f *testing.F) {
	f.Add("private-award", "unit", false)
	f.Add("another-award", "other-unit", true)
	f.Fuzz(func(t *testing.T, bearer, id string, cancelBearer bool) {
		if bearer == "" || id == "" {
			return
		}
		unit := runnerAuthorityAwardW8(capability.UnitNode)
		unit.Capability, unit.UnitID = bearer, id
		prepared := errors.New("preparation reached")
		host := runners.Hosting{
			ControllerURL: "https://controller.example",
			Lease:         time.Minute, Interval: 10 * time.Second,
			NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
				ctx, cancel := context.WithCancel(t.Context())
				if cancelBearer {
					cancel()
				}
				defer cancel()
				got, err := options.Bearer(ctx)
				if cancelBearer {
					if !errors.Is(err, context.Canceled) || got != "" {
						t.Fatal("cancelled credential lookup exposed authority")
					}
				} else if err != nil || got != bearer {
					t.Fatal("controller received another award's authority")
				}
				return &runnerControllerW8{renew: func(_ context.Context, gotID string, _ controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
					if gotID != id {
						t.Fatal("controller renewed another unit")
					}
					return controllerwire.RenewalResponse{}, nil
				}}, nil
			},
			StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) { return ctx, func() {} },
			PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				return nil, nodehost.NodeInput{}, nil, prepared
			},
		}
		if err := host.Host(t.Context(), unit); !errors.Is(err, prepared) {
			t.Fatal(err)
		}
	})
}
