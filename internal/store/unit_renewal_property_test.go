package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestRenewUnitRequiresCurrentAwardProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(172, 936))
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, now := renewalFixture(t, kind)
			table := "triggers"
			if kind == capability.UnitEval {
				table = "eval_units"
			} else if kind == capability.UnitNode {
				table = "nodes"
			}
			initial := *now
			var root []byte
			if err := s.db.QueryRow(`SELECT authority_json FROM capabilities WHERE prefix=?`, demand.CapabilityPrefix).Scan(&root); err != nil {
				t.Fatal(err)
			}
			for iteration := range 96 {
				prefix := claim.Award.CapabilityPrefix
				switch random.IntN(6) {
				case 0:
					prefix = demand.CapabilityPrefix
				case 1:
					prefix = fmt.Sprintf("missing-%x", random.Uint64())
				case 2:
					if err := s.inTx(t.Context(), func(tx *storeTx) error {
						return tx.operator.revokeAuthorityTx(t.Context(), tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
					}); err != nil {
						t.Fatal(err)
					}
				case 3:
					*now = claim.Award.LeaseExpiresAt.AddDate(0, 0, random.IntN(30))
				case 4:
					rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=generation+?`, 1+random.IntN(10000))
				case 5:
					rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET claimed_by=?`, fmt.Sprintf("other-%x", random.Uint64()))
				}
				_, err := s.AsOperator().RenewUnit(t.Context(), prefix, random.IntN(2) == 0)
				var transition *ErrIllegalTransition
				var denied *apierr.Error
				if !(errors.As(err, &transition) && transition.Reason == ReasonHolderRights || errors.As(err, &denied) && denied.Code == apierr.Unauthenticated) {
					t.Fatalf("iteration %d expected an authority refusal: %v", iteration, err)
				}
				var lease int64
				if err := s.db.QueryRow(`SELECT lease_expires_at FROM ` + table).Scan(&lease); err != nil || lease != claim.Award.LeaseExpiresAt.UnixNano() {
					t.Fatalf("refused renewal changed lease: %d, %v", lease, err)
				}
				var started int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE started_at IS NOT NULL`).Scan(&started); err != nil || started != 0 {
					t.Fatalf("refused renewal started execution: %d, %v", started, err)
				}
				rewriteSchemaCount(t, s.db, "node_attempts", 0)
				*now = initial
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=NULL,authority_json=? WHERE prefix=?`, root, demand.CapabilityPrefix)
				rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET generation=?,claimed_by=?`, claim.Award.Generation, claim.Award.Holder)
			}
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
				t.Fatalf("current award refused after generated failures: %v", err)
			}
		})
	}
}
