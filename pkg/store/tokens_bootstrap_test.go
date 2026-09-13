package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

const bootstrapRaw = "swu_bootstrapadmintokenvalue000001"

func TestCreateTokenIfNoneExist_WritesOnceOnEmptyTable(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

	created, err := s.CreateTokenIfNoneExist(bootstrapRaw, "bootstrap:admin", store.TokenKindUser,
		[]string{"admin"}, now)
	if err != nil {
		t.Fatalf("CreateTokenIfNoneExist: %v", err)
	}
	if !created {
		t.Fatal("CreateTokenIfNoneExist on an empty table reported no write")
	}

	tok, err := s.LookupToken(bootstrapRaw, now)
	if err != nil {
		t.Fatalf("LookupToken: %v", err)
	}
	if tok.Principal != "bootstrap:admin" || !tok.HasScope("admin") {
		t.Fatalf("token = %+v, want principal bootstrap:admin with scope admin", tok)
	}
	if tok.ExpiresAt != nil {
		t.Fatalf("bootstrap token expires at %v, want no expiry", tok.ExpiresAt)
	}

	stored, err := s.ListTokens("", true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("ListTokens len=%d want 1", len(stored))
	}
	if strings.Contains(stored[0].Hash, bootstrapRaw) {
		t.Fatal("stored hash carries the raw token")
	}

	const second = "swu_adifferentbootstrapvalue000002"
	created, err = s.CreateTokenIfNoneExist(second, "bootstrap:admin", store.TokenKindUser,
		[]string{"admin"}, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second CreateTokenIfNoneExist: %v", err)
	}
	if created {
		t.Fatal("CreateTokenIfNoneExist wrote a second token over a populated table")
	}
	if _, err := s.LookupToken(second, now.Add(time.Hour)); !errors.Is(err, store.ErrNoTokenCandidates) {
		t.Fatalf("LookupToken(second) err=%v, want ErrNoTokenCandidates", err)
	}
}

func TestCreateTokenIfNoneExist_RefusesMalformedRaw(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

	for name, raw := range map[string]string{
		"no marker":    "bootstrapadmintokenvalue000000001",
		"wrong marker": "abc_bootstrapadmintokenvalue00001",
		"runner kind":  "swr_bootstrapadmintokenvalue00001",
		"too short":    "swu_short",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			created, err := s.CreateTokenIfNoneExist(raw, "bootstrap:admin", store.TokenKindUser,
				[]string{"admin"}, now)
			if err == nil {
				t.Fatalf("CreateTokenIfNoneExist(%q) succeeded", raw)
			}
			if created {
				t.Fatal("refused token reported as created")
			}
		})
	}

	stored, err := s.ListTokens("", true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("ListTokens len=%d want 0", len(stored))
	}
}

func TestValidateRawToken_AcceptsAMintedShape(t *testing.T) {
	if err := store.ValidateRawToken(bootstrapRaw, store.TokenKindUser); err != nil {
		t.Fatalf("ValidateRawToken: %v", err)
	}
	if err := store.ValidateRawToken(bootstrapRaw, "nonesuch"); err == nil {
		t.Fatal("ValidateRawToken accepted an unknown kind")
	}
}
