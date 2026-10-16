package acceptance_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"

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
			reached := errors.New("native binding reached")
			var events []string
			bound := &runnerControllerW8{renew: func(_ context.Context, id string, request controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
				if id != unit.UnitID || request.Executing {
					t.Fatal("confirmation changed its unit or execution state")
				}
				events = append(events, "renew")
				return controllerwire.RenewalResponse{}, nil
			}}
			host := runners.Hosting{ControllerURL: "https://controller.example", NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
				bearer, err := options.Bearer(t.Context())
				if err != nil || bearer != unit.Capability || options.URL != "https://controller.example" || options.Launcher {
					t.Fatal("wrong award transport")
				}
				events = append(events, "connect")
				return bound, nil
			}, Bind: func(_ context.Context, got controllerwire.ClaimedUnit, controller controllerwire.Controller) (runners.NativeServices, capability.Principal, error) {
				if !reflect.DeepEqual(got, unit) || controller != bound {
					t.Fatal("native binder received another award or controller")
				}
				events = append(events, "bind")
				return runners.NativeServices{}, capability.Principal{}, reached
			}}
			if err := host.Host(t.Context(), unit); !errors.Is(err, reached) {
				t.Fatal(err)
			}
			if !slices.Equal(events, []string{"connect", "renew", "bind"}) {
				t.Fatal(events)
			}
		})
	}
}

func TestRunnerPlanAndEvalUseTheirAwardedUnitAuthority(t *testing.T) {
	plan := runnerAuthorityAwardW8(capability.UnitTrigger)
	plan.Unit, plan.Purpose, plan.Binding.Kind = capability.UnitPlan, capability.PurposePlan, capability.BindingRun
	eval := runnerAuthorityAwardW8(capability.UnitNode)
	eval.Unit, eval.Purpose, eval.SourceGeneration = capability.UnitEval, capability.PurposeEval, eval.Binding.Generation
	eval.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: eval.UnitID, Holder: "evaluation", Generation: 7}
	memoized := eval
	memoized.Binding.Generation, memoized.SourceGeneration = 0, 0
	for _, unit := range []controllerwire.ClaimedUnit{plan, eval, memoized} {
		t.Run(string(unit.Unit), func(t *testing.T) { assertRunnerAuthorityW8(t, unit, true) })
	}
	eval.Binding.Expansion = nil
	assertRunnerAuthorityW8(t, eval, false)
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
		reached := errors.New("native binding reached")
		host := runners.Hosting{ControllerURL: "https://controller.example", NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
			opened <- options
			<-release
			bearer, err := options.Bearer(t.Context())
			return clients[bearer], err
		}, Bind: func(_ context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (runners.NativeServices, capability.Principal, error) {
			if controller != clients[unit.Capability] {
				t.Error("native binding shared another award's client")
			}
			return runners.NativeServices{}, capability.Principal{}, reached
		}}
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
		if err := <-done; !errors.Is(err, reached) || claimed != 2 {
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
			}, Bind: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (runners.NativeServices, capability.Principal, error) {
				return runners.NativeServices{}, capability.Principal{}, prepared
			},
		}
		if err := host.Host(t.Context(), unit); !errors.Is(err, prepared) {
			t.Fatal(err)
		}
	})
}
