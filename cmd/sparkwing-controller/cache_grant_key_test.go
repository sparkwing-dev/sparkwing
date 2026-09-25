package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/license"
)

func TestCheckCacheGrantKey_MultiTeamWithACacheRefusesAMissingOrSharedKey(t *testing.T) {
	srv, _ := secretsTestServer(t, license.FeatureMultiTeam)
	for _, tc := range []struct {
		name, cacheURL, podURL, key, token, want string
	}{
		{"no key", "http://cache:8080", "", "", "op-token", "SPARKWING_CACHE_GRANT_KEY"},
		{"no key, pod URL only", "", "https://cache.example", "", "", "SPARKWING_CACHE_GRANT_KEY"},
		{"key is the operator token", "http://cache:8080", "", "op-token", "op-token", "operator token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCacheGrantKey(srv, tc.cacheURL, tc.podURL, tc.key, tc.token)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkCacheGrantKey = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestCheckCacheGrantKey_StartsWhenTheKeyIsItsOwnOrNothingNeedsIt(t *testing.T) {
	multi, _ := secretsTestServer(t, license.FeatureMultiTeam)
	single, _ := secretsTestServer(t)
	if err := checkCacheGrantKey(multi, "http://cache:8080", "", "grant-key", "op-token"); err != nil {
		t.Fatalf("a multi-team controller with its own grant key: %v", err)
	}
	if err := checkCacheGrantKey(multi, "", "", "", ""); err != nil {
		t.Fatalf("a multi-team controller with no cache: %v", err)
	}
	if err := checkCacheGrantKey(single, "http://cache:8080", "", "", "op-token"); err != nil {
		t.Fatalf("a single-team install with a cache and no grant key: %v", err)
	}
	if err := checkCacheGrantKey(single, "http://cache:8080", "", "op-token", "op-token"); err != nil {
		t.Fatalf("a single-team install keeps its request-time answer: %v", err)
	}
}
