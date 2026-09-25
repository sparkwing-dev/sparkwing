package store

import (
	"errors"
	"testing"
)

// claimScope never builds these scopes today, so the refusal is the only thing
// standing between a future caller and a claim with no team predicate.
func TestClaimTeamWhereRefusesAScopeThatNamesNoOneTeam(t *testing.T) {
	for name, scope := range map[string]teamScope{
		"all teams":  allTeams(),
		"zero value": {},
	} {
		clause, args, err := claimTeamWhere(scope, "team")
		if !errors.Is(err, ErrClaimantHasNoTeam) {
			t.Errorf("%s: clause %q args %v err %v, want ErrClaimantHasNoTeam", name, clause, args, err)
		}
	}
}
