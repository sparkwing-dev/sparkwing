package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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
					claim, err := st.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: prefix, Kinds: []capability.UnitKind{kind}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
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
