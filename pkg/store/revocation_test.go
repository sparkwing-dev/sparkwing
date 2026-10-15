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
