package runspec

import (
	"testing"
	"time"
)

func TestRepositoryPlacementValidation(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	team := RepositoryTier{Target: PlacementTeam, Hold: time.Second}
	for name, test := range map[string]struct {
		policy RepositoryPlacement
		valid  bool
	}{
		"valid":            {RepositoryPlacement{Revision: sha, Prefer: []RepositoryTier{team}}, true},
		"unsealed":         {RepositoryPlacement{Prefer: []RepositoryTier{team}}, false},
		"duplicate job":    {RepositoryPlacement{Revision: sha, Jobs: []RepositoryJobPlacement{{Job: "a"}, {Job: "a"}}}, false},
		"invalid override": {RepositoryPlacement{Revision: sha, Jobs: []RepositoryJobPlacement{{Job: "a", Prefer: []RepositoryTier{{Target: "here"}}}}}, false},
	} {
		if err := test.policy.Validate(); (err == nil) != test.valid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSealRequestRefusesBurstWithRepositoryPlacement(t *testing.T) {
	request := Request{Burst: &BurstPlacement{Preferences: []PlacementStep{{Target: "cloud"}}}, Placement: &RepositoryPlacement{Revision: "0123456789abcdef0123456789abcdef01234567"}}
	if _, err := SealRequest(request, IntakeFacts{RunID: "run"}); err == nil {
		t.Fatal("sealed a personal burst run with repository placement")
	}
}
