package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestCommittingAccountOriginProperty(t *testing.T) {
	s := authorityStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSessionKeyCipher(cipher)
	if _, err := s.csrfSigningKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	owner := authorityMember(t, s, "owner", RoleOwner)
	s.SetProvisionPolicy(provisionPolicyFunc(func(_ context.Context, _ capability.AuthorityResolver, _ capability.ProvisionSpec) error { return nil }))
	registration := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}}
	machine, err := s.ProvisionCapability(t.Context(), registration)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	random := rand.New(rand.NewPCG(902, 73))
	for i := range 64 {
		root := authorityRoot(fmt.Sprintf("account-root-%d-%x", i, random.Uint64()), "a")
		root.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}
		root.Grants = []capability.Grant{capability.RunsRead, capability.TeamAdmin}
		authorityPersist(t, s, root)
		raw, _, err := s.CreateCapabilitySession(t.Context(), root.Prefix, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		derived := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)}
		var failure *apierr.Error
		for _, origin := range []capability.AuthorityRef{derived, {Kind: capability.AuthorityCapability, ID: root.Prefix}} {
			registration.Authority = &origin
			issued, err := s.ProvisionCapability(t.Context(), registration)
			if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || issued.Raw != "" {
				t.Fatalf("capability-derived %s machine provisioning: issued=%v err=%v", origin.Kind, issued.Raw != "", err)
			}
		}
		spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &derived, Subject: root.Subject, Team: "a", Grants: root.Grants, Binding: root.Binding}
		issued, err := s.ProvisionCapability(t.Context(), spec)
		if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || issued.Raw != "" {
			t.Fatalf("capability-derived session member provisioning: issued=%v err=%v", issued.Raw != "", err)
		}
		enabled := random.IntN(2) == 0
		if err := tenant.SetSourceMachineConsent(t.Context(), owner.Authority, machine.Capability.Prefix, !enabled); err != nil {
			t.Fatal(err)
		}
		if err := tenant.SetSourceMachineConsent(t.Context(), derived, machine.Capability.Prefix, enabled); !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
			t.Fatalf("capability-derived session owner consent: %v", err)
		}
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_machines WHERE capability_prefix=?`, machine.Capability.Prefix).Scan(&count); err != nil || (count == 1) == enabled {
			t.Fatalf("refused consent changed persisted state: %d, %v", count, err)
		}
		spec.Authority = &owner.Authority
		if issued, err := s.ProvisionCapability(t.Context(), spec); err != nil || issued.Raw == "" {
			t.Fatalf("direct session could not provision: %v", err)
		}
	}
}

func TestMachineProvisionGrantsProperty(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	deployment := authorityRoot("deployment", "")
	deployment.Subject.Kind = capability.SubjectService
	deployment.Grants = []capability.Grant{capability.OperatorTokens}
	authorityPersist(t, s, deployment)
	s.SetProvisionPolicy(provisionPolicyFunc(func(context.Context, capability.AuthorityResolver, capability.ProvisionSpec) error { return nil }))
	random := rand.New(rand.NewPCG(453, 28))
	for _, origin := range []capability.AuthorityRef{owner.Authority, {Kind: capability.AuthorityCapability, ID: deployment.Prefix}} {
		for _, grant := range capability.Grants {
			grants := []capability.Grant{grant}
			if random.IntN(2) == 0 && grant != capability.NodesClaim {
				grants = append(grants, capability.NodesClaim)
			}
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &origin, RegistrationID: "runner", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}}
			issued, err := s.ProvisionCapability(t.Context(), spec)
			if grant == capability.NodesClaim || grant == capability.TriggersClaim {
				if err != nil || issued.Raw == "" {
					t.Fatalf("%s origin with %s: %v", origin.Kind, grant, err)
				}
			} else {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || issued.Raw != "" {
					t.Fatalf("%s origin issued machine grant %s: %v", origin.Kind, grant, err)
				}
			}
		}
	}
}
