package engine

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestLocalEngineUsesVerifiedCacheWithoutVersionPolicyNetwork(t *testing.T) {
	r, transport, key, _, now := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.10.2"}
	bundle := signedBundle(t, key, pin.Version, target, fixtureExecutable(t, pin.Version, target))
	release, err := VerifyRelease(pin.Version, target, bundle, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	cached, err := r.storeVerified(t.Context(), pin, release)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Index(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	transport.requests = nil
	transport.failure = true
	config := r.config
	config.LocalOnly = true
	local, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, ensure := range []bool{false, true} {
		var got Resolved
		if ensure {
			got, err = local.Ensure(t.Context(), cached.Identity, target)
		} else {
			got, err = local.Resolve(t.Context(), pin, target)
		}
		if err != nil || got.Identity != cached.Identity || len(got.Notices) == 0 {
			t.Fatalf("offline local engine: %+v: %v", got, err)
		}
	}
	if len(transport.requests) != 0 {
		t.Fatalf("local policy accessed the network: %v", transport.requests)
	}
}

func TestLocalPolicyPreservesVerifiedRestrictionsAndRunnerDenial(t *testing.T) {
	r, transport, key, policy, now := fixtureResolver(t)
	policy.Revoked = []Restriction{{Version: "v0.10.2", Reason: "security", Security: true, AnnouncedAt: now, EffectiveAt: now}}
	transport.policy(t, key, policy)
	if _, err := r.Index(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	config := r.config
	config.LocalOnly = true
	local, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	transport.requests = nil
	index, err := local.Index(t.Context(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Evaluate(index.Index, "v0.10.2", now)
	requireCode(t, err, hosterr.VersionRevoked)
	if len(transport.requests) != 0 {
		t.Fatal("local cached policy refreshed from network")
	}
	config.CacheDirectory = t.TempDir()
	config.HTTPClient = &http.Client{Transport: transport}
	empty, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	index, err = empty.Index(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EvaluateRunner(index.Index, "v0.10.2", now)
	requireCode(t, err, hosterr.Unavailable)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := empty.Index(ctx, now); err != context.Canceled {
		t.Fatalf("local cancellation lost: %v", err)
	}
}

func TestLocalPolicyRequiresSignedSDKMapping(t *testing.T) {
	r, transport, key, _, now := fixtureResolver(t)
	if _, err := r.Index(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.30.0"}
	release, err := VerifyRelease(pin.Version, target, signedBundle(t, key, pin.Version, target, fixtureExecutable(t, pin.Version, target)), signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.storeVerified(t.Context(), pin, release)
	if err != nil {
		t.Fatal(err)
	}
	transport.failure, transport.requests = true, nil
	config := r.config
	config.LocalOnly = true
	local, err := NewResolver(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = local.Resolve(t.Context(), pin, target)
	requireCode(t, err, hosterr.VersionUnsupported)
	if len(transport.requests) != 0 {
		t.Fatalf("local policy accessed the network: %v", transport.requests)
	}
}

func TestUpdateRefreshFillsThePolicyCacheLocalRunsRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	key := testSigningKey(t)
	policy, now := policyFixture()
	transport := &fixtureTransport{responses: map[string][]byte{}}
	transport.policy(t, key, policy)
	local, err := NewResolver(Config{TrustedKeys: signingKeys(key), LocalOnly: true, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if local.config.CacheDirectory != filepath.Join(home, "engines") {
		t.Fatalf("local runs read %s, not the Sparkwing home", local.config.CacheDirectory)
	}
	empty, err := local.Index(t.Context(), now)
	if err != nil || !empty.Index.Offline() || len(empty.Notices) != 1 || empty.Notices[0].Hint != PolicyRefreshHint || len(transport.requests) != 0 {
		t.Fatalf("empty local cache: %+v %v requests=%v", empty, err, transport.requests)
	}
	refresh, err := NewResolver(Config{TrustedKeys: signingKeys(key), HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refresh.Index(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	transport.requests = nil
	local, err = NewResolver(Config{TrustedKeys: signingKeys(key), LocalOnly: true, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := local.Index(t.Context(), now)
	if err != nil || cached.Index.Offline() || !cached.Index.Policy().PublishedAt.Equal(policy.PublishedAt) || len(transport.requests) != 0 {
		t.Fatalf("local run did not read the refreshed policy offline: %+v %v requests=%v", cached, err, transport.requests)
	}
}

func TestFreshLocalSDKMappingBootstrapsVerifiedPolicy(t *testing.T) {
	for _, pin := range []engineid.Pin{{SDK: engineid.GoSDK, Version: "v0.20.0"}, {SDK: engineid.TypeScriptSDK, Version: "0.20.0"}} {
		t.Run(string(pin.SDK), func(t *testing.T) {
			for _, failure := range []string{"", "offline", "signature"} {
				t.Run(failure, func(t *testing.T) {
					r, transport, _, policy, _ := fixtureResolver(t)
					t.Setenv("SPARKWING_HOME", t.TempDir())
					r.config.CacheDirectory = ""
					r.config.LocalOnly = true
					if failure == "offline" {
						transport.failure = true
					}
					if failure == "signature" {
						transport.policy(t, testSigningKey(t), policy)
					}
					local, err := NewResolver(r.config)
					if err != nil {
						t.Fatal(err)
					}
					version, err := local.EngineVersion(t.Context(), pin)
					if failure != "" {
						requireCode(t, err, hosterr.Unavailable)
						var refusal *apierr.Error
						if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, failure) || !strings.Contains(refusal.Hint, "sparkwing update") {
							t.Fatalf("missing cause or recovery: %v", err)
						}
						cached, cacheErr := local.Index(t.Context(), time.Now())
						if cacheErr != nil || !cached.Index.Offline() {
							t.Fatalf("unverified policy cached: %+v %v", cached, cacheErr)
						}
						return
					}
					if err != nil || version != "v0.20.0" {
						t.Fatalf("fresh mapping: %s %v", version, err)
					}
					transport.failure, transport.requests = true, nil
					again, err := NewResolver(r.config)
					if err != nil {
						t.Fatal(err)
					}
					version, err = again.EngineVersion(t.Context(), pin)
					if err != nil || version != "v0.20.0" || len(transport.requests) != 0 {
						t.Fatalf("cached mapping: %s %v requests=%v", version, err, transport.requests)
					}
				})
			}
		})
	}
}
