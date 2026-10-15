package capability_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestAuthCacheRequiresEveryGeneration(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	p := workPrincipal()
	parent := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "parent"}
	p.Capability.Parent = &parent
	vector := capability.AuthGenerations{Team: "team", MembershipGeneration: 3, Authorities: []capability.AuthorityGeneration{
		{Authority: p.Authority, Generation: 1},
		{Authority: parent, Generation: 4},
		{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "cross-team-root"}, Generation: 5},
	}}
	entry := capability.AuthCacheEntry{Principal: p, Generations: vector, CachedAt: now}
	if !entry.Reusable(vector, now.Add(4*time.Second)) {
		t.Fatal("unchanged live snapshot refused")
	}
	for _, at := range []time.Time{now.Add(-time.Second), now.Add(5 * time.Second)} {
		if entry.Reusable(vector, at) {
			t.Fatal("entry outside cache lifetime reused")
		}
	}
	for _, change := range []func(*capability.AuthGenerations){
		func(g *capability.AuthGenerations) { g.Team = "other" },
		func(g *capability.AuthGenerations) { g.MembershipGeneration++ },
		func(g *capability.AuthGenerations) { g.Authorities[0].Generation++ },
		func(g *capability.AuthGenerations) { g.Authorities[1].Generation++ },
		func(g *capability.AuthGenerations) { g.Authorities[2].Generation++ },
		func(g *capability.AuthGenerations) { g.Authorities = g.Authorities[:2] },
		func(g *capability.AuthGenerations) { g.Authorities[1] = g.Authorities[0] },
	} {
		g := vector
		g.Authorities = slices.Clone(vector.Authorities)
		change(&g)
		if entry.Reusable(g, now) {
			t.Fatal("changed ancestry reused")
		}
	}
	entry.Principal.Capability.ExpiresAt = &now
	if entry.Reusable(vector, now) {
		t.Fatal("expired credential reused")
	}
}
