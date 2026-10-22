package store

import (
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
	if err := s.EnsureCredentialEpoch(t.Context()); err != nil {
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
