package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresSimultaneousClaimsInsideTheWindowAwardOnlyTheBestTier(t *testing.T) {
	for round := range 3 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			st := storetest.NewPostgres(t).Open(t)
			st.DB().SetMaxOpenConns(24)
			f := st.TestOnlyRankingFixture(t, true)
			second := f.SelfHosted(t, "second")
			f.At(time.Second)
			contenders := []string{f.Self, second, f.Launcher, f.Actions}
			for _, prefix := range contenders {
				f.Poll(t, prefix, capability.UnitPlan, capability.UnitNode)
			}
			start := make(chan struct{})
			awards := make(chan store.UnitClaim, 4*len(contenders))
			var group sync.WaitGroup
			for i := range 4 * len(contenders) {
				prefix := contenders[(i+round)%len(contenders)]
				kind := []capability.UnitKind{capability.UnitPlan, capability.UnitNode}[i/len(contenders)%2]
				group.Go(func() {
					<-start
					claim, err := st.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: prefix, Kinds: []capability.UnitKind{kind}})
					var illegal *store.ErrIllegalTransition
					switch {
					case err == nil:
						awards <- claim
					case errors.Is(err, store.ErrNotFound), errors.As(err, &illegal):
					default:
						t.Error(err)
					}
				})
			}
			close(start)
			group.Wait()
			close(awards)
			won := map[capability.UnitKind]string{}
			for claim := range awards {
				if won[claim.Kind] != "" {
					t.Fatalf("second %s award: %+v after %s", claim.Kind, claim, won[claim.Kind])
				}
				won[claim.Kind] = claim.Award.Holder
			}
			if won[capability.UnitPlan] != "actions" || won[capability.UnitNode] != "actions" {
				t.Fatalf("awards inside the window went to %v; want the Actions tier for every unit", won)
			}
		})
	}
}

func TestPostgresAnAskInFlightHoldsTheLaterAward(t *testing.T) {
	st := storetest.NewPostgres(t).Open(t)
	f := st.TestOnlyRankingFixture(t, true)
	commit := f.OpenAsk(t, f.Self, 3, f.ReadyAt.Add(4900*time.Millisecond))
	f.At(5100 * time.Millisecond)
	f.Poll(t, f.Launcher, capability.UnitNode)
	claimed := make(chan error, 1)
	go func() {
		_, err := st.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Launcher, Kinds: []capability.UnitKind{capability.UnitNode}})
		claimed <- err
	}()
	select {
	case err := <-claimed:
		t.Fatalf("the launcher decided while a better ask was in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	commit()
	if err := <-claimed; !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the launcher took the job a better claimant asked for: %v", err)
	}
}
