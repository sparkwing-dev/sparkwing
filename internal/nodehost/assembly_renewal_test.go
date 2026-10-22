package nodehost

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func FuzzLocalOwnerRenewKeepsAuthenticatedFence(f *testing.F) {
	for field := uint8(0); field < 9; field++ {
		f.Add(field, "foreign", int64(1), false)
	}
	f.Fuzz(func(t *testing.T, field uint8, value string, delta int64, computing bool) {
		principal := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "award"}, Capability: capability.Capability{
			Prefix: "award", Unit: capability.UnitTrigger, Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "holder", Generation: 2},
		}}
		actor := store.Actor{Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: "award", Owner: store.OwnerLeaseRef{Kind: store.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "run", Holder: "holder", Generation: 2}}}
		field %= 9
		switch field {
		case 1:
			actor.Kind = store.ActorHolder
		case 2:
			actor.Authority.ID += value + "!"
		case 3:
			actor.CapabilityPrefix += value + "!"
		case 4:
			actor.Owner.RunID += runspec.RunID(value + "!")
		case 5:
			actor.Owner.Holder += value + "!"
		case 6:
			actor.Owner.Generation ^= runspec.ClaimGeneration(delta | 1)
		case 7:
			actor.Owner.Kind = store.OwnerController
		case 8:
			principal.Capability.Unit = capability.UnitNode
		}
		called := false
		renew := localOwnerRenew(func(context.Context) (capability.Principal, error) { return principal, nil }, func(_ context.Context, prefix string, activity bool) (store.LeaseBeat, error) {
			called = true
			if prefix != "award" || activity != computing {
				t.Fatal("renewal changed the authenticated prefix or compute report")
			}
			return store.LeaseBeat{CancelRequested: true, CancelReason: "drain"}, nil
		})
		beat, err := renew(t.Context(), actor, computing)
		if field == 0 {
			if err != nil || !called || !beat.CancelRequested || beat.CancelReason != "drain" {
				t.Fatalf("valid renewal lost its result: %+v %v", beat, err)
			}
		} else {
			var denied *apierr.Error
			if called || !errors.As(err, &denied) || denied.Code != apierr.LockHeld {
				t.Fatalf("changed owner renewed: %v", err)
			}
		}
	})
}

func TestLocalOwnerRenewReturnsAuthenticationFailure(t *testing.T) {
	lost := errors.New("owner no longer live")
	renew := localOwnerRenew(func(context.Context) (capability.Principal, error) { return capability.Principal{}, lost }, func(context.Context, string, bool) (store.LeaseBeat, error) {
		t.Fatal("lost authority renewed")
		return store.LeaseBeat{}, nil
	})
	if _, err := renew(t.Context(), store.Actor{}, false); !errors.Is(err, lost) {
		t.Fatal(err)
	}
}
