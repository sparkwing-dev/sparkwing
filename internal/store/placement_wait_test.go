package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestPlacementTierWaitsMirrorTheClaimHold(t *testing.T) {
	ready := time.Unix(100, 0)
	team := runspec.RepositoryTier{Target: runspec.PlacementTeam, Labels: []string{"os=darwin"}, Hold: 5 * time.Second}
	cloud := runspec.RepositoryTier{Target: runspec.PlacementCloud, Hold: time.Second}
	actions := runspec.RepositoryTier{Target: runspec.PlacementGitHubActions, Workflow: ".github/workflows/f.yaml", Runner: "macos-latest"}
	mac := placementCandidate{name: "build-mac", facts: claimFacts{Tier: tierSelfHosted, Labels: []string{"os=darwin"}, FreeSlots: 1}}
	busy := placementCandidate{name: "build-mac", facts: claimFacts{Tier: tierSelfHosted, Labels: []string{"os=darwin"}}}
	linux := placementCandidate{name: "build-linux", facts: claimFacts{Tier: tierSelfHosted, Labels: []string{"os=linux"}, FreeSlots: 1}}
	unfunded := placementCandidate{name: "launcher", facts: claimFacts{Cloud: 1, FreeSlots: 1}, standing: eligibility{failed: stepBilling}}
	list := []runspec.RepositoryTier{team, cloud, actions}

	waits := placementTierWaits(list, []placementCandidate{mac, unfunded}, nil, ready, ready.Add(2*time.Second))
	if len(waits[0].Live) != 1 || waits[0].OpensIn != 0 || waits[1].OpensIn != 3*time.Second || waits[1].Unavailable != "Cloud credits cannot fund it" || waits[2].OpensIn != 3*time.Second {
		t.Fatalf("live team tier holds later tiers until its deadline: %+v", waits)
	}
	waits = placementTierWaits(list, []placementCandidate{busy, linux}, nil, ready, ready.Add(2*time.Second))
	if waits[0].Busy != 1 || waits[1].OpensIn != 0 || waits[1].Unavailable != "no live runner" || waits[2].Unavailable == "" {
		t.Fatalf("a busy or mismatched team tier holds nothing: %+v", waits)
	}
	waits = placementTierWaits(list, nil, []string{tierSelfHosted}, ready, ready)
	if waits[1].Unavailable != "off in this team's runner ranking" {
		t.Fatalf("ranking exclusion: %+v", waits[1])
	}
}

func TestPlacementTierWaitsCountAClaimantInItsEarliestTierOnly(t *testing.T) {
	ready := time.Unix(100, 0)
	tiers := []runspec.RepositoryTier{
		{Target: runspec.PlacementTeam, Labels: []string{"os=darwin"}, Hold: 5 * time.Second},
		{Target: runspec.PlacementTeam, Hold: 5 * time.Second},
		{Target: runspec.PlacementCloud},
	}
	mac := placementCandidate{name: "mac", facts: claimFacts{Tier: tierSelfHosted, Labels: []string{"os=darwin"}, FreeSlots: 1}}
	got := placementTierWaits(tiers, []placementCandidate{mac}, nil, ready, ready)
	if len(got[0].Live) != 1 || len(got[1].Live) != 0 || got[2].OpensIn != 5*time.Second {
		t.Fatalf("the mac holds through its earliest tier only, so cloud opens in 5s: %+v", got)
	}
}
