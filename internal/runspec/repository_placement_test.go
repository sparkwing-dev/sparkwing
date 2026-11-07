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

func TestTierMaySatisfyOperatingSystemRequirements(t *testing.T) {
	for name, test := range map[string]struct {
		tier     RepositoryTier
		requires []string
		want     bool
	}{
		"cloud runs linux":            {RepositoryTier{Target: PlacementCloud}, []string{"os=darwin"}, false},
		"cloud alternative":           {RepositoryTier{Target: PlacementCloud}, []string{"os=darwin,os=linux"}, true},
		"mixed term is not judged":    {RepositoryTier{Target: PlacementCloud}, []string{"os=darwin,gpu"}, true},
		"macOS hosted image":          {RepositoryTier{Target: PlacementGitHubActions, Runner: "macos-latest"}, []string{"os=darwin"}, true},
		"windows image for darwin":    {RepositoryTier{Target: PlacementGitHubActions, Runner: "windows-latest"}, []string{"os=darwin"}, false},
		"unknown image is not judged": {RepositoryTier{Target: PlacementGitHubActions, Runner: "self-image"}, []string{"os=darwin"}, true},
		"team tier is open":           {RepositoryTier{Target: PlacementTeam}, []string{"os=darwin"}, true},
		"team tier narrowed to win":   {RepositoryTier{Target: PlacementTeam, Labels: []string{"os=windows"}}, []string{"os=darwin"}, false},
	} {
		if got := test.tier.MaySatisfy(test.requires); got != test.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
