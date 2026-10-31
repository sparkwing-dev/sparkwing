package store

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestCloudClaimMetersAwardFromUnmeteredLauncher(t *testing.T) {
	s, spec := provisionOrigin(t, capability.ProvisionService)
	s, demand := unitClaimFixtureStore(t, s)
	spec.Metered = false
	root, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if root.Capability.RootMetered {
		t.Fatal("launcher fixture is metered")
	}
	fundUnitClaim(t, s)
	demand.CapabilityPrefix = root.Capability.Prefix
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	award, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	c := award.Capability
	if c.Billing == nil || !c.Billing.Metered || c.Billing.ExecutorClass != "cloud" || !c.RootMetered {
		t.Fatalf("Cloud award from unmetered launcher: root_metered=%t, billing=%+v", c.RootMetered, c.Billing)
	}
}

func TestPlacementMeteringUsesEnrolledOriginProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 3.3s measured in the short class; the full suite runs it")
	}
	for i := range 64 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cloud, metered, queued, delegated, funded := i&1 != 0, i&2 != 0, i&4 != 0, i&8 != 0, i&16 != 0
			exchanged := i&32 != 0
			kind := capability.ProvisionMachine
			if cloud {
				kind = capability.ProvisionService
			}
			s, spec := provisionOrigin(t, kind)
			s, demand := unitClaimFixtureStore(t, s)
			proveClaimFixtureRepository(t, s)
			operator := authorityRoot("enroller", "")
			operator.Subject.Kind = capability.SubjectOperator
			operator.Grants = []capability.Grant{capability.OperatorTokens}
			authorityPersist(t, s, operator)
			ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: operator.Prefix}
			spec.Authority, spec.Metered = &ref, metered
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if delegated {
				issued, err = s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, Audience: capability.Controller, Grants: issued.Capability.Grants, Binding: issued.Capability.Binding, Repositories: issued.Capability.Repositories, ExpiresAt: issued.Capability.ExpiresAt})
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
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
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
			if cloud && exchanged {
				issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				claim.Award.CapabilityPrefix = issued.Capability.Prefix
			}
			p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
			if err != nil || p.Capability.Billing == nil || p.Capability.Billing.Metered != wantMetered || p.Capability.RootMetered != wantMetered || cloud && p.Capability.Billing.ExecutorClass != "cloud" {
				t.Fatalf("placement root_metered=%t, billing=%+v, %v; want metered=%t", p.Capability.RootMetered, p.Capability.Billing, err, wantMetered)
			}
			var persisted bool
			if err := s.db.QueryRow("SELECT metered FROM billing_awards WHERE reservation_id=?", p.Capability.Billing.ReservationID).Scan(&persisted); err != nil || persisted != wantMetered {
				t.Fatalf("persisted metering=%t, %v; want %t", persisted, err, wantMetered)
			}
		})
	}
}
