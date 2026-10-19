//go:build qatrust

package engine

import (
	"net/url"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func TestQATrustBuildAddsOnlyTheQAKeyAndOrigin(t *testing.T) {
	if !slices.Equal(releaseauth.TrustedPublicKeys, []string{releaseauth.ProductionPublicKey, releaseauth.QAPublicKey}) {
		t.Fatalf("qatrust build trusts %v, want the production and QA keys", releaseauth.TrustedPublicKeys)
	}
	if _, err := releaseauth.PublicKeys(); err != nil {
		t.Fatalf("QA public key does not decode: %v", err)
	}
	if VersionIndexURL != "https://qa-releases.sparkwing.dev/versions.json" || VersionIndexSignatureURL != "https://qa-releases.sparkwing.dev/versions.json.sig" || ReleaseBase != "https://qa-releases.sparkwing.dev/releases/download" {
		t.Fatalf("qatrust build reads %s, %s and %s", VersionIndexURL, VersionIndexSignatureURL, ReleaseBase)
	}
	for address, release := range map[string]bool{
		"https://qa-releases.sparkwing.dev/versions.json":                             false,
		"https://qa-releases.sparkwing.dev/versions.json.sig":                         false,
		"https://qa-releases.sparkwing.dev/releases/download/v0.68.0-qa.1/SHA256SUMS": true,
	} {
		if !allowedURL(mustURL(t, address), release) {
			t.Errorf("qatrust build refused QA origin %s", address)
		}
	}
	for address, release := range map[string]bool{
		"https://sparkwing.dev/versions.json":                                                  false,
		"https://qa-releases.sparkwing.dev/other/versions.json":                                false,
		"https://qa-releases.sparkwing.dev/elsewhere/SHA256SUMS":                               true,
		"https://github.com/sparkwing-dev/sparkwing/releases/download/v0.68.0/SHA256SUMS":      true,
		"https://release-assets.githubusercontent.com/github-production-release-asset/1/asset": true,
		"http://qa-releases.sparkwing.dev/releases/download/v0.68.0-qa.1/SHA256SUMS":           true,
	} {
		if allowedURL(mustURL(t, address), release) {
			t.Errorf("qatrust build admitted %s", address)
		}
	}
	if !releaseBuild("v0.68.0-qa.1") {
		t.Error("qatrust build treats a QA pre-release as a development build")
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
