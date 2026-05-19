package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateAndLookupToken(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	raw, tok, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !strings.HasPrefix(raw, "swu_") {
		t.Fatalf("raw missing prefix: %q", raw)
	}
	if tok.Prefix != raw[:PrefixLen] {
		t.Fatalf("prefix mismatch: token=%q raw prefix=%q", tok.Prefix, raw[:PrefixLen])
	}
	if tok.ExpiresAt != nil {
		t.Fatalf("ttl=0 should leave expires_at nil, got %v", tok.ExpiresAt)
	}

	got, err := s.LookupToken(raw, now)
	if err != nil {
		t.Fatalf("LookupToken: %v", err)
	}
	if got.Principal != "alice" || got.Kind != TokenKindUser {
		t.Fatalf("unexpected token: %+v", got)
	}
	if !got.HasScope("admin") {
		t.Fatalf("missing admin scope: %v", got.Scopes)
	}
	if got.LastUsedAt == nil {
		t.Fatalf("last_used_at should be stamped on successful lookup")
	}
}

func TestLookupToken_UnknownReturnsError(t *testing.T) {
	s := newTestStore(t)
	_, err := s.LookupToken("swu_NoSuchTokenValueAtAll00000000000000000000", time.Now())
	if err == nil {
		t.Fatalf("expected error for unknown token")
	}
}

func TestLookupToken_RevokedFails(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	raw, tok, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := s.RevokeToken(tok.Prefix, now); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	// Revocation is immediate; lookup at the same instant must fail.
	if _, err := s.LookupToken(raw, now.Add(time.Second)); err == nil {
		t.Fatalf("expected revoked token to fail lookup")
	}
}

func TestLookupToken_ExpiredFails(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	raw, _, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, time.Hour, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	// Lookup past the ttl must fail.
	if _, err := s.LookupToken(raw, now.Add(2*time.Hour)); err == nil {
		t.Fatalf("expected expired token to fail lookup")
	}
	// Lookup within the ttl still succeeds.
	if _, err := s.LookupToken(raw, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("within-ttl lookup should succeed: %v", err)
	}
}

func TestLookupToken_IgnoresCollidedRowsWithDifferentHash(t *testing.T) {
	// The prefix segment carries 48 bits of entropy; natural
	// collisions are vanishingly rare but not impossible. We force a
	// collision by creating two rows, then overwriting the second
	// row's prefix column to match the first. Verifies that
	// LookupToken iterates the candidate rows and returns the one
	// whose stored hash actually matches the raw token, not just the
	// first row encountered.
	s := newTestStore(t)
	now := time.Now().UTC()

	rawA, tokA, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken A: %v", err)
	}
	_, tokB, err := s.CreateToken("alice", TokenKindUser, []string{"runs.read"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken B: %v", err)
	}
	// Collide: move rowB's prefix to rowA's. Now two rows share a
	// prefix column but have different hashes.
	if _, err := s.db.Exec(`UPDATE tokens SET prefix = ? WHERE prefix = ?`, tokA.Prefix, tokB.Prefix); err != nil {
		t.Fatalf("collide: %v", err)
	}

	got, err := s.LookupToken(rawA, now)
	if err != nil {
		t.Fatalf("LookupToken A: %v", err)
	}
	if got.Principal != "alice" {
		t.Fatalf("expected alice, got %q (LookupToken returned the wrong candidate on a prefix collision)", got.Principal)
	}
}

func TestListTokens_FiltersKindAndRevoked(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	_, user1, _ := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	_, _, _ = s.CreateToken("pool", TokenKindRunner, []string{"nodes.claim"}, 0, now)
	_, _, _ = s.CreateToken("web", TokenKindService, []string{"runs.read"}, 0, now)
	_ = s.RevokeToken(user1.Prefix, now)

	all, err := s.ListTokens("", true)
	if err != nil {
		t.Fatalf("ListTokens all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(all))
	}

	active, err := s.ListTokens("", false)
	if err != nil {
		t.Fatalf("ListTokens active: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("expected 2 active rows, got %d", len(active))
	}

	runners, err := s.ListTokens(TokenKindRunner, false)
	if err != nil {
		t.Fatalf("ListTokens runner: %v", err)
	}
	if len(runners) != 1 || runners[0].Kind != TokenKindRunner {
		t.Fatalf("expected 1 runner, got %+v", runners)
	}
}

func TestTokenKindFromPrefix(t *testing.T) {
	cases := map[string]string{
		"swu_abc":    TokenKindUser,
		"swr_abc":    TokenKindRunner,
		"sws_abc":    TokenKindService,
		"legacy-tok": "",
		"":           "",
		"swu":        "", // no underscore
		"xxx_foo":    "", // unknown marker
	}
	for raw, want := range cases {
		got := TokenKindFromPrefix(raw)
		if got != want {
			t.Fatalf("TokenKindFromPrefix(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestRotateToken(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	rawA, tokA, err := s.CreateToken("pool", TokenKindRunner, []string{"nodes.claim"}, 30*24*time.Hour, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	rawB, newTok, oldTok, err := s.RotateToken(tokA.Prefix, 24*time.Hour, 30*24*time.Hour, now)
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if newTok.Prefix == tokA.Prefix {
		t.Fatalf("new token shares prefix with old")
	}
	if oldTok.ReplacedBy != newTok.Prefix {
		t.Fatalf("old.replaced_by = %q, want %q", oldTok.ReplacedBy, newTok.Prefix)
	}

	// Within grace window: old token still authenticates.
	if _, err := s.LookupToken(rawA, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("old token should authenticate during grace: %v", err)
	}
	// Past grace: old token fails.
	if _, err := s.LookupToken(rawA, now.Add(25*time.Hour)); err == nil {
		t.Fatalf("old token should not authenticate after grace")
	}
	// New token authenticates immediately.
	if _, err := s.LookupToken(rawB, now.Add(time.Second)); err != nil {
		t.Fatalf("new token should authenticate: %v", err)
	}
}
