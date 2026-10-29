package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestRestoredCredentialRowsStayRefusedAfterEpochRotation(t *testing.T) {
	s := authorityStore(t)
	s.credentialEpoch = "before-restore"
	root := authorityRoot("backup-token", "a")
	root.CredentialEpoch = s.credentialEpoch
	authorityPersist(t, s, root)
	member := authorityMember(t, s, "backup-session", RoleEditor)
	rewriteSchemaExec(t, s.db, "UPDATE sessions SET credential_epoch=? WHERE hash=?", s.credentialEpoch, member.Authority.ID)
	refs := []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: root.Prefix}, member.Authority}
	for _, ref := range refs {
		if _, err := s.AsOperator().ResolveAuthority(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=1 WHERE prefix=?", root.Prefix)
	rewriteSchemaExec(t, s.db, "UPDATE sessions SET revoked_at=1 WHERE hash=?", member.Authority.ID)
	// Restoring the backup brings back the earlier unrevoked rows.
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=NULL WHERE prefix=?", root.Prefix)
	rewriteSchemaExec(t, s.db, "UPDATE sessions SET revoked_at=NULL WHERE hash=?", member.Authority.ID)
	s.credentialEpoch = "after-restore"
	for _, ref := range refs {
		if _, err := s.AsOperator().ResolveAuthority(t.Context(), ref); err == nil {
			t.Fatalf("restored %s accepted", ref.Kind)
		}
	}
}

func TestEpochChangeReopensBootstrapForFreshSeed(t *testing.T) {
	s := authorityStore(t)
	s.credentialEpoch = "original"
	if err := s.EnsureCredentialEpoch(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := bootstrapSpec([32]byte{1})
	if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), first); err != nil || !created {
		t.Fatalf("first bootstrap: %t %v", created, err)
	}
	s.credentialEpoch = "restored"
	if err := s.EnsureCredentialEpoch(t.Context()); !errors.Is(err, ErrCredentialEpochChanged) {
		t.Fatalf("changed epoch served without a deliberate sign-out: %v", err)
	}
	if err := s.AsOperator().PrepareRestore(t.Context(), "restored"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: first.Seed[:CredentialPrefixLength]}); err == nil {
		t.Fatal("old bootstrap accepted")
	}
	state, err := s.AsOperator().RestoreStatus(t.Context())
	if err != nil || !state.Pending {
		t.Fatalf("schedule hold: %+v %v", state, err)
	}
	fresh := bootstrapSpec([32]byte{2})
	if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), fresh); err != nil || !created {
		t.Fatalf("fresh bootstrap: %t %v", created, err)
	}
}

func TestPeerAuthenticationCreatesRootForCurrentEpoch(t *testing.T) {
	s, err := Open(t.TempDir()+"/state.db", WithCredentialEpoch("original"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lookup := capability.CredentialLookup{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}}
	first, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	s.credentialEpoch = "restored"
	next, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Authorities[0].Principal.Authority == next.Authorities[0].Principal.Authority {
		t.Fatal("reused root from restored database")
	}
	again, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil || again.Authorities[0].Principal.Authority != next.Authorities[0].Principal.Authority {
		t.Fatalf("current epoch root unstable: %+v %v", again, err)
	}
}

func TestNormalRestorePreservesCredentialsAndBootstrapFence(t *testing.T) {
	s := authorityStore(t)
	s.credentialEpoch = "original"
	if err := s.EnsureCredentialEpoch(t.Context()); err != nil {
		t.Fatal(err)
	}
	seed := bootstrapSpec([32]byte{1})
	if created, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), seed); err != nil || !created {
		t.Fatalf("bootstrap: %t %v", created, err)
	}
	member := authorityMember(t, s, "session", RoleOwner)
	rewriteSchemaExec(t, s.db, `UPDATE sessions SET credential_epoch=?`, s.credentialEpoch)
	bootstrap := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: seed.Seed[:CredentialPrefixLength]}
	refs := []capability.AuthorityRef{member.Authority}
	specs := []capability.ProvisionSpec{
		{Kind: capability.ProvisionMember, Authority: &member.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "session"}, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}},
		{Kind: capability.ProvisionMachine, Authority: &member.Authority, Team: "a", RegistrationID: "runner:a:worker", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "worker"}, Repositories: []string{"github.com/acme/*"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}},
		{Kind: capability.ProvisionService, Authority: &bootstrap, RegistrationID: "service:administrator", Subject: capability.Subject{Kind: capability.SubjectService, ID: "administrator"}, Grants: []capability.Grant{capability.OperatorTokens}, Binding: capability.Binding{Kind: capability.BindingNone}},
	}
	for _, spec := range specs {
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
	}
	var before []byte
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, bootstrapUsedKey).Scan(&before); err != nil || len(before) == 0 {
		t.Fatalf("setup did not close bootstrap: %q %v", before, err)
	}
	if err := s.AsOperator().PrepareRestore(t.Context(), s.credentialEpoch); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if _, err := s.AsOperator().ResolveAuthority(t.Context(), ref); err != nil {
			t.Fatalf("normal restore invalidated %s: %v", ref.ID, err)
		}
	}
	var used []byte
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, bootstrapUsedKey).Scan(&used); err != nil || string(used) != string(before) {
		t.Fatalf("normal restore reopened bootstrap: %q %v", used, err)
	}
}
