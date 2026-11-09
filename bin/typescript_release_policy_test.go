package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestTypeScriptReleaseRetainsPublishedSDKAcrossSignedEngineReleases(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	policyPath, entryPath := filepath.Join(dir, "policy.json"), filepath.Join(dir, "sdk-entry-typescript.json")
	runDecision := func(wantPublish bool) []byte {
		t.Helper()
		command := exec.Command("node", filepath.Join(root, "bin/typescript-release.mjs"), root, policyPath, entryPath, root)
		command.Env = append(os.Environ(), "GITHUB_OUTPUT="+filepath.Join(dir, "outputs"))
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("SDK publication decision: %v\n%s", err, output)
		}
		var decision struct {
			Publish bool
			Entry   engine.SDKRelease
		}
		if err := json.Unmarshal(output, &decision); err != nil {
			t.Fatal(err)
		}
		if decision.Publish != wantPublish || decision.Entry.Language != engineid.TypeScriptSDK {
			t.Fatalf("publication decision: %s", output)
		}
		body, err := os.ReadFile(entryPath)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	writePolicy := func(policy engine.VersionIndex) {
		t.Helper()
		body, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(policyPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	policy := engine.VersionIndex{Format: engine.VersionIndexFormat, PublishedAt: now, Latest: "v0.89.0", Versions: []engineid.VersionInfo{{
		Version: "v0.89.0", PublishedAt: now, SupportUntil: now.AddDate(0, limits.ControllerCompatibilityMonths, 0), AcceptedSDKProtocols: []int{2},
	}}}
	initial, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	verifiedInitial, err := engine.VerifyIndex(engine.SignedIndex{Body: initial, Signature: ed25519.Sign(private, initial)}, now, []ed25519.PublicKey{public})
	if err != nil {
		t.Fatal(err)
	}
	writePolicy(verifiedInitial.Policy())
	entry := runDecision(true)
	for _, version := range []string{"v0.90.0", "v0.91.0"} {
		policy.Latest = version
		policy.Versions = append(policy.Versions, engineid.VersionInfo{
			Version: version, PublishedAt: now,
			SupportUntil: now.AddDate(0, limits.ControllerCompatibilityMonths, 0), AcceptedSDKProtocols: []int{2},
		})
		policy, err = engine.MergeSDKEntries(policy, entry)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: ed25519.Sign(private, body)}, now, []ed25519.PublicKey{public})
		if err != nil {
			t.Fatal(err)
		}
		policy = verified.Policy()
		if len(policy.SDKs) != 1 || policy.SDKs[0].Version == version {
			t.Fatalf("SDK publication coupled to engine release: %+v", policy)
		}
		pin := engineid.Pin{SDK: engineid.TypeScriptSDK, Version: strings.TrimPrefix(policy.SDKs[0].Version, "v")}
		sdkVersion, err := engine.SDKVersionForPin(pin)
		if err != nil {
			t.Fatal(err)
		}
		selected, _, err := engine.SelectSDK(verified, pin.SDK, sdkVersion, now)
		if err != nil || selected.Version != policy.SDKs[0].Version {
			t.Fatalf("npm pin did not resolve the signed SDK entry: %+v %v", selected, err)
		}
		resolver, err := engine.NewResolver(engine.Config{
			CacheDirectory: t.TempDir(), BuildVersion: version, TrustedKeys: []ed25519.PublicKey{public},
			HTTPClient: &http.Client{Transport: sdkPolicyTransport{
				engine.VersionIndexURL:          body,
				engine.VersionIndexSignatureURL: ed25519.Sign(private, body),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		engineVersion, err := resolver.EngineVersion(t.Context(), pin)
		if err != nil || engineVersion != version {
			t.Fatalf("SDK %s selected engine %s, want %s: %v", pin.Version, engineVersion, version, err)
		}
		writePolicy(policy)
		entry = runDecision(false)
	}
}

type sdkPolicyTransport map[string][]byte

func (transport sdkPolicyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, ok := transport[request.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected release request: %s", request.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}
