package store

import (
	"testing"
	"time"
)

func TestPoolInventoryHonorsRotationGraceAndExpiry(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1700000000, 0)
	s.clock = func() time.Time { return now }
	for _, name := range []string{"grace", "expired", "revoked"} {
		c := authorityRoot(name, "a")
		c.ReservedPools = []string{"prod"}
		future, past := now.Add(time.Minute), now.Add(-time.Minute)
		if name == "grace" {
			c.RevokedAt = &future
		}
		if name == "expired" {
			c.ExpiresAt = &past
		}
		if name == "revoked" {
			c.RevokedAt = &past
		}
		authorityPersist(t, s, c)
	}
	_, runners, err := (&Tenant{s: s, team: "a"}).ReadPoolAccess(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := runners["prod"]; len(got) != 1 || got[0] != "grace" {
		t.Fatalf("live pool runners=%v, want [grace]", got)
	}
}
