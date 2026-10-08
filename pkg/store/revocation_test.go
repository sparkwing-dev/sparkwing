package store

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestRevokeToken_ClampsRotationGrace(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	rawA, tokA, err := s.CreateToken("pool", TokenKindRunner, []string{"nodes.claim"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, _, _, err := s.RotateToken(tokA.Prefix, 24*time.Hour, 0, now); err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if err := s.RevokeToken(tokA.Prefix, now.Add(time.Minute)); err != nil {
		t.Fatalf("RevokeToken during grace: %v", err)
	}

	got, err := s.LookupTokenByPrefix(tokA.Prefix)
	if err != nil {
		t.Fatalf("LookupTokenByPrefix: %v", err)
	}
	if got.RevokedAt == nil {
		t.Fatalf("revoked_at is nil after revoke")
	}
	if want := now.Add(time.Minute).Unix(); got.RevokedAt.Unix() != want {
		t.Fatalf("revoked_at = %d, want %d (clamped to the revoke time)", got.RevokedAt.Unix(), want)
	}
	if _, err := s.LookupToken(rawA, now.Add(2*time.Minute)); err == nil {
		t.Fatalf("old token still authenticates after an early revoke")
	}
}

func TestRevokeToken_AlreadyEffectiveIsRejected(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	_, tok, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := s.RevokeToken(tok.Prefix, now); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := s.RevokeToken(tok.Prefix, now.Add(time.Second)); err == nil {
		t.Fatalf("second revoke should report the token already revoked")
	}
}

func TestTokenSession_EndsWithItsToken(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	_, tok, err := s.CreateToken("operator", TokenKindUser, []string{"admin"}, time.Hour, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	raw, _, err := s.CreateTokenSession(t.Context(), tok.Prefix, tok.Principal, tok.Team, 24*time.Hour, now)
	if err != nil {
		t.Fatalf("CreateTokenSession: %v", err)
	}
	sess, err := s.LookupSession(t.Context(), raw, now.Add(time.Second))
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if !slices.Equal(sess.Scopes, []string{"admin"}) || sess.TokenPrefix != tok.Prefix || sess.Team != DefaultTeam {
		t.Fatalf("session = scopes %v prefix %q team %q, want the token's", sess.Scopes, sess.TokenPrefix, sess.Team)
	}
	if _, err := s.LookupSession(t.Context(), raw, now.Add(2*time.Hour)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("session past its token's expiry = %v, want ErrTokenExpired", err)
	}
	if err := s.RevokeToken(tok.Prefix, now.Add(time.Minute)); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := s.LookupSession(t.Context(), raw, now.Add(2*time.Minute)); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("session of a revoked token = %v, want ErrTokenRevoked", err)
	}
}
