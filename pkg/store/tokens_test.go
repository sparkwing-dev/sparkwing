package store

import (
	"crypto/sha256"
	"errors"
	"path/filepath"
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

	var stored int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != minters {
		t.Fatalf("stored %d rows, want %d", stored, minters)
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
