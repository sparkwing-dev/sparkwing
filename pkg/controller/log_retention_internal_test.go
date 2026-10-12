package controller

import (
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

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

func TestLifecycleRuleMustExpireTheClassPrefixAfterItsDays(t *testing.T) {
	rule := func(prefix string, days int32, enabled bool) types.LifecycleRule {
		r := types.LifecycleRule{
			Filter:     &types.LifecycleRuleFilter{Prefix: aws.String(prefix)},
			Expiration: &types.LifecycleExpiration{Days: aws.Int32(days)},
			Status:     types.ExpirationStatusDisabled,
		}
		if enabled {
			r.Status = types.ExpirationStatusEnabled
		}
		return r
	}
	const prefix = "cache/logs/r90/"
	for name, tc := range map[string]struct {
		rules []types.LifecycleRule
		want  bool
	}{
		"exact rule":         {[]types.LifecycleRule{rule("cache/logs/r90/", 90, true)}, true},
		"broader rule":       {[]types.LifecycleRule{rule("cache/logs/", 90, true)}, true},
		"wrong days":         {[]types.LifecycleRule{rule("cache/logs/r90/", 30, true)}, false},
		"disabled":           {[]types.LifecycleRule{rule("cache/logs/r90/", 90, false)}, false},
		"another prefix":     {[]types.LifecycleRule{rule("cache/logs/r30/", 90, true)}, false},
		"no rules":           {nil, false},
		"tagged filter only": {[]types.LifecycleRule{{Status: types.ExpirationStatusEnabled, Expiration: &types.LifecycleExpiration{Days: aws.Int32(90)}, Filter: &types.LifecycleRuleFilter{Tag: &types.Tag{Key: aws.String("k"), Value: aws.String("v")}}}}, false},
	} {
		if got := lifecycleExpires(tc.rules, prefix, 90); got != tc.want {
			t.Errorf("%s: lifecycleExpires = %v, want %v", name, got, tc.want)
		}
	}
}
