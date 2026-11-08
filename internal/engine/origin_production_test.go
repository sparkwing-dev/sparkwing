//go:build !qatrust

package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func TestDefaultBuildTrustsOnlyProductionKeyAndOrigins(t *testing.T) {
	if !slices.Equal(releaseauth.TrustedPublicKeys(), []string{releaseauth.ProductionPublicKey}) {
		t.Fatalf("default build trusts %v, want only the production key", releaseauth.TrustedPublicKeys())
	}
	if VersionIndexURL != "https://sparkwing.dev/versions.json" || VersionIndexSignatureURL != "https://sparkwing.dev/versions.json.sig" || ReleaseBase != "https://github.com/sparkwing-dev/sparkwing/releases/download" {
		t.Fatalf("default build reads %s, %s and %s", VersionIndexURL, VersionIndexSignatureURL, ReleaseBase)
	}
	for address, release := range map[string]bool{
		"https://sparkwing.dev/versions.json":                                                  false,
		"https://sparkwing.dev/versions.json.sig":                                              false,
		"https://github.com/sparkwing-dev/sparkwing/releases/download/v0.68.0/SHA256SUMS":      true,
		"https://release-assets.githubusercontent.com/github-production-release-asset/1/asset": true,
		"https://api.github.com/repos/sparkwing-dev/sparkwing/releases/tags/v0.70.0":           true,
		"https://api.github.com/repos/sparkwing-dev/sparkwing/releases/assets/42":              true,
	} {
		if !allowedURL(mustURL(t, address), release) {
			t.Errorf("default build refused production origin %s", address)
		}
	}
	for address, release := range map[string]bool{
		"https://qa-releases.sparkwing.dev/versions.json":                                false,
		"https://qa-releases.sparkwing.dev/releases/download/v0.68.0-qa.1/SHA256SUMS":    true,
		"https://github.com/someone-else/sparkwing/releases/download/v0.68.0/SHA256SUMS": true,
		"https://api.github.com/repos/someone-else/sparkwing/releases/assets/42":         true,
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

func TestEngineReleaseAPIFallbackVerifiesBytes(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	version := "v0.70.0"
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	bundle := signedBundle(t, key, version, target, fixtureExecutable(t, version, target))
	transport.release(t, version, target, bundle)
	var assets []map[string]any
	api := "https://api.github.com/repos/sparkwing-dev/sparkwing/releases/"
	for address, body := range transport.responses {
		if !strings.HasPrefix(address, ReleaseBase+"/") {
			continue
		}
		name := address[strings.LastIndex(address, "/")+1:]
		id := len(assets) + 1
		assets = append(assets, map[string]any{"id": id, "name": name})
		transport.responses[fmt.Sprintf("%sassets/%d", api, id)] = body
	}
	metadata, err := json.Marshal(map[string]any{"assets": assets})
	if err != nil {
		t.Fatal(err)
	}
	transport.responses[api+"tags/"+version] = metadata
	r.config.HTTPClient.Transport = installedTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.String(), ReleaseBase+"/") {
			return &http.Response{StatusCode: http.StatusGatewayTimeout, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: req}, nil
		}
		return transport.RoundTrip(req)
	})
	if _, err := r.fetchRelease(t.Context(), version, target); err != nil {
		t.Fatalf("verified API release: %v", err)
	}
	bundle.AssetSignature[0] ^= 1
	name, err := target.NameForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		if asset["name"] == name+".sig" {
			transport.responses[fmt.Sprintf("%sassets/%d", api, asset["id"])] = bundle.AssetSignature
		}
	}
	if _, err := r.fetchRelease(t.Context(), version, target); err == nil {
		t.Fatal("accepted API asset with a bad signature")
	}
}
