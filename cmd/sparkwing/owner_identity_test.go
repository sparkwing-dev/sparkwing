package main

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestOwnerBindingsCannotMutateGrantedRunnerProperty(t *testing.T) {
	property := func(name, kind string, labels []string) bool {
		for _, here := range []bool{false, true} {
			award := localTestAward(t)
			if here {
				award = hereAward(t)
			}
			award.Runner = nodeproto.RunnerIdentity{Name: name, Type: kind, Labels: slices.Clone(labels)}
			want := award.Runner
			want.Labels = slices.Clone(labels)
			valid := false
			bind := func(_ context.Context, received controllerwire.ClaimedUnit) (hereHostSession, error) {
				for i := range received.Runner.Labels {
					received.Runner.Labels[i] = "binding-selected-label"
				}
				return hereHostSession{
					input: nodehost.RunInput{GrantedRunner: nodeproto.RunnerIdentity{Name: "binding-selected-runner", Type: "cloud"}},
					close: func() error { return nil },
					host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						valid = reflect.DeepEqual(input.GrantedRunner, want)
						return nodehost.RunCompletion{Outcome: "success"}, nil
					}),
				}, nil
			}
			var err error
			if here {
				_, err = runHereAward(t.Context(), client.EngineOptions{}, award.RunID, award,
					func(ctx context.Context, _ client.EngineOptions, award controllerwire.ClaimedUnit) (hereHostSession, error) {
						return bind(ctx, award)
					})
			} else {
				runtime := localRunRuntime{bind: bind, controller: localTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					return controllerwire.ClaimResponse{Claim: &award}, nil
				}}}
				_, err = hostLocalRun(t.Context(), runtime, award.RunID, award.Request.Hash, "v1.2.3")
			}
			if err != nil || !valid {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerBindingsCannotReplaceOwnerFenceProperty(t *testing.T) {
	property := func(holder, unit string, generation uint16) bool {
		for _, here := range []bool{false, true} {
			award := localTestAward(t)
			if here {
				award = hereAward(t)
			}
			award.Binding.Holder = holder + "-holder"
			award.Binding.Generation = int64(generation) + 1
			award.UnitID = unit + "-unit"
			want := runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(award.RunID), Holder: award.Binding.Holder, Generation: runspec.ClaimGeneration(award.Binding.Generation)}}
			authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "resolved-prefix"}
			valid := false
			bind := func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error) {
				return hereHostSession{
					input: nodehost.RunInput{Owner: runstate.Actor{Kind: runstate.ActorController, Authority: authority, CapabilityPrefix: authority.ID, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController, Name: "foreign-unit", EvalFence: runspec.EvalFence{RunID: "foreign-run", Holder: "foreign-holder", Generation: -1}}}},
					close: func() error { return nil },
					host: hereTestHost(func(_ context.Context, input nodehost.RunInput) (nodehost.RunCompletion, error) {
						valid = input.Owner.Kind == runstate.ActorRunHost && input.Owner.Owner == want && input.Owner.Authority == authority && input.Owner.CapabilityPrefix == authority.ID
						return nodehost.RunCompletion{Outcome: "success"}, nil
					}),
				}, nil
			}
			var err error
			if here {
				_, err = runHereAward(t.Context(), client.EngineOptions{}, award.RunID, award, func(ctx context.Context, _ client.EngineOptions, received controllerwire.ClaimedUnit) (hereHostSession, error) {
					return bind(ctx, received)
				})
			} else {
				runtime := localRunRuntime{bind: bind, controller: localTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					return controllerwire.ClaimResponse{Claim: &award}, nil
				}}}
				_, err = hostLocalRun(t.Context(), runtime, award.RunID, award.Request.Hash, "v1.2.3")
			}
			if err != nil || !valid {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
