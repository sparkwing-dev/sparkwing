package store

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLocalAndHereClaimsDoNotInventAnExecutionDeadline(t *testing.T) {
	for _, placement := range []string{"local", "here"} {
		t.Run(placement, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET expires_at=NULL,created_at=?`, s.now().UnixNano())
			root := authorityRoot("local-owner", "a")
			root.Subject.Kind = capability.SubjectLocal
			root.Grants = []capability.Grant{capability.TriggersClaim, capability.NodesClaim, capability.RunsWrite}
			authorityPersist(t, s, root)
			demand.CapabilityPrefix = root.Prefix
			demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
			if placement == "here" {
				var here capability.Issued
				err := s.inTx(t.Context(), func(tx *storeTx) error {
					var err error
					here, err = s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Unit: capability.UnitTrigger, UnitID: "run", HereRunID: "run", Purpose: capability.PurposeHere, Delivery: capability.DeliveryDirect}})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				demand.CapabilityPrefix = here.Capability.Prefix
			}
			claim, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			principal, err := s.LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
			if err != nil || principal.Capability.ExpiresAt != nil || principal.Capability.Binding.Deadline != nil {
				t.Fatalf("invented owner deadline: %+v, %v", principal.Capability.Binding.Deadline, err)
			}
			if principal.Capability.Billing == nil || principal.Capability.Billing.ExecutorClass != placement {
				t.Fatalf("placement identity: %+v", principal.Capability.Billing)
			}
			before := s.now()
			s.clock = func() time.Time { return before.Add(time.Second) }
			beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
			if err != nil || !beat.ExpiresAt.After(claim.Award.LeaseExpiresAt) {
				t.Fatalf("local lease cannot renew: %+v, %v", beat, err)
			}
		})
	}
}
