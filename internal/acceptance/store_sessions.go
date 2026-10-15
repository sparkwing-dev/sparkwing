package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreSessions(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "sessions.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	key := sha256.Sum256([]byte("session-persistence-acceptance"))
	cipher, err := secrets.NewCipher(key[:])
	if err != nil {
		return err
	}
	s.SetSessionKeyCipher(cipher)
	for _, member := range []struct{ id, role string }{{"owner", "owner"}, {"member", "editor"}} {
		if _, err := s.DB().ExecContext(ctx, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,?,1,1)`, member.id, member.id+"@example.test"); err != nil {
			return err
		}
		if _, err := s.DB().ExecContext(ctx, `INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,?,?,1)`, store.DefaultTeam, member.id, member.role); err != nil {
			return err
		}
	}
	raw, csrf, session, err := s.CreateAccountSession(ctx, store.Account{ID: "member", Email: "forged@example.test"}, store.DefaultTeam, time.Hour)
	if err != nil {
		return err
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialSession, Digest: sha256.Sum256([]byte(raw)), Audience: capability.Controller}
	read, err := s.ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
	if err != nil {
		return err
	}
	if len(read.Authorities) != 1 || read.Authorities[0].Principal.Capability.Subject.ID != "member" || !slices.Contains(read.Authorities[0].Principal.Capability.Grants, capability.RunsWrite) || !read.Authorities[0].Principal.Capability.IssuedAt.Equal(session.CreatedAt) || session.Principal != "member@example.test" {
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
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	s.SetSessionKeyCipher(cipher)
	current, err := s.LookupSession(ctx, raw)
	if err != nil {
		return err
	}
	if current.CSRFToken != csrf || !current.CreatedAt.Equal(session.CreatedAt) || !current.ExpiresAt.Equal(session.ExpiresAt) {
		return errors.New("reopening changed session identity, csrf or timestamps")
	}
	ownerRaw, _, _, err := s.CreateAccountSession(ctx, store.Account{ID: "owner"}, store.DefaultTeam, time.Hour)
	if err != nil {
		return err
	}
	ownerDigest := sha256.Sum256([]byte(ownerRaw))
	owner := store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(ownerDigest[:])}}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	role := store.RoleReader
	if err := tenant.UpdateMembership(ctx, owner, store.MembershipUpdate{AccountID: "member", Role: &role}); err != nil {
		return err
	}
	read, err = s.ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
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
	if _, err := s.ReadAuthentication(ctx, lookup, capability.RequestEvidence{}); err == nil {
		return errors.New("revoked session still authenticates by digest")
	}
	if _, err := s.LookupSession(ctx, raw); err == nil {
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
	if _, err := s.LookupSession(ctx, ownerRaw); err != nil {
		return fmt.Errorf("maintenance removed the live owner session: %w", err)
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err = store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	s.SetSessionKeyCipher(cipher)
	var retained int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE hash=?`, hex.EncodeToString(lookup.Digest[:])).Scan(&retained); err != nil {
		return err
	}
	if retained != 0 {
		return errors.New("expired session returned after reopening")
	}
	return nil
}
