package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/license"
)

func TestCheckCacheGrantKey_MultiTeamWithACacheRefusesAMissingKey(t *testing.T) {
	srv, _ := secretsTestServer(t, license.FeatureMultiTeam)
	for _, tc := range []struct {
		name, cacheURL, podURL, key, want string
	}{
		{"no key", "http://cache:8080", "", "", "SPARKWING_CACHE_GRANT_KEY"},
		{"no key, pod URL only", "", "https://cache.example", "", "SPARKWING_CACHE_GRANT_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCacheGrantKey(srv, tc.cacheURL, tc.podURL, tc.key)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkCacheGrantKey = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestCheckCacheGrantKey_StartsWithAKeyOrWhenNothingNeedsIt(t *testing.T) {
	multi, _ := secretsTestServer(t, license.FeatureMultiTeam)
	single, _ := secretsTestServer(t)
	if err := checkCacheGrantKey(multi, "http://cache:8080", "", "grant-key"); err != nil {
		t.Fatalf("a multi-team controller with a grant key: %v", err)
	}
	if err := checkCacheGrantKey(multi, "", "", ""); err != nil {
		t.Fatalf("a multi-team controller with no cache: %v", err)
	}
	if err := checkCacheGrantKey(single, "http://cache:8080", "", ""); err != nil {
		t.Fatalf("a single-team install with a cache and no grant key: %v", err)
	}
}
