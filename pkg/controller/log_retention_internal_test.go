package controller

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestLogClassFollowsTheOperatorMapThenTheTeamsStanding(t *testing.T) {
	s := (&Server{}).WithLogRetention(map[store.Team]int{"audit": 365})
	for _, tc := range []struct {
		team store.Team
		tier store.TeamStorageTier
		want string
	}{
		{"audit", store.TeamTierFree, "r365"},
		{"acme", store.TeamTierFunded, logseg.ClassStandard},
		{"acme", store.TeamTierFree, logseg.ClassFree},
	} {
		if got := s.logClassFor(tc.team, tc.tier); got != tc.want {
			t.Errorf("class for %s on %s = %s, want %s", tc.team, tc.tier, got, tc.want)
		}
	}
	if got := s.logClasses(); !slices.Equal(got, []string{"r30", "r365", "r90"}) {
		t.Fatalf("classes = %v", got)
	}
}
