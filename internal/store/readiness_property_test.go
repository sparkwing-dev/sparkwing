package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestReadinessWithdrawalPreservesHolderAndAuthorityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(71, 109))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand, owner, now := renewalFixture(t, capability.UnitTrigger)
			tenant := &Tenant{s: s, team: owner.Team}
			ref := controllerwire.NodeRef{RunID: owner.RunID, NodeID: "job"}
			actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Award.CapabilityPrefix}, CapabilityPrefix: owner.Award.CapabilityPrefix, Owner: owner.Owner}
			change := NodeTransition{Ref: ref, Actor: actor, To: NodePending, Cause: CauseRevoke}
			for range random.IntN(4) + 1 {
				applied, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || applied.Replayed || applied.Attempt != 0 || applied.Generation != 0 {
					t.Fatalf("withdrawal: %+v, %v", applied, err)
				}
				replay, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || !replay.Replayed || replay.StateSeq != applied.StateSeq {
					t.Fatalf("withdrawal replay: %+v, %v", replay, err)
				}
				demand.Target = &UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}
				if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
					t.Fatal("withdrawn job remained claimable")
				}
				if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: actor, To: NodeReady, Cause: CauseRelease}); err != nil {
					t.Fatal(err)
				}
				*now = now.Add(time.Duration(random.IntN(100)+1) * time.Millisecond)
			}
			bad := change
			switch i % 8 {
			case 0:
				bad.Actor.Owner.Generation += int64(random.IntN(100) + 1)
			case 1:
				bad.Actor.Owner.Holder = fmt.Sprintf("other-%d", random.Uint64())
			case 2:
				bad.Actor.CapabilityPrefix = demand.CapabilityPrefix
				bad.Actor.Authority.ID = demand.CapabilityPrefix
			case 3:
				*now = owner.Award.LeaseExpiresAt
			case 4, 5, 6:
				claim, err := s.ClaimUnit(t.Context(), demand)
				if err != nil {
					t.Fatal(err)
				}
				if i%8 >= 5 {
					holder := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
					start, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: holder, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)})
					if err != nil {
						t.Fatal(err)
					}
					if i%8 == 6 {
						if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: holder, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(start.Attempt)}); err != nil {
							t.Fatal(err)
						}
					}
				}
			case 7:
				tenant = &Tenant{s: s, team: "b"}
			}
			actual := &Tenant{s: s, team: owner.Team}
			before, err := actual.ReadSnapshot(t.Context(), ref.RunID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := actual.ReadStateEvents(t.Context(), ref.RunID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TransitionNode(t.Context(), bad); err == nil {
				t.Fatal("invalid owner withdrew work")
			}
			after, err := actual.ReadSnapshot(t.Context(), ref.RunID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refused withdrawal changed state: %v", err)
			}
			afterEvents, err := actual.ReadStateEvents(t.Context(), ref.RunID, 0, 100)
			if err != nil || !reflect.DeepEqual(events, afterEvents) {
				t.Fatalf("refused withdrawal changed events: %v", err)
			}
		})
	}
}
