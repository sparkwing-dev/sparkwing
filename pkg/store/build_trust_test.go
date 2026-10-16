package store

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestStoreDataBuildTrustDefaultsAndTenantIsolation(t *testing.T) {
	s := authorityStore(t)
	_ = authorityMember(t, s, "owner", RoleOwner)
	a, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ForTeam(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []*Tenant{a, b} {
		if got, err := tenant.TrustLocalBuilds(t.Context()); err != nil || got {
			t.Fatalf("default for %s=%v, %v", tenant.Team(), got, err)
		}
	}
	for _, trust := range []bool{true, true, false, false} {
		if err := a.SetAuthorizedBuildTrust(t.Context(), a.mutationFixtureAuthority(t, "owner", capability.TeamAdmin, time.Now()), trust); err != nil {
			t.Fatal(err)
		}
		if got, err := a.TrustLocalBuilds(t.Context()); err != nil || got != trust {
			t.Fatalf("selected team=%v, %v; want %v", got, err, trust)
		}
		if got, err := b.TrustLocalBuilds(t.Context()); err != nil || got {
			t.Fatalf("foreign team=%v, %v", got, err)
		}
	}
}

func TestStoreDataBuildTrustPersistsOnlyTheSelectedTenantProperty(t *testing.T) {
	s := authorityStore(t)
	_ = authorityMember(t, s, "owner", RoleOwner)
	path := blobAccountingDiskStore(t, s)
	iteration := 0
	property := func(suffix string, first, second, reopen bool) bool {
		iteration++
		tenants := make([]*Tenant, 2)
		for owner := range tenants {
			team := NormalizeTeam(Team(fmt.Sprintf("build_%d_%d_%s_%%'--", iteration, owner, suffix)))
			rewriteSchemaExec(t, s.db, "INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1)", team)
			rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES (?,'owner','owner',1)`, team)
			var err error
			tenants[owner], err = s.ForTeam(t.Context(), team)
			if err != nil {
				t.Fatal(err)
			}
		}
		check := func(want [2]bool) bool {
			for owner, tenant := range tenants {
				got, err := tenant.TrustLocalBuilds(t.Context())
				if err != nil || got != want[owner] {
					t.Logf("team %q policy=%v, %v; want %v", tenant.Team(), got, err, want[owner])
					return false
				}
			}
			return true
		}
		if !check([2]bool{}) {
			return false
		}
		for _, want := range [][2]bool{{first, second}, {!first, second}, {!first, !second}} {
			for owner, tenant := range tenants {
				if err := tenant.SetAuthorizedBuildTrust(t.Context(), tenant.mutationFixtureAuthority(t, "owner", capability.TeamAdmin, time.Now()), want[owner]); err != nil {
					t.Fatal(err)
				}
			}
			if reopen {
				if err := s.db.Close(); err != nil {
					t.Fatal(err)
				}
				s.db = blobAccountingDB(t, path)
			}
			if !check(want) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(73))}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDataBuildTrustFailuresNeverEnableIt(t *testing.T) {
	s := authorityStore(t)
	_ = authorityMember(t, s, "owner", RoleOwner)
	tenant, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_build_trust BEFORE INSERT ON team_build_trust BEGIN SELECT RAISE(ABORT,'policy write unavailable'); END`)
	if err := tenant.SetAuthorizedBuildTrust(t.Context(), tenant.mutationFixtureAuthority(t, "owner", capability.TeamAdmin, time.Now()), true); err == nil {
		t.Fatal("failed opt-in returned success")
	}
	if trust, err := tenant.TrustLocalBuilds(t.Context()); err != nil || trust {
		t.Fatalf("failed opt-in changed policy=%v, %v", trust, err)
	}
	rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_build_trust")
	for _, value := range []any{2, -1, "true", "false"} {
		rewriteSchemaExec(t, s.db, "INSERT INTO team_build_trust(team,trust_local_builds) VALUES ('a',?) ON CONFLICT(team) DO UPDATE SET trust_local_builds=excluded.trust_local_builds", value)
		if trust, err := tenant.TrustLocalBuilds(t.Context()); err == nil || trust {
			t.Fatalf("corrupt policy %v authorized local builds=%v, %v", value, trust, err)
		}
	}
}
