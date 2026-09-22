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
