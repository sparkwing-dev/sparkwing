package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRepositoryClaimScopeProperty(t *testing.T) {
	for i := range 20 {
		for _, outside := range []bool{false, true} {
			for _, scope := range [][]string{nil, {}, {"github.com/acme/*"}, {"gh:1"}} {
				t.Run(fmt.Sprint(i, scope, outside), func(t *testing.T) {
					s, demand := unitClaimFixture(t)
					root := authorityRoot("restricted", "a")
					root.Repositories = scope
					authorityPersist(t, s, root)
					child := authorityRoot("descendant", "a")
					child.Repositories = nil
					child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
					authorityPersist(t, s, child)
					demand.CapabilityPrefix = child.Prefix
					display := fmt.Sprintf("github.com/acme/project-%d", i)
					if outside {
						display = fmt.Sprintf("github.com/acme-evil/project-%d", i)
					}
					rewriteSchemaExec(t, s.db, "UPDATE runs SET repo_display=? WHERE id='run'", display)
					_, err := s.AsOperator().ClaimUnit(t.Context(), demand)
					if scope != nil && (len(scope) == 0 || outside && scope[0] == "github.com/acme/*") {
						if err == nil {
							t.Fatal("empty ancestor scope authorized work")
						}
					} else if err != nil {
						t.Fatalf("authorized repository claim: %v", err)
					}
				})
			}
		}
	}
}

func TestRepositoryDelegationCannotDropRestriction(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("restricted", "a")
	root.Repositories = []string{"gh:1"}
	authorityPersist(t, s, root)
	_, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Audience: capability.Controller, Grants: root.Grants, Binding: root.Binding})
	if err == nil {
		t.Fatal("delegated unrestricted scope from restricted parent")
	}
}

func TestRepositoryAwardRechecksAncestry(t *testing.T) {
	s, _ := unitClaimFixture(t)
	root := authorityRoot("restricted", "a")
	root.Grants = []capability.Grant{capability.TriggersClaim}
	root.Repositories = []string{"gh:other"}
	authorityPersist(t, s, root)
	rewriteSchemaExec(t, s.db, `UPDATE triggers SET claimed_by='restricted',generation=1,lease_expires_at=?`, s.now().Add(time.Hour).UnixNano())
	err := s.inTx(t.Context(), func(tx *storeTx) error {
		_, err := s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitTrigger, UnitID: "run", Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect}})
		return err
	})
	if err == nil {
		t.Fatal("direct award escaped repository scope")
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 0)
}
