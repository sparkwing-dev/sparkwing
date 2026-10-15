package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestCommittingAccountOriginProperty(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	s.SetProvisionPolicy(provisionPolicyFunc(func(_ context.Context, _ capability.AuthorityResolver, _ capability.ProvisionSpec) error { return nil }))
	registration := capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner:a:runner", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}}
	machine, err := s.ProvisionCapability(t.Context(), registration)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	random := rand.New(rand.NewPCG(902, 73))
	for i := range 64 {
		root := authorityRoot(fmt.Sprintf("account-root-%d-%x", i, random.Uint64()), "a")
		root.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}
		root.Grants = []capability.Grant{capability.RunsRead}
		authorityPersist(t, s, root)
		hash := fmt.Sprintf("derived-%d", i)
		rewriteSchemaExec(t, s.db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id,capability_prefix) SELECT ?,principal,scopes,created_at,expires_at,team,account_id,? FROM sessions WHERE hash=?`, hash, root.Prefix, owner.Authority.ID)
		derived := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hash}
		spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &derived, Subject: root.Subject, Team: "a", Grants: root.Grants, Binding: root.Binding}
		issued, err := s.ProvisionCapability(t.Context(), spec)
		if err == nil || issued.Raw != "" {
			t.Fatal("capability-derived session minted an independent member root")
		}
		enabled := random.IntN(2) == 0
		if err := tenant.SetSourceMachineConsent(t.Context(), owner.Authority, machine.Capability.Prefix, !enabled); err != nil {
			t.Fatal(err)
		}
		if err := tenant.SetSourceMachineConsent(t.Context(), derived, machine.Capability.Prefix, enabled); err == nil {
			t.Fatal("capability-derived session changed owner consent")
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
