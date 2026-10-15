package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRenewUnitCannotMutateAStaleJobProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(313, 977))
	for trial := range 48 {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			s, demand, claim, now := renewalFixture(t, capability.UnitNode)
			prefix := claim.Award.CapabilityPrefix
			switch trial % 6 {
			case 0:
				prefix = demand.CapabilityPrefix
			case 1:
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=? WHERE prefix=?`, now.Add(-time.Duration(1+random.IntN(1000000))).UnixNano(), demand.CapabilityPrefix)
			case 2:
				*now = claim.Award.LeaseExpiresAt.Add(time.Duration(random.IntN(2)))
			case 3:
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+?`, 1+random.IntN(1000000))
			case 4:
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET claimed_by=?`, fmt.Sprintf("other-%x", random.Uint64()))
			case 5:
				prefix = fmt.Sprintf("forged-%x", random.Uint64())
			}
			tenant := &Tenant{s: s, team: claim.Team}
			ref := NodeRef{RunID: claim.RunID, NodeID: claim.NodeID}
			before, err := tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			for _, executing := range []bool{false, true} {
				if _, err := s.RenewUnit(t.Context(), prefix, executing); err == nil {
					t.Fatal("stale authority renewed a job")
				}
				after, err := tenant.ReadNode(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("refused renewal changed the job")
				}
			}
			unitBillingTotal(t, s, 0, false)
			rewriteSchemaCount(t, s.db, "billable_intervals", 0)
			rewriteSchemaCount(t, s.db, "node_attempts", 0)
		})
	}
}
