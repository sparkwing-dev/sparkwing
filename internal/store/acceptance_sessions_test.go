package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func checkStoreSessions(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	ownerAccount := signIn(t, s, "owner", "owner@example.test").Account
	memberAccount := signIn(t, s, "member", "member@example.test").Account
	team := ownerAccount.ActiveTeam
	tenant, err := s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	invitation, err := tenant.InviteForTest(t, ctx, ownerAccount.ID, memberAccount.Email, store.RoleEditor, time.Now())
	if err != nil {
		return err
	}
	if _, err := s.AsOperator().AcceptAccountInvitation(ctx, storetest.AccountAuthority(t, s, memberAccount.ID), invitation.ID); err != nil {
		return err
	}
	raw, csrf, session, err := s.AsOperator().CreateAccountSession(ctx, store.Account{ID: memberAccount.ID, Email: "forged@example.test"}, team, time.Hour)
	if err != nil {
		return err
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}
	read, err := s.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
	if err != nil {
		return err
	}
	if len(read.Authorities) != 1 || read.Authorities[0].Principal.Capability.Subject.ID != memberAccount.ID || !slices.Contains(read.Authorities[0].Principal.Capability.Grants, capability.RunsWrite) || !read.Authorities[0].Principal.Capability.IssuedAt.Equal(session.CreatedAt) || session.Principal != "member@example.test" {
		return errors.New("created session did not retain its account and nanosecond authority")
	}
	var reusable int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE hash=? OR hash=?`, raw, csrf).Scan(&reusable); err != nil {
		return err
	}
	var envelope string
	var sealed int
	if err := s.DB().QueryRowContext(ctx, `SELECT value,sealed FROM settings WHERE key='session_csrf_key'`).Scan(&envelope, &sealed); err != nil {
		return err
	}
	if reusable != 0 || sealed != 1 || !strings.HasPrefix(envelope, secrets.BoundPrefix) {
		return errors.New("session persistence contains a reusable plaintext secret")
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := target.TryOpen()
	if err != nil {
		return err
	}
	s = reopened
	current, err := s.AsOperator().LookupSession(ctx, raw)
	if err != nil {
		return err
	}
	if current.CSRFToken != csrf || !current.CreatedAt.Equal(session.CreatedAt) || !current.ExpiresAt.Equal(session.ExpiresAt) {
		return errors.New("reopening changed session identity, csrf or timestamps")
	}
	ownerRaw, _, _, err := s.AsOperator().CreateAccountSession(ctx, ownerAccount, team, time.Hour)
	if err != nil {
		return err
	}
	ownerDigest := sha256.Sum256([]byte(ownerRaw))
	owner := store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(ownerDigest[:])}}
	tenant, err = s.ForTeam(ctx, team)
	if err != nil {
		return err
	}
	role := store.RoleReader
	if err := tenant.UpdateMembership(ctx, owner, store.MembershipUpdate{AccountID: memberAccount.ID, Role: &role}); err != nil {
		return err
	}
	if _, err := s.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{}); err == nil {
		return errors.New("demoted session still authenticates by digest")
	}
	if _, err := s.AsOperator().LookupSession(ctx, raw); err == nil {
		return errors.New("demoted session still authenticates by presentation")
	}
	raw, _, _, err = s.AsOperator().CreateAccountSession(ctx, memberAccount, team, time.Hour)
	if err != nil {
		return err
	}
	lookup.Digest = sha256.Sum256([]byte(raw))
	read, err = s.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
	if err != nil {
		return err
	}
	grants := read.Authorities[0].Principal.Capability.Grants
	if slices.Contains(grants, capability.RunsWrite) || !slices.Contains(grants, capability.RunsRead) {
		return fmt.Errorf("demotion left stale session grants: %v", grants)
	}
	if err := tenant.RevokeAuthority(ctx, owner, capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(lookup.Digest[:])}); err != nil {
		return err
	}
	if _, err := s.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{}); err == nil {
		return errors.New("revoked session still authenticates by digest")
	}
	if _, err := s.AsOperator().LookupSession(ctx, raw); err == nil {
		return errors.New("revoked session still authenticates by presentation")
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE sessions SET expires_at=1 WHERE hash=?`, hex.EncodeToString(lookup.Digest[:])); err != nil {
		return err
	}
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "maintenance", "session-acceptance", time.Hour)
	if err != nil {
		return err
	}
	forged := lease
	forged.Generation++
	if result, err := s.AsOperator().MaintainRetention(ctx, store.RetentionRequest{Lease: forged, Limit: 1}); !errors.Is(err, store.ErrLockHeld) || result != (store.RetentionResult{}) {
		return fmt.Errorf("forged maintenance fence returned %+v: %v", result, err)
	}
	if result, err := s.AsOperator().MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 1}); err != nil || result.Sessions != 1 {
		return fmt.Errorf("session maintenance returned %+v: %v", result, err)
	}
	if _, err := s.AsOperator().LookupSession(ctx, ownerRaw); err != nil {
		return fmt.Errorf("maintenance removed the live owner session: %w", err)
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err = target.TryOpen()
	if err != nil {
		return err
	}
	s = reopened
	var retained int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE hash=?`, hex.EncodeToString(lookup.Digest[:])).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("expired session returned after reopening")
	}
	if err := checkStoreCredentialMutations(ctx, s, team, owner); err != nil {
		return err
	}
	return checkRolelessStoreSession(ctx, s, team)
}

func checkRolelessStoreSession(ctx context.Context, s *store.Store, team store.Team) error {
	profile := store.SignInProfile{Provider: "github", Subject: "waitlisted", Email: "waitlisted@example.test", EmailVerified: true}
	member, err := s.AsOperator().ResolveSignIn(ctx, profile, store.SignUpConditions{ForceWaitlist: true}, time.Now())
	if err != nil {
		return err
	}
	raw, csrf, created, err := s.AsOperator().CreateIdentityAccountSession(ctx, member.Account, "", "github", "waitlisted", time.Hour)
	if err != nil {
		return err
	}
	read, err := s.AsOperator().LookupSessionAndRenew(ctx, raw, 2*time.Hour, 24*time.Hour)
	if err != nil {
		return err
	}
	if read.Team != "" || len(read.Scopes) != 0 || read.CSRFToken != csrf || !read.ExpiresAt.After(created.ExpiresAt) {
		return errors.New("roleless renewal gained team authority or lost its identity")
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}
	snapshot, err := s.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{Target: capability.Target{Team: string(team)}})
	if err != nil {
		return err
	}
	principal, err := capability.ValidateAuthority(snapshot.Authorities, snapshot.Now)
	if err != nil {
		return err
	}
	if principal.Capability.Team != "" || len(principal.Capability.Grants) != 0 {
		return errors.New("roleless authentication gained team authority")
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/me/active-team")
	if err := s.AsOperator().SelectAccountTeam(ctx, capability.Authorized{Principal: principal, Access: route.Access[0]}, team); !errors.Is(err, store.ErrNotMember) {
		return fmt.Errorf("roleless switch bypassed membership: %v", err)
	}
	digest := sha256.Sum256([]byte(raw))
	claim := capability.IdentityLink{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, AccountID: member.Account.ID, Provider: store.ProviderGoogle, Nonce: "roleless-link", ExpiresAt: time.Now().Add(time.Minute)}
	if err := s.AsOperator().ConsumeIdentityLink(ctx, claim); err != nil {
		return fmt.Errorf("link nonce did not commit: %w", err)
	}
	if err := s.AsOperator().ConsumeIdentityLink(ctx, claim); err == nil {
		return errors.New("link nonce replayed")
	}
	return nil
}

func TestStoreSessions(t *testing.T) {
	if err := checkStoreSessions(t); err != nil {
		t.Fatal(err)
	}
}
