package authwire_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

var (
	testClaim = &authwire.CacheClaim{Kind: authwire.CacheClaimToken, NodeID: "build", Generation: 1, Principal: "agent:a", TokenPrefix: "swc_abc"}
	testScope = &authwire.CacheScope{Repo: "github.com/acme/app", Refs: []string{"refs/heads/main"}}
)

func mint(key, team, run string, now time.Time, ttl time.Duration) (string, error) {
	return authwire.MintClaimCacheGrant(key, team, run, now, ttl, testClaim, testScope)
}

func TestCacheGrantVerifiesOnlyWhatTheTokenSigned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw, err := mint("operator-token", "team-a", "run-1", now, time.Hour)
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

	other, err := mint("operator-token", "team-b", "run-1", now, time.Hour)
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
		if _, err := mint("operator-token", team, "run-1", time.Now(), time.Hour); err == nil {
			t.Errorf("minted a grant for team %q", team)
		}
	}
}

func TestCacheGrantWireFormatIsStable(t *testing.T) {
	// safety: the controller mints and the cache verifies on possibly different builds,
	// so the MAC key derivation and payload encoding are a wire contract.
	raw, err := mint("k", "team-a", "r", time.Unix(1_800_000_000, 0), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const want = "swcg1.eyJ0IjoidGVhbS1hIiwiciI6InIiLCJlIjoxODAwMDAzNjAwLCJjIjp7ImsiOiJjbGFpbSIsIm4iOiJidWlsZCIsImciOjEsInAiOiJhZ2VudDphIiwidCI6InN3Y19hYmMifSwicyI6eyJwIjoiZ2l0aHViLmNvbS9hY21lL2FwcCIsImYiOlsicmVmcy9oZWFkcy9tYWluIl19fQ" +
		".hs8lTK_F21V7nW1hEF833LS0LrD0QsfNkQ2R2y5X4TY"
	if raw != want {
		t.Fatalf("grant = %s, want %s", raw, want)
	}
}

func TestCacheGrantMintRefusesMissingInputs(t *testing.T) {
	now := time.Now()
	cases := map[string]func() (string, error){
		"blank key": func() (string, error) { return mint("  ", "team-a", "run-1", now, time.Hour) },
		"no run":    func() (string, error) { return mint("k", "team-a", "", now, time.Hour) },
		"zero ttl":  func() (string, error) { return mint("k", "team-a", "run-1", now, 0) },
	}
	for name, mint := range cases {
		if raw, err := mint(); err == nil {
			t.Errorf("%s: minted %q", name, raw)
		}
	}
}

// A grant with no claim or no scope opened the team's whole tree on its
// signature alone, so none is minted.
func TestCacheGrantMintRefusesAGrantWithoutClaimOrScope(t *testing.T) {
	now := time.Now()
	for name, mint := range map[string]func() (string, error){
		"no claim": func() (string, error) {
			return authwire.MintClaimCacheGrant("k", "team-a", "run-1", now, time.Hour, nil, testScope)
		},
		"no scope": func() (string, error) {
			return authwire.MintClaimCacheGrant("k", "team-a", "run-1", now, time.Hour, testClaim, nil)
		},
	} {
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
