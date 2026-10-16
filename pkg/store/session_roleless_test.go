package store

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestRolelessSessionsHaveNoTeamAuthorityProperty(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1700000000, 123456789).UTC()
	s.clock = func() time.Time { return now }
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSessionKeyCipher(cipher)
	for i := range 16 {
		id := fmt.Sprintf("waitlisted-%d", i)
		rewriteSchemaExec(t, s.db, `INSERT INTO accounts(id,email,email_verified,created_at,updated_at,waitlist_reason) VALUES (?,?,1,1,1,'signup_gate')`, id, id+"@example.test")
		rewriteSchemaExec(t, s.db, `INSERT INTO identities(provider,subject,account_id,email_verified,created_at) VALUES ('github',?,?,1,1)`, id, id)
		raw, csrf, session, err := s.CreateIdentityAccountSession(t.Context(), Account{ID: id}, "", "github", id, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if session.Team != "" || len(session.Scopes) != 0 || csrf == "" {
			t.Fatalf("roleless creation: %+v", session)
		}
		rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a',?,'owner',1)`, id)
		got, err := s.LookupSessionAndRenew(t.Context(), raw, 2*time.Hour, 24*time.Hour)
		if err != nil || got.Team != "" || len(got.Scopes) != 0 || got.CSRFToken != csrf || !got.ExpiresAt.Equal(now.Add(2*time.Hour)) {
			t.Fatalf("roleless renewal: %+v, %v", got, err)
		}
		lookup := capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}
		snap, err := s.ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{Target: capability.Target{Team: "a"}})
		if err != nil {
			t.Fatal(err)
		}
		principal, err := capability.ValidateAuthority(snap.Authorities, snap.Now)
		if err != nil || principal.Capability.Team != "" || len(principal.Capability.Grants) != 0 {
			t.Fatalf("roleless snapshot: %+v, %v", principal, err)
		}
		for _, path := range []string{"/api/v1/me", "/api/v1/runs"} {
			route, ok := capability.LookupRoute("GET", path)
			if !ok {
				t.Fatalf("missing policy %s", path)
			}
			auth, err := capability.NewAuthenticator(s).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
			if err != nil {
				t.Fatal(err)
			}
			target := capability.Target{AccountID: id}
			if path == "/api/v1/runs" {
				target.Team = "a"
			}
			_, err = capability.Authorize(t.Context(), s, route, auth, target)
			if (err == nil) != (path == "/api/v1/me") {
				t.Fatalf("roleless access %s: %v", path, err)
			}
		}
		rewriteSchemaRefuses(t, s.db, `UPDATE sessions SET capability_prefix='forged' WHERE hash=?`, sessionDigest(raw))
		rewriteSchemaRefuses(t, s.db, `UPDATE sessions SET account_id='' WHERE hash=?`, sessionDigest(raw))
		if err := s.SelectAccountTeam(t.Context(), teamAuthorization(t, s, Actor{Authority: principal.Authority}, "", "POST", "/api/v1/me/active-team"), "a"); err != nil {
			t.Fatal(err)
		}
		got, err = s.LookupSession(t.Context(), raw)
		if err != nil || got.Team != "a" || len(got.Scopes) == 0 {
			t.Fatalf("explicit team switch: %+v, %v", got, err)
		}
	}
}
