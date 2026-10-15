package main

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
