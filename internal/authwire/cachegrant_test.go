package authwire_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

func TestCacheGrantVerifiesOnlyWhatTheTokenSigned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw, err := authwire.MintCacheGrant("operator-token", "team-a", "run-1", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	g, err := authwire.VerifyCacheGrant("operator-token", raw, now.Add(time.Minute))
	if err != nil || g.Team != "team-a" || g.Run != "run-1" {
		t.Fatalf("own grant = %+v, %v", g, err)
	}

	if _, err := authwire.VerifyCacheGrant("another-token", raw, now); err == nil {
		t.Error("a grant verified under a token that did not sign it")
	}
	if _, err := authwire.VerifyCacheGrant("operator-token", raw, now.Add(time.Hour)); err == nil {
		t.Error("an expired grant verified")
	}

	// Rewriting the team in the payload must break the signature.
	other, err := authwire.MintCacheGrant("operator-token", "team-b", "run-1", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	forged := other[:strings.LastIndexByte(other, '.')] + raw[strings.LastIndexByte(raw, '.'):]
	if _, err := authwire.VerifyCacheGrant("operator-token", forged, now); err == nil {
		t.Error("team B's payload verified under team A's signature")
	}
	if _, err := authwire.VerifyCacheGrant("operator-token", "operator-token", now); err == nil {
		t.Error("the operator token itself verified as a grant")
	}
}

func TestCacheGrantRefusesATeamThatCouldLeaveItsDirectory(t *testing.T) {
	for _, team := range []string{"", "..", "a/b", "Team", "-a"} {
		if _, err := authwire.MintCacheGrant("operator-token", team, "run-1", time.Now(), time.Hour); err == nil {
			t.Errorf("minted a grant for team %q", team)
		}
	}
}

func TestCacheGrantWireFormatIsStable(t *testing.T) {
	// The controller mints and the cache verifies, and the two can run
	// different builds, so the MAC key derivation and payload encoding are a
	// wire contract.
	raw, err := authwire.MintCacheGrant("k", "team-a", "r", time.Unix(1_800_000_000, 0), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const want = "swcg1.eyJ0IjoidGVhbS1hIiwiciI6InIiLCJlIjoxODAwMDAzNjAwfQ.8evRyDhk8hVXLPYuYmoZ7GWongdZ7Kv8Hjg_gPkCXcM"
	if raw != want {
		t.Fatalf("grant = %s, want %s", raw, want)
	}
}

func TestCacheGrantMintRefusesMissingInputs(t *testing.T) {
	now := time.Now()
	cases := map[string]func() (string, error){
		"blank key": func() (string, error) { return authwire.MintCacheGrant("  ", "team-a", "run-1", now, time.Hour) },
		"no run":    func() (string, error) { return authwire.MintCacheGrant("k", "team-a", "", now, time.Hour) },
		"zero ttl":  func() (string, error) { return authwire.MintCacheGrant("k", "team-a", "run-1", now, 0) },
	}
	for name, mint := range cases {
		if raw, err := mint(); err == nil {
			t.Errorf("%s: minted %q", name, raw)
		}
	}
}

func TestCacheBearerPrefersTheRunGrant(t *testing.T) {
	t.Setenv(authwire.CacheGrantEnv, "grant")
	t.Setenv(authwire.CacheTokenEnv, "token")
	if got := authwire.CacheBearerFromEnv(); got != "grant" {
		t.Errorf("bearer = %q, want the grant", got)
	}
	t.Setenv(authwire.CacheGrantEnv, "")
	if got := authwire.CacheBearerFromEnv(); got != "token" {
		t.Errorf("bearer = %q, want the operator token", got)
	}
}
