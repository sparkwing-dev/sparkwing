package store

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPlacementMeteringUsesEnrolledOriginProperty(t *testing.T) {
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cloud, metered, queued, delegated, funded := i&1 != 0, i&2 != 0, i&4 != 0, i&8 != 0, i&16 != 0
			kind := capability.ProvisionMachine
			if cloud {
				kind = capability.ProvisionService
			}
			s, spec := provisionOrigin(t, kind)
			s, demand := unitClaimFixtureStore(t, s)
			operator := authorityRoot("enroller", "")
			operator.Subject.Kind = capability.SubjectOperator
			operator.Grants = []capability.Grant{capability.OperatorTokens}
			authorityPersist(t, s, operator)
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Prefix}
			spec.Authority, spec.Metered = &ref, metered
			issued, err := s.ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if delegated {
				issued, err = s.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, Audience: capability.Controller, Grants: issued.Capability.Grants, Binding: issued.Capability.Binding, Repositories: issued.Capability.Repositories, ExpiresAt: issued.Capability.ExpiresAt})
				if err != nil {
					t.Fatal(err)
				}
			}
			demand.CapabilityPrefix = issued.Capability.Prefix
			if queued {
				demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
			}
			if funded {
				fundUnitClaim(t, s)
			}
			claim, err := s.ClaimUnit(t.Context(), demand)
			wantMetered := cloud || metered
			if wantMetered && !funded {
				if err == nil || len(claim.Bearer) != 0 {
					t.Fatal("unfunded metered placement received an award")
				}
				for _, table := range []string{"billing_awards", "billable_intervals", "credit_charges", "events"} {
					rewriteSchemaCount(t, s.db, table, 0)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
			if err != nil || p.Capability.Billing == nil || p.Capability.Billing.Metered != wantMetered {
				t.Fatalf("placement billing=%+v, %v; want metered=%t", p.Capability.Billing, err, wantMetered)
			}
			var persisted bool
			if err := s.db.QueryRow("SELECT metered FROM billing_awards WHERE reservation_id=?", p.Capability.Billing.ReservationID).Scan(&persisted); err != nil || persisted != wantMetered {
				t.Fatalf("persisted metering=%t, %v; want %t", persisted, err, wantMetered)
			}
		})
	}
}
