package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOwnerFinishRaceRejectsStaleAwardProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(439, 811))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET generation=?`, random.Int64N(1000))
			demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
			first, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ReleaseUnit(t.Context(), first, first.Award.CapabilityPrefix); err != nil {
				t.Fatal(err)
			}
			second, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || second.Award.Generation != first.Award.Generation+1 {
				t.Fatalf("successor: %+v, %v", second, err)
			}
			tenant := &Tenant{s: s, team: first.Team}
			finish := func(claim UnitClaim, state RunState) (Applied, error) {
				actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
				return tenant.FinishRunAndUnit(t.Context(), FinishRunRequest{Unit: claim.Owner, Transition: RunCompletion{RunID: claim.RunID, Cause: RunFinish, Actor: actor}})
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Go(func() {
				<-start
				if _, err := finish(first, RunFailed); err == nil {
					t.Error("stale owner completed the successor's run")
				}
			})
			wg.Go(func() {
				<-start
				if _, err := finish(second, RunSuccess); err != nil {
					t.Error(err)
				}
			})
			close(start)
			wg.Wait()
			before, err := tenant.ReadSnapshot(t.Context(), first.RunID)
			if err != nil || before.Run.State != RunSuccess || before.Run.FinishedAt == nil {
				t.Fatalf("successor verdict: %+v, %v", before.Run, err)
			}
			if _, err := finish(first, RunSuccess); err == nil {
				t.Fatal("stale owner replayed the successor's verdict")
			}
			if err := s.ReleaseUnit(t.Context(), first, first.Award.CapabilityPrefix); err == nil {
				t.Fatal("stale owner released the completed unit")
			}
			after, err := tenant.ReadSnapshot(t.Context(), first.RunID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("stale report changed completed state: %v", err)
			}
			rewriteSchemaCount(t, s.db, "billing_awards", 2)
			rewriteSchemaCount(t, s.db, "credit_charges", 0)
		})
	}
}
