package store

import (
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

func TestDeleteUser_RevokesSessionsAndTokens(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	if _, err := s.CreateUser("mallory", "correct-horse", []string{"admin"}, now); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	rawSession, _, _, err := s.CreateSession("mallory", []string{"admin"}, time.Hour, now)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rawToken, tok, err := s.CreateToken("mallory", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	rawKeep, keep, err := s.CreateToken("bob", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken bob: %v", err)
	}

	revoked, err := s.DeleteUser("mallory", now)
	if err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != tok.Prefix {
		t.Fatalf("revoked prefixes = %v, want [%s]", revoked, tok.Prefix)
	}
	if _, err := s.LookupSession(rawSession, now.Add(time.Second)); err == nil {
		t.Fatalf("session of a deleted user still resolves")
	}
	if _, err := s.LookupToken(rawToken, now.Add(time.Second)); err == nil {
		t.Fatalf("token of a deleted user still authenticates")
	}
	if _, err := s.LookupToken(rawKeep, now.Add(time.Second)); err != nil {
		t.Fatalf("another principal's token was revoked: %v", err)
	}
	if _, err := s.LookupTokenByPrefix(keep.Prefix); err != nil {
		t.Fatalf("LookupTokenByPrefix bob: %v", err)
	}
}

func TestDeleteUser_UnknownLeavesTokensAlone(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	rawToken, _, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := s.DeleteUser("nobody", now); err == nil {
		t.Fatalf("expected an error deleting an unknown user")
	}
	if _, err := s.LookupToken(rawToken, now.Add(time.Second)); err != nil {
		t.Fatalf("token revoked by a failed delete: %v", err)
	}
}
