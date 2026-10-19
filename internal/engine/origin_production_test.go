//go:build !qatrust

package engine

import (
	"net/url"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func TestDefaultBuildTrustsOnlyProductionKeyAndOrigins(t *testing.T) {
	if !slices.Equal(releaseauth.TrustedPublicKeys, []string{releaseauth.ProductionPublicKey}) {
		t.Fatalf("default build trusts %v, want only the production key", releaseauth.TrustedPublicKeys)
	}
	if VersionIndexURL != "https://sparkwing.dev/versions.json" || VersionIndexSignatureURL != "https://sparkwing.dev/versions.json.sig" || ReleaseBase != "https://github.com/sparkwing-dev/sparkwing/releases/download" {
		t.Fatalf("default build reads %s, %s and %s", VersionIndexURL, VersionIndexSignatureURL, ReleaseBase)
	}
	for address, release := range map[string]bool{
		"https://sparkwing.dev/versions.json":                                                  false,
		"https://sparkwing.dev/versions.json.sig":                                              false,
		"https://github.com/sparkwing-dev/sparkwing/releases/download/v0.68.0/SHA256SUMS":      true,
		"https://release-assets.githubusercontent.com/github-production-release-asset/1/asset": true,
	} {
		if !allowedURL(mustURL(t, address), release) {
			t.Errorf("default build refused production origin %s", address)
		}
	}
	for address, release := range map[string]bool{
		"https://qa-releases.sparkwing.dev/versions.json":                                false,
		"https://qa-releases.sparkwing.dev/releases/download/v0.68.0-qa.1/SHA256SUMS":    true,
		"https://github.com/someone-else/sparkwing/releases/download/v0.68.0/SHA256SUMS": true,
	} {
		if allowedURL(mustURL(t, address), release) {
			t.Errorf("default build admitted %s", address)
		}
	}
	if releaseBuild("v0.68.0-qa.1") {
		t.Error("default build treats a pre-release as a policy-enforcing release")
	}
}

func mustURL(t *testing.T, address string) *url.URL {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
