package store

import (
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
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

func fakeArgonKey(secret, salt []byte, keyLen uint32) []byte {
	sum := sha256.Sum256(append(append([]byte{}, secret...), salt...))
	out := make([]byte, keyLen)
	copy(out, sum[:])
	return out
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
