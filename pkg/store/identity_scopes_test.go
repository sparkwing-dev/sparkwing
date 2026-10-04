package store

import (
	"slices"
	"testing"
)

func TestRoleScopeResultsCannotChangePolicy(t *testing.T) {
	if got := ScopesForRole("unknown"); len(got) != 0 {
		t.Fatalf("unknown role scopes=%v", got)
	}
	for _, role := range []Role{RoleReader, RoleEditor, RoleOwner} {
		before := ScopesForRole(role)
		changed := ScopesForRole(role)
		changed[0] = "admin"
		if !slices.Equal(ScopesForRole(role), before) {
			t.Fatalf("caller changed %s policy", role)
		}
		cli := CLITokenScopes(role)
		if slices.Contains(cli, ScopeTeamAdmin) || slices.Contains(cli, "admin") {
			t.Fatalf("CLI administrative authority: %v", cli)
		}
		if !slices.Equal(ScopesForRole(role), before) {
			t.Fatalf("CLI filtering changed %s membership policy", role)
		}
	}
}
