package store

import (
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"sync"
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

func dropPrefixUniqueIndex(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.execNoCtx(`DROP INDEX ` + TokenPrefixIndexName); err != nil {
		t.Fatalf("drop %s: %v", TokenPrefixIndexName, err)
	}
}

func collidePrefixes(t *testing.T, s *Store, keep, drop string) {
	t.Helper()
	dropPrefixUniqueIndex(t, s)
	if _, err := s.execNoCtx(`UPDATE tokens SET prefix = ? WHERE prefix = ?`, keep, drop); err != nil {
		t.Fatalf("collide: %v", err)
	}
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
	if _, err := s.LookupToken(raw, now.Add(2*time.Hour)); err == nil {
		t.Fatalf("expected expired token to fail lookup")
	}
	if _, err := s.LookupToken(raw, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("within-ttl lookup should succeed: %v", err)
	}
}

func TestLookupToken_IgnoresCollidedRowsWithDifferentHash(t *testing.T) {
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
	collidePrefixes(t, s, tokA.Prefix, tokB.Prefix)

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
		"swu":        "",
		"xxx_foo":    "",
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

	if _, err := s.LookupToken(rawA, now.Add(12*time.Hour)); err != nil {
		t.Fatalf("old token should authenticate during grace: %v", err)
	}
	if _, err := s.LookupToken(rawA, now.Add(25*time.Hour)); err == nil {
		t.Fatalf("old token should not authenticate after grace")
	}
	if _, err := s.LookupToken(rawB, now.Add(time.Second)); err != nil {
		t.Fatalf("new token should authenticate: %v", err)
	}
}

func TestCreateToken_ConcurrentMintsGetDistinctPrefixes(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	const minters = 4
	prefixes := make([]string, minters)
	errs := make([]error, minters)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range minters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, tok, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
			if err != nil {
				errs[i] = err
				return
			}
			prefixes[i] = tok.Prefix
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]struct{}{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("minter %d: %v", i, err)
		}
		seen[prefixes[i]] = struct{}{}
	}
	if len(seen) != minters {
		t.Fatalf("got %d distinct prefixes from %d mints: %v", len(seen), minters, prefixes)
	}

	stored, err := s.ListTokens("", true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(stored) != minters {
		t.Fatalf("stored %d rows, want %d", len(stored), minters)
	}
}

func TestCreateToken_StoreRefusesASecondRowOnOnePrefix(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	_, tok, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	_, err = s.execNoCtx(
		`INSERT INTO tokens (hash, prefix, principal, kind, scopes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"argon2id$00$00", tok.Prefix, "mallory", TokenKindUser, "admin", now.Unix(),
	)
	if err == nil {
		t.Fatal("a second row carrying a live prefix should violate the unique index")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("err = %v, want a unique-constraint violation", err)
	}
}

func TestAmbiguousPrefixRefusesLookupRevokeAndRotate(t *testing.T) {
	cases := []struct {
		name string
		act  func(*Store, string, time.Time) error
	}{
		{"lookup", func(s *Store, prefix string, _ time.Time) error {
			_, err := s.LookupTokenByPrefix(prefix)
			return err
		}},
		{"revoke", func(s *Store, prefix string, now time.Time) error {
			return s.RevokeToken(prefix, now)
		}},
		{"rotate", func(s *Store, prefix string, now time.Time) error {
			_, _, _, err := s.RotateToken(prefix, time.Hour, time.Hour, now)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			now := time.Now().UTC()
			_, tokA, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
			if err != nil {
				t.Fatalf("CreateToken A: %v", err)
			}
			_, tokB, err := s.CreateToken("bob", TokenKindUser, []string{"runs.read"}, 0, now)
			if err != nil {
				t.Fatalf("CreateToken B: %v", err)
			}
			collidePrefixes(t, s, tokA.Prefix, tokB.Prefix)

			err = tc.act(s, tokA.Prefix, now)
			if err == nil {
				t.Fatalf("%s on an ambiguous prefix should fail", tc.name)
			}
			if !strings.Contains(err.Error(), "matched 2 rows") {
				t.Fatalf("err = %v, want the ambiguity refusal", err)
			}

			live, err := s.ListTokens("", false)
			if err != nil {
				t.Fatalf("ListTokens: %v", err)
			}
			if len(live) != 2 {
				t.Fatalf("%d live rows after the refusal, want 2", len(live))
			}
		})
	}
}

func fakeArgonKey(secret, salt []byte, keyLen uint32) []byte {
	sum := sha256.Sum256(append(append([]byte{}, secret...), salt...))
	out := make([]byte, keyLen)
	copy(out, sum[:])
	return out
}

func TestRotateToken_ARevokeDuringTheMintIsNotUndone(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	restore := argonIDFunc
	t.Cleanup(func() { argonIDFunc = restore })

	armed := false
	var once sync.Once
	var revokeErr error
	revokeDone := make(chan struct{})
	prefix := ""
	argonIDFunc = func(secret, salt []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		if armed {
			once.Do(func() {
				go func() {
					revokeErr = s.RevokeToken(prefix, now)
					close(revokeDone)
				}()
				select {
				case <-revokeDone:
				case <-time.After(200 * time.Millisecond):
				}
			})
		}
		return fakeArgonKey(secret, salt, keyLen)
	}

	raw, tok, err := s.CreateToken("alice", TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	prefix = tok.Prefix
	armed = true

	_, _, _, err = s.RotateToken(prefix, 24*time.Hour, 0, now)
	if err != nil && !strings.Contains(err.Error(), "already revoked") {
		t.Fatalf("RotateToken: %v", err)
	}

	select {
	case <-revokeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the concurrent revoke never finished")
	}
	if revokeErr != nil {
		t.Fatalf("RevokeToken: %v", revokeErr)
	}

	if _, err := s.LookupToken(raw, now.Add(time.Minute)); err == nil {
		t.Fatal("the revoked token still authenticates after the rotation")
	}
	revoked, err := s.LookupTokenByPrefix(prefix)
	if err != nil {
		t.Fatalf("LookupTokenByPrefix: %v", err)
	}
	if revoked.RevokedAt == nil || revoked.RevokedAt.After(now) {
		t.Fatalf("revoked_at = %v, want no later than the revoke at %v", revoked.RevokedAt, now)
	}
}

func TestMintRetryOnlyMatchesAPrefixCollision(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sqlite prefix", errors.New("constraint failed: UNIQUE constraint failed: tokens.prefix (2067)"), true},
		{"postgres prefix", errors.New(
			`ERROR: duplicate key value violates unique constraint "idx_tokens_prefix" (SQLSTATE 23505)`), true},
		{"sqlite hash", errors.New("constraint failed: UNIQUE constraint failed: tokens.hash (2067)"), false},
		{"sqlite principal", errors.New("constraint failed: UNIQUE constraint failed: tokens.principal (2067)"), false},
		{"not a violation", errors.New("database is locked"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTokenPrefixCollision(tc.err); got != tc.want {
				t.Fatalf("isTokenPrefixCollision(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
