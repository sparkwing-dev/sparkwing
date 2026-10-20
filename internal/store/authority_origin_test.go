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
	s.csrfCipher, s.csrfKey = cipher, nil
	if _, err := s.AsOperator().csrfSigningKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	owner := authorityMember(t, s, "owner", RoleOwner)
	s.provisions = provisionPolicyFunc(func(_ context.Context, _ capability.AuthorityResolver, _ capability.ProvisionSpec) error { return nil })
	registration := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner:a:runner", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}}
	_, err = s.AsOperator().ProvisionCapability(t.Context(), registration)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(902, 73))
	for i := range 64 {
		root := authorityRoot(fmt.Sprintf("account-root-%d-%x", i, random.Uint64()), "a")
		root.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}
		root.Grants = []capability.Grant{capability.RunsRead, capability.TeamAdmin}
		authorityPersist(t, s, root)
		raw, _, err := s.AsOperator().CreateCapabilitySession(t.Context(), root.Prefix, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		derived := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)}
		var failure *apierr.Error
		for _, origin := range []capability.AuthorityRef{derived, {Kind: capability.AuthorityCapability, ID: root.Prefix}} {
			registration.Authority = &origin
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), registration)
			if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || issued.Raw != "" {
				t.Fatalf("capability-derived %s machine provisioning: issued=%v err=%v", origin.Kind, issued.Raw != "", err)
			}
		}
		spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &derived, Subject: root.Subject, Team: "a", Grants: root.Grants, Binding: root.Binding}
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
		if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || issued.Raw != "" {
			t.Fatalf("capability-derived session member provisioning: issued=%v err=%v", issued.Raw != "", err)
		}
		spec.Authority = &owner.Authority
		if issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec); err != nil || issued.Raw == "" {
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
	s.provisions = provisionPolicyFunc(func(context.Context, capability.AuthorityResolver, capability.ProvisionSpec) error { return nil })
	random := rand.New(rand.NewPCG(453, 28))
	for originIndex, origin := range []capability.AuthorityRef{owner.Authority, {Kind: capability.AuthorityCapability, ID: deployment.Prefix}} {
		for grantIndex, grant := range capability.Grants() {
			grants := []capability.Grant{grant}
			if random.IntN(2) == 0 && grant != capability.NodesClaim {
				grants = append(grants, capability.NodesClaim)
			}
			name := fmt.Sprintf("runner-%d-%d", originIndex, grantIndex)
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &origin, RegistrationID: "runner:a:" + name, Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Repositories: []string{"github.com/acme/*"}, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}}
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
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
