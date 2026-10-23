package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func sessionRuntimeStore(t *testing.T) (*Store, Account, *time.Time) {
	t.Helper()
	s := authorityStore(t)
	now := time.Unix(1700000000, 123456789).UTC()
	s.clock = func() time.Time { return now }
	_ = authorityMember(t, s, "member", RoleEditor)
	return s, Account{ID: "member", Email: "member@example.test"}, &now
}

func TestSessionPersistenceNeverStoresReusableSecretsProperty(t *testing.T) {
	s, account, _ := sessionRuntimeStore(t)
	random := rand.New(rand.NewPCG(831, 172))
	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(random.UintN(256))
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	for range 32 {
		account.Email = fmt.Sprintf("forged-%x@elsewhere.test", random.Uint64())
		raw, csrf, session, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", time.Duration(random.Int64N(int64(time.Hour)))+1)
		if err != nil {
			t.Fatal(err)
		}
		if session.Principal != "member@example.test" || raw == csrf || len(csrf) == 0 {
			t.Fatalf("caller selected session identity: %+v", session)
		}
		digest := sha256.Sum256([]byte(raw))
		var storedHash, envelope string
		var sealed int
		if err := s.db.QueryRow(`SELECT hash FROM sessions WHERE hash=?`, hex.EncodeToString(digest[:])).Scan(&storedHash); err != nil || storedHash == raw {
			t.Fatalf("session presentation persisted: %v", err)
		}
		if err := s.db.QueryRow(`SELECT value,sealed FROM settings WHERE key='session_csrf_key'`).Scan(&envelope, &sealed); err != nil || sealed != 1 || !strings.HasPrefix(envelope, secrets.BoundPrefix) || strings.Contains(envelope, raw) || strings.Contains(envelope, csrf) {
			t.Fatalf("reusable secret persisted in plaintext: sealed=%d, %v", sealed, err)
		}
		other := &Store{db: s.db, dialect: s.dialect, clock: s.clock}
		other.csrfCipher, other.csrfKey = cipher, nil
		read, err := other.AsOperator().LookupSession(t.Context(), raw)
		if err != nil || read.CSRFToken != csrf {
			t.Fatalf("replica csrf differs: %+v, %v", read, err)
		}
		forged := raw[:len(raw)-1] + "!"
		if _, err := other.AsOperator().LookupSession(t.Context(), forged); err == nil {
			t.Fatal("unrecorded presentation authenticated")
		}
		revoked, err := other.AsOperator().DeleteSession(raw)
		if err != nil || revoked == nil || revoked.Digest != storedHash || revoked.AccountID != account.ID || revoked.Team != "a" {
			t.Fatalf("revocation identity: %+v %v", revoked, err)
		}
		if duplicate, err := other.AsOperator().DeleteSession(raw); err != nil || duplicate != nil {
			t.Fatalf("absent session produced revocation: %+v %v", duplicate, err)
		}
		if _, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialSession, Digest: digest, Audience: capability.Controller}, capability.RequestEvidence{}); err == nil {
			t.Fatal("deleted session authenticated through its digest")
		}
	}
}

func TestSessionKeyRefusesMissingWrongOrCorruptCipher(t *testing.T) {
	s, account, _ := sessionRuntimeStore(t)
	if _, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", time.Hour); err == nil {
		t.Fatal("created an unsealed session key")
	}
	key := make([]byte, secrets.KeySize)
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	raw, csrf, _, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key[0] = 1
	wrong, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	other := &Store{db: s.db, dialect: s.dialect, clock: s.clock}
	other.csrfCipher, other.csrfKey = wrong, nil
	if _, err := other.AsOperator().LookupSession(t.Context(), raw); err == nil {
		t.Fatal("wrong key authenticated a session")
	}
	other.csrfCipher, other.csrfKey = cipher, nil
	read, err := other.AsOperator().LookupSession(t.Context(), raw)
	if err != nil || read.CSRFToken != csrf {
		t.Fatalf("wrong-key failure changed persisted key: %+v, %v", read, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE settings SET value=? WHERE key='session_csrf_key'`, []byte("not-an-envelope"))
	other.csrfCipher, other.csrfKey = cipher, nil
	if _, err := other.AsOperator().LookupSession(t.Context(), raw); err == nil {
		t.Fatal("corrupt key authenticated a session")
	}
}

func TestCreatedSessionAuthenticatesThroughDigestSnapshot(t *testing.T) {
	s, account, now := sessionRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	raw, csrf, session, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}
	got, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil || len(got.Authorities) != 1 {
		t.Fatalf("created session: %+v, %v", got, err)
	}
	p := got.Authorities[0].Principal
	if p.Capability.Subject.ID != account.ID || !slices.Contains(p.Capability.Grants, capability.RunsWrite) || !p.Capability.IssuedAt.Equal(*now) || !p.Capability.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("session authority: %+v", p)
	}
	read, err := s.AsOperator().LookupSession(t.Context(), raw)
	if err != nil || read.CSRFToken != csrf || read.CSRFToken != session.CSRFToken || !read.CreatedAt.Equal(*now) || !read.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("session roundtrip: %+v, %v", read, err)
	}
}

func TestSessionCreationRefusesForeignMembership(t *testing.T) {
	s, account, _ := sessionRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	raw, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), account, "b", time.Hour)
	if err == nil || raw != "" {
		t.Fatal("session created without current team membership")
	}
}

func TestCapabilitySessionEndsWithItsCapability(t *testing.T) {
	for _, cause := range []string{"expired", "revoked"} {
		t.Run(cause, func(t *testing.T) {
			s, account, now := sessionRuntimeStore(t)
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			s.csrfCipher, s.csrfKey = cipher, nil
			root := authorityRoot("login", "a")
			root.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: account.ID}
			root.Grants = []capability.Grant{capability.RunsRead}
			expiry := now.Add(time.Hour)
			root.ExpiresAt = &expiry
			authorityPersist(t, s, root)
			raw, session, err := s.AsOperator().CreateCapabilitySession(t.Context(), root.Prefix, 24*time.Hour)
			if err != nil || session.CapabilityPrefix != root.Prefix || !slices.Equal(session.Scopes, []string{"runs.read"}) {
				t.Fatalf("capability session: %+v, %v", session, err)
			}
			read, err := s.AsOperator().LookupSession(t.Context(), raw)
			if err != nil || !slices.Equal(read.Scopes, []string{"runs.read"}) {
				t.Fatalf("session widened parent: %+v, %v", read, err)
			}
			if cause == "expired" {
				*now = expiry
			} else {
				target := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
				if err := (&Tenant{s: s, team: "a"}).RevokeAuthority(t.Context(), Actor{Authority: target, CapabilityPrefix: root.Prefix}, target); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AsOperator().LookupSession(t.Context(), raw); err == nil {
				t.Fatalf("session survived %s parent", cause)
			}
			if _, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}, capability.RequestEvidence{}); err == nil {
				t.Fatalf("digest authentication survived %s parent", cause)
			}
		})
	}
}

func TestSessionMembershipChangesApplyWithoutCachedGrantsProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(141, 803))
	for i := range 16 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, account, _ := sessionRuntimeStore(t)
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			s.csrfCipher, s.csrfKey = cipher, nil
			raw, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			owner := authorityMember(t, s, "owner", RoleOwner)
			tenant := &Tenant{s: s, team: "a"}
			previous := RoleEditor
			for range 4 {
				role := []Role{RoleReader, RoleEditor, RoleOwner}[random.IntN(3)]
				if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: account.ID, Role: &role}); err != nil {
					t.Fatal(err)
				}
				if !role.AtLeast(previous) {
					if _, err := s.AsOperator().LookupSession(t.Context(), raw); err == nil {
						t.Fatal("demotion retained the old session")
					}
					raw, _, _, err = s.AsOperator().CreateAccountSession(t.Context(), account, "a", time.Hour)
					if err != nil {
						t.Fatal(err)
					}
				}
				previous = role
				got, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}, capability.RequestEvidence{})
				if err != nil || len(got.Authorities) != 1 {
					t.Fatalf("membership snapshot: %+v, %v", got, err)
				}
				grants := got.Authorities[0].Principal.Capability.Grants
				for _, grant := range grants {
					if !slices.Contains(capability.Grants(), grant) {
						t.Fatalf("session acquired an unknown grant: %s", grant)
					}
				}
				if !slices.Contains(grants, capability.RunsRead) || slices.Contains(grants, capability.RunsWrite) != (role != RoleReader) || slices.Contains(grants, capability.TeamAdmin) != (role == RoleOwner) {
					t.Fatalf("stale role %s grants: %+v", role, grants)
				}
			}
			if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: account.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AsOperator().LookupSession(t.Context(), raw); err == nil {
				t.Fatal("removed member's session authenticated")
			}
		})
	}
}

func TestCapabilitySessionCannotChangeParentIdentityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(913, 736))
	for i := range 16 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, account, _ := sessionRuntimeStore(t)
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			s.csrfCipher, s.csrfKey = cipher, nil
			root := authorityRoot(fmt.Sprintf("root-%x", random.Uint64()), "a")
			root.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: account.ID}
			root.Grants = []capability.Grant{capability.RunsRead}
			authorityPersist(t, s, root)
			raw, _, err := s.AsOperator().CreateCapabilitySession(t.Context(), root.Prefix, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b',?,'owner',1)`, account.ID)
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET team='b' WHERE hash=?`, sessionDigest(raw))
			if _, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}, capability.RequestEvidence{}); err == nil {
				t.Fatal("session widened its parent's team")
			}
			if _, err := s.AsOperator().LookupSession(t.Context(), raw); err == nil {
				t.Fatal("session lookup widened its parent's team")
			}
		})
	}
}

func TestExpiredSessionCannotRenewProperty(t *testing.T) {
	s, account, now := sessionRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	random := rand.New(rand.NewPCG(930, 552))
	for range 32 {
		lifetime := time.Duration(random.Int64N(int64(time.Second))) + 1
		raw, _, session, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", lifetime)
		if err != nil {
			t.Fatal(err)
		}
		*now = session.ExpiresAt.Add(time.Duration(random.IntN(2)))
		if _, err := s.AsOperator().LookupSessionAndRenew(t.Context(), raw, time.Hour, 24*time.Hour); err == nil {
			t.Fatal("renewal revived an expired session")
		}
		if _, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}, capability.RequestEvidence{}); err == nil {
			t.Fatal("expired session authenticated through its digest")
		}
	}
}

func TestSessionLookupClassifiesAuthorityBackendFailure(t *testing.T) {
	s, account, _ := sessionRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	raw, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `ALTER TABLE memberships RENAME TO unavailable_memberships`)
	for _, renew := range []bool{false, true} {
		var err error
		if renew {
			_, err = s.AsOperator().LookupSessionAndRenew(t.Context(), raw, time.Hour, 24*time.Hour)
		} else {
			_, err = s.AsOperator().LookupSession(t.Context(), raw)
		}
		if !errors.Is(err, ErrSessionBackend) {
			t.Fatalf("backend failure lost classification: %v", err)
		}
	}
}
