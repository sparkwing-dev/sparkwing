package engine

import (
	"context"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

func TestLocalEngineUsesVerifiedCacheWithoutVersionPolicyNetwork(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
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
		if err != nil || got.Identity != cached.Identity || len(got.Notices) != 1 || !strings.Contains(got.Notices[0].Reason, "version policy") {
			t.Fatalf("offline local engine: %+v: %v", got, err)
		}
	}
	if len(transport.requests) != 0 {
		t.Fatalf("local policy accessed the network: %v", transport.requests)
	}
	_, err = r.Resolve(t.Context(), pin, target)
	requireCode(t, err, apierr.Unavailable)
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
	requireCode(t, err, apierr.VersionRevoked)
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
	requireCode(t, err, apierr.Unavailable)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := empty.Index(ctx, now); err != context.Canceled {
		t.Fatalf("local cancellation lost: %v", err)
	}
}

// Staging qa.9: a local run pinned to a release published after the cached
// policy failed as "not listed", though a machine with no cache runs it.
func TestLocalPolicyAdmitsAReleaseNewerThanTheCache(t *testing.T) {
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
	cached, err := r.storeVerified(t.Context(), pin, release)
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
	got, err := local.Resolve(t.Context(), pin, target)
	if err != nil || got.Identity != cached.Identity || !slices.ContainsFunc(got.Notices, func(n engineid.Notice) bool { return n.Kind == engineid.NoticePolicyUnavailable }) {
		t.Fatalf("newer release under a cached policy: %+v: %v", got, err)
	}
	if len(transport.requests) != 0 {
		t.Fatalf("local policy accessed the network: %v", transport.requests)
	}
}
