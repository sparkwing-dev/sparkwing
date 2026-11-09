package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func TestIndependentSDKEngineResolution(t *testing.T) {
	r, transport, key, policy, now := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"}
	var accepted engineid.Identity
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.20.0"}
	for _, version := range []string{"v0.21.0", "v0.22.0"} {
		policy.Versions = append(policy.Versions, engineid.VersionInfo{Version: version, PublishedAt: now, SupportUntil: now.AddDate(1, 0, 0), AcceptedSDKProtocols: []int{3}})
		policy.Latest = version
		policy.PublishedAt = policy.PublishedAt.Add(time.Second)
		transport.policy(t, key, policy)
		transport.release(t, version, target, signedBundle(t, key, version, target, fixtureExecutable(t, version, target)))
		resolver, err := NewResolver(r.config)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := resolver.Resolve(t.Context(), pin, target)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Identity.Pin.Version != "v0.20.0" || resolved.Identity.Version != version {
			t.Fatalf("identities conflated: %+v", resolved.Identity)
		}
		if accepted.Version != "" {
			got, err := resolver.Ensure(t.Context(), accepted, target)
			if err != nil || got.Identity != accepted {
				t.Fatalf("accepted engine changed after release: %+v %v", got, err)
			}
		}
		accepted = resolved.Identity
		if _, err := resolver.Ensure(t.Context(), resolved.Identity, target); err != nil {
			t.Fatal(err)
		}
		rejected := resolved.Identity
		rejected.Version = "v0.10.2"
		if _, err := resolver.Ensure(t.Context(), rejected, target); err == nil || !strings.Contains(err.Error(), "does not accept") {
			t.Fatalf("unaccepted protocol: %v", err)
		}
	}
	policy.PublishedAt = policy.PublishedAt.Add(time.Minute)
	policy.SDKs[1].Revoked = &Restriction{Version: "v0.20.0", Reason: "SDK-only vulnerability", Security: true, AnnouncedAt: now, EffectiveAt: now}
	transport.policy(t, key, policy)
	resolver, err := NewResolver(r.config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(t.Context(), pin, target)
	requireCode(t, err, hosterr.VersionRevoked)
	_, err = resolver.Ensure(t.Context(), accepted, target)
	requireCode(t, err, hosterr.VersionRevoked)
}

func TestSignedSDKSelectionAndRevocation(t *testing.T) {
	key := testSigningKey(t)
	policy, now := policyFixture()
	policy.SDKs = append(policy.SDKs, SDKRelease{Language: engineid.GoSDK, Version: "v0.21.0", Protocol: 3, SourceDigest: strings.Repeat("d", 64), Revoked: &Restriction{Version: "v0.21.0", Reason: "compromised SDK", Security: true, AnnouncedAt: now, EffectiveAt: now}})
	index, err := VerifyIndex(signPolicy(t, key, policy), now, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	sdk, _, err := SelectSDK(index, engineid.GoSDK, "", now)
	if err != nil || sdk.Version != "v0.20.0" {
		t.Fatalf("latest permitted SDK: %+v %v", sdk, err)
	}
	_, _, err = SelectSDK(index, engineid.GoSDK, "v0.21.0", now)
	requireCode(t, err, hosterr.VersionRevoked)
	if !strings.Contains(err.Error(), "SDK") {
		t.Fatalf("SDK revocation lost its identity: %v", err)
	}
	if _, err := Evaluate(index, "v0.20.0", now); err != nil {
		t.Fatalf("SDK revocation affected engine: %v", err)
	}
	for _, mutate := range []func(*VersionIndex){
		func(p *VersionIndex) { p.SDKs = append(p.SDKs, p.SDKs[0]) },
		func(p *VersionIndex) { p.SDKs[0].Protocol = 0 },
		func(p *VersionIndex) { p.SDKs[0].SourceDigest = "bad" },
		func(p *VersionIndex) { p.Versions[0].AcceptedSDKProtocols = []int{2, 2} },
	} {
		p := index.Policy()
		mutate(&p)
		if _, err := VerifyIndex(signPolicy(t, key, p), now, signingKeys(key)); err == nil {
			t.Fatal("invalid signed mapping accepted")
		}
	}
	signed := signPolicy(t, key, policy)
	var edited VersionIndex
	if err := json.Unmarshal(signed.Body, &edited); err != nil {
		t.Fatal(err)
	}
	edited.SDKs[0].Protocol++
	signed.Body, _ = json.Marshal(edited)
	if _, err := VerifyIndex(signed, now, signingKeys(key)); err == nil {
		t.Fatal("unsigned SDK mapping edit accepted")
	}
	copy := index.Policy()
	copy.Versions[0].AcceptedSDKProtocols[0] = 99
	copy.SDKs[len(copy.SDKs)-1].Revoked.Reason = "mutated"
	if index.Policy().Versions[0].AcceptedSDKProtocols[0] == 99 || index.Policy().SDKs[len(copy.SDKs)-1].Revoked.Reason == "mutated" {
		t.Fatal("policy copy aliases signed authority")
	}
}

func TestSDKPolicyAdvanceRetainsSignedMapping(t *testing.T) {
	key := testSigningKey(t)
	policy, now := policyFixture()
	previous, err := VerifyIndex(signPolicy(t, key, policy), now, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*VersionIndex){
		func(p *VersionIndex) { p.SDKs = p.SDKs[1:] },
		func(p *VersionIndex) { p.SDKs[0].Protocol++ },
		func(p *VersionIndex) { p.SDKs[0].SourceDigest = strings.Repeat("d", 64) },
		func(p *VersionIndex) { p.Versions[0].AcceptedSDKProtocols = []int{3} },
	} {
		nextPolicy := previous.Policy()
		nextPolicy.PublishedAt = nextPolicy.PublishedAt.Add(time.Second)
		mutate(&nextPolicy)
		next, err := VerifyIndex(signPolicy(t, key, nextPolicy), now, signingKeys(key))
		if err != nil {
			t.Fatal(err)
		}
		if err := checkPolicyAdvance(previous, next); err == nil {
			t.Fatal("changed retained SDK mapping accepted")
		}
	}
	nextPolicy := previous.Policy()
	nextPolicy.PublishedAt = nextPolicy.PublishedAt.Add(time.Second)
	nextPolicy.SDKs[0].Revoked = &Restriction{Version: nextPolicy.SDKs[0].Version, Reason: "security", Security: true, AnnouncedAt: now, EffectiveAt: now}
	next, err := VerifyIndex(signPolicy(t, key, nextPolicy), now, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPolicyAdvance(previous, next); err != nil {
		t.Fatalf("SDK revocation refused: %v", err)
	}
}

func TestDevelopmentHostResolvesSignedSDKMapping(t *testing.T) {
	r, _, _, _, _ := fixtureResolver(t)
	r.config.BuildVersion = "(devel)"
	version, err := r.EngineVersion(t.Context(), engineid.Pin{SDK: engineid.GoSDK, Version: "v0.20.0"})
	if err != nil || version != "v0.20.0" {
		t.Fatalf("development host mapping: %s %v", version, err)
	}
}
